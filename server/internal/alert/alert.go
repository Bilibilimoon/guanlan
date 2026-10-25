// Package alert 阈值告警：规则存 SQLite meta，周期评估，历史落表。
package alert

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"guanlan/server/internal/collect"
	"guanlan/server/internal/store"
)

type Rule struct {
	ID        string  `json:"id"`
	Metric    string  `json:"metric"` // cpu/mem_pct/swap_pct/load1/fan/temp.any/temp.disk/fs
	Op        string  `json:"op"`     // ">" / "<"
	Threshold float64 `json:"threshold"`
	ForMin    int     `json:"forMin"`
	Level     string  `json:"level"` // warn / crit
	Enabled   bool    `json:"enabled"`
}

type Alert struct {
	ID       int64   `json:"id"`
	Started  int64   `json:"started"`
	Resolved int64   `json:"resolved"` // 0 = 活跃
	RuleID   string  `json:"ruleId"`
	Metric   string  `json:"metric"`
	Value    float64 `json:"value"`
	Level    string  `json:"level"`
	Acked    bool    `json:"acked"`
}

func DefaultRules() []Rule {
	return []Rule{
		{ID: "cpu_high", Metric: "cpu", Op: ">", Threshold: 90, ForMin: 5, Level: "warn", Enabled: true},
		{ID: "mem_high", Metric: "mem_pct", Op: ">", Threshold: 92, ForMin: 5, Level: "warn", Enabled: true},
		{ID: "soc_hot", Metric: "temp.any", Op: ">", Threshold: 85, ForMin: 2, Level: "crit", Enabled: true},
		{ID: "disk_hot", Metric: "temp.disk", Op: ">", Threshold: 60, ForMin: 2, Level: "crit", Enabled: true},
		{ID: "fan_stall", Metric: "fan", Op: "<", Threshold: 500, ForMin: 5, Level: "crit", Enabled: true},
		{ID: "load_high", Metric: "load1", Op: ">", Threshold: 16, ForMin: 5, Level: "warn", Enabled: true},
		{ID: "fs_full", Metric: "fs", Op: ">", Threshold: 95, ForMin: 5, Level: "warn", Enabled: true},
	}
}

type Evaluator struct {
	col *collect.Collector
	st  *store.Store
	mu  sync.Mutex
}

func New(col *collect.Collector, st *store.Store) *Evaluator {
	e := &Evaluator{col: col, st: st}
	e.initTable()
	e.loadRules() // 无规则时写入默认
	return e
}

func (e *Evaluator) initTable() {
	e.st.DB().Exec(`CREATE TABLE IF NOT EXISTS alerts(
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		started INTEGER NOT NULL, resolved INTEGER DEFAULT 0,
		rule_id TEXT NOT NULL, metric TEXT NOT NULL, value REAL, level TEXT, acked INTEGER DEFAULT 0
	)`)
}

func (e *Evaluator) loadRules() []Rule {
	var out []Rule
	if v, ok := e.st.GetMeta("alert_rules"); ok {
		if json.Unmarshal([]byte(v), &out) == nil && len(out) > 0 {
			return out
		}
	}
	out = DefaultRules()
	e.SaveRules(out)
	return out
}

func (e *Evaluator) SaveRules(rules []Rule) {
	b, _ := json.Marshal(rules)
	e.st.SetMeta("alert_rules", string(b))
}

// Rules 返回当前规则（供 API 展示）。
func (e *Evaluator) Rules() []Rule {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loadRules()
}

// metricValue 从样本取规则指标的当前值。
func metricValue(metric string, sm collect.Sample) (float64, bool) {
	switch metric {
	case "cpu":
		return sm.CPUTotal, true
	case "mem_pct":
		if sm.MemTotal == 0 {
			return 0, false
		}
		return float64(sm.MemUsed) / float64(sm.MemTotal) * 100, true
	case "swap_pct":
		if sm.SwapTotal == 0 {
			return 0, false
		}
		return float64(sm.SwapUsed) / float64(sm.SwapTotal) * 100, true
	case "load1":
		return sm.Load1, true
	case "fan":
		return float64(sm.Fan), true
	case "temp.any":
		return maxTemp(sm, true), true
	case "temp.disk":
		return maxTemp(sm, false), true
	case "fs":
		m := -1.0
		for _, mo := range sm.Mounts {
			if mo.UsedPct() > m {
				m = mo.UsedPct()
			}
		}
		return m, m >= 0
	}
	return 0, false
}

// maxTemp: soc=true 取 SoC 各路（key 含 -thermal），否则取硬盘温度。
func maxTemp(sm collect.Sample, soc bool) float64 {
	m := -273.0
	for k, v := range sm.Temps {
		isSoc := strings.Contains(k, "-thermal")
		if isSoc != soc {
			continue
		}
		if v > m {
			m = v
		}
	}
	return m
}

func breach(r Rule, v float64) bool {
	if r.Op == "<" {
		return v < r.Threshold
	}
	return v > r.Threshold
}

// sustained: 最近 ForMin 分钟内超限样本占比 ≥ 80%。
// 环形缓冲只保留最近 15 分钟，ForMin 上限 15（API/导入校验同步此约束）。
func (e *Evaluator) sustained(r Rule) bool {
	if r.ForMin > 15 {
		r.ForMin = 15
	}
	sec := r.ForMin * 60
	if sec < 60 {
		sec = 60
	}
	ring := e.col.Snapshot(float64(sec))
	if len(ring) < 3 {
		return false
	}
	hit := 0
	for _, sm := range ring {
		if v, ok := metricValue(r.Metric, sm); ok && breach(r, v) {
			hit++
		}
	}
	return float64(hit)/float64(len(ring)) >= 0.8
}

// Eval 跑一轮评估（由 ticker 调用）。
func (e *Evaluator) Eval() {
	e.mu.Lock()
	defer e.mu.Unlock()
	rules := e.loadRules()
	sm := e.col.Latest()
	if sm.T == 0 {
		return
	}
	db := e.st.DB()
	now := time.Now().Unix()
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		v, ok := metricValue(r.Metric, sm)
		if !ok {
			continue
		}
		var activeID int64
		db.QueryRow(`SELECT id FROM alerts WHERE rule_id=? AND resolved=0 ORDER BY id DESC LIMIT 1`, r.ID).Scan(&activeID)
		if breach(r, v) && activeID == 0 && e.sustained(r) {
			if _, err := db.Exec(`INSERT INTO alerts(started, rule_id, metric, value, level) VALUES(?,?,?,?,?)`,
				now, r.ID, r.Metric, v, r.Level); err != nil {
				log.Printf("告警写入失败: %v", err)
			}
		} else if !breach(r, v) && activeID != 0 {
			db.Exec(`UPDATE alerts SET resolved=? WHERE id=?`, now, activeID)
		} else if activeID != 0 && breach(r, v) {
			db.Exec(`UPDATE alerts SET value=? WHERE id=?`, v, activeID) // 活跃告警刷新当前值
		}
	}
}

// Snapshot 供 API：未读数、活跃告警、近期历史、规则。
func (e *Evaluator) Snapshot() (unread int, active []Alert, recent []Alert) {
	db := e.st.DB()
	db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE resolved=0 AND acked=0`).Scan(&unread)
	rows, err := db.Query(`SELECT id, started, resolved, rule_id, metric, value, level, acked
		FROM alerts WHERE resolved=0 ORDER BY started DESC LIMIT 50`)
	if err == nil {
		for rows.Next() {
			var a Alert
			var acked int
			rows.Scan(&a.ID, &a.Started, &a.Resolved, &a.RuleID, &a.Metric, &a.Value, &a.Level, &acked)
			a.Acked = acked != 0
			active = append(active, a)
		}
		rows.Close()
	}
	rows, err = db.Query(`SELECT id, started, resolved, rule_id, metric, value, level, acked
		FROM alerts ORDER BY started DESC LIMIT 100`)
	if err == nil {
		for rows.Next() {
			var a Alert
			var acked int
			rows.Scan(&a.ID, &a.Started, &a.Resolved, &a.RuleID, &a.Metric, &a.Value, &a.Level, &acked)
			a.Acked = acked != 0
			recent = append(recent, a)
		}
		rows.Close()
	}
	return unread, active, recent
}

func (e *Evaluator) Ack(id int64) error {
	_, err := e.st.DB().Exec(`UPDATE alerts SET acked=1 WHERE id=?`, id)
	return err
}
