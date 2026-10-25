// Package store 窄表时序存储：metrics 字典 + points 分层表（L0/L1/L2）。
package store

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"guanlan/server/internal/collect"
)

// 各层保留时长（秒）
const (
	RetainL0 = 30 * 86400  // 原始粒度
	RetainL1 = 180 * 86400 // 5 分钟聚合
	RetainL2 = 365 * 86400 // 1 小时聚合
)

type Store struct {
	db *sql.DB

	mu    sync.RWMutex
	ids   map[string]int64 // metric name -> id 缓存
	stmts map[string]*sql.Stmt
}

// Open 打开/初始化数据库（WAL、synchronous=NORMAL、页缓存限 2MB）。
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(1)&_pragma=busy_timeout(5000)&_pragma=cache_size(-2000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 单写连接足够（写入方只有落盘 goroutine），避免 SQLITE_BUSY
	db.SetMaxOpenConns(2)

	s := &Store{db: db, ids: map[string]int64{}, stmts: map[string]*sql.Stmt{}}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS metrics(
  id   INTEGER PRIMARY KEY,
  name TEXT UNIQUE NOT NULL
);
CREATE TABLE IF NOT EXISTS points_l0(
  t INTEGER NOT NULL, k INTEGER NOT NULL, v REAL NOT NULL,
  PRIMARY KEY(k, t)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS points_l1(
  t INTEGER NOT NULL, k INTEGER NOT NULL, v REAL NOT NULL,
  PRIMARY KEY(k, t)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS points_l2(
  t INTEGER NOT NULL, k INTEGER NOT NULL, v REAL NOT NULL,
  PRIMARY KEY(k, t)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS meta(
  key TEXT PRIMARY KEY,
  val TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_metrics_name ON metrics(name);
`)
	return err
}

func (s *Store) Close() error {
	for _, st := range s.stmts {
		st.Close()
	}
	return s.db.Close()
}

// DB 暴露底层连接（告警等包建表用）。
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) metricID(tx *sql.Tx, name string) (int64, error) {
	s.mu.RLock()
	id, ok := s.ids[name]
	s.mu.RUnlock()
	if ok {
		return id, nil
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO metrics(name) VALUES(?)`, name); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(`SELECT id FROM metrics WHERE name=?`, name).Scan(&id); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.ids[name] = id
	s.mu.Unlock()
	return id, nil
}

// WriteSample 把一个样本的序列值写入 L0（单事务批量）。
func (s *Store) WriteSample(sm collect.Sample) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO points_l0(t,k,v) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	series := sm.Series()
	tSec := int64(sm.T) // 落盘取整秒（落盘粒度 ≥5s，无亚秒需求）
	for name, v := range series {
		id, err := s.metricID(tx, name)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(tSec, id, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Query 返回 level 层（l0/l1/l2）指定指标在 [start,end] 内的序列。
func (s *Store) Query(level string, names []string, start, end int64) (map[string][][2]float64, error) {
	switch level {
	case "l0", "l1", "l2":
	default:
		level = "l0"
	}
	out := map[string][][2]float64{}
	for _, name := range names {
		if len(name) > 128 {
			out[name] = nil // 超长名（异常输入）直接视为无数据
			continue
		}
		// 不在读锁内回填缓存：Query 会并发调用，读锁内写 map 会 panic
		s.mu.RLock()
		id, cached := s.ids[name]
		s.mu.RUnlock()
		if !cached {
			var idv int64
			err := s.db.QueryRow(`SELECT id FROM metrics WHERE name=?`, name).Scan(&idv)
			if err != nil {
				out[name] = nil // 未知指标（尚无数据）不算错误
				continue
			}
			id = idv
		}
		rows, err := s.db.Query(
			`SELECT t, v FROM points_`+level+` WHERE k=? AND t BETWEEN ? AND ? ORDER BY t`,
			id, start, end)
		if err != nil {
			return nil, err
		}
		pts := [][2]float64{}
		for rows.Next() {
			var t int64
			var v float64
			if err := rows.Scan(&t, &v); err != nil {
				rows.Close()
				return nil, err
			}
			pts = append(pts, [2]float64{float64(t), v})
		}
		rows.Close()
		out[name] = pts
	}
	return out, nil
}

// KnownMetrics 返回已有数据的指标名（供前端指标选择与估算器）。
func (s *Store) KnownMetrics() []string {
	rows, err := s.db.Query(`SELECT name FROM metrics ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}

// GetMeta / SetMeta 读写键值配置（采集间隔等，重启后恢复）。
func (s *Store) GetMeta(key string) (string, bool) {
	var v string
	err := s.db.QueryRow(`SELECT val FROM meta WHERE key=?`, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

func (s *Store) SetMeta(key, val string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key,val) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET val=excluded.val`, key, val)
	return err
}

// Retain 滚动删除各层过期数据；保留天数可由 meta 覆盖（day_l0/l1/l2）。
func (s *Store) Retain() (string, error) {
	now := nowUnix()
	ret := func(key string, def int64) int64 {
		if v, ok := s.GetMeta(key); ok {
			if d, err := strconv.ParseInt(v, 10, 64); err == nil && d >= 1 && d <= 3650 {
				return d * 86400
			}
		}
		return def
	}
	var sb strings.Builder
	for _, l := range []struct {
		name string
		def  int64
	}{
		{"l0", RetainL0}, {"l1", RetainL1}, {"l2", RetainL2},
	} {
		cutoff := now - ret("retain_"+l.name, l.def)
		res, err := s.db.Exec(`DELETE FROM points_`+l.name+` WHERE t < ?`, cutoff)
		if err != nil {
			return sb.String(), err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			fmt.Fprintf(&sb, "%s:%d ", l.name, n)
		}
	}
	return sb.String(), nil
}

// Downsample 增量聚合 L0→L1（5 分钟桶）、L1→L2（1 小时桶），
// 聚合位置持久化在 meta（ds_l1_pos / ds_l2_pos），重启续跑。
func (s *Store) Downsample() error {
	now := nowUnix()
	steps := []struct {
		dst, src, posKey string
		bucket           int64
		lag              int64 // 只聚合到 now-lag，避免与实时写入竞争
		defPos           int64
	}{
		{"points_l1", "points_l0", "ds_l1_pos", 300, 3600, 0},
		{"points_l2", "points_l1", "ds_l2_pos", 3600, 86400, 0},
	}
	for _, st := range steps {
		pos := int64(0)
		if v, ok := s.GetMeta(st.posKey); ok {
			if p, err := strconv.ParseInt(v, 10, 64); err == nil {
				pos = p
			}
		}
		end := now - st.lag
		if end <= pos {
			continue
		}
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO `+st.dst+`(t,k,v)
			SELECT (t/?)*?, k, AVG(v) FROM `+st.src+`
			WHERE t >= ? AND t < ? GROUP BY k, (t/?)*?`,
			st.bucket, st.bucket, pos, end, st.bucket, st.bucket); err != nil {
			return err
		}
		if err := s.SetMeta(st.posKey, strconv.FormatInt(end, 10)); err != nil {
			return err
		}
	}
	return nil
}

// Stats 返回库文件大小与各层行数（存储估算器用）。
func (s *Store) Stats(path string) map[string]any {
	out := map[string]any{"dbBytes": 0, "rows": map[string]int64{}}
	if fi, err := os.Stat(path); err == nil {
		out["dbBytes"] = fi.Size()
	}
	for _, l := range []string{"l0", "l1", "l2"} {
		var n int64
		s.db.QueryRow(`SELECT COUNT(*) FROM points_` + l).Scan(&n)
		out["rows"].(map[string]int64)[l] = n
	}
	return out
}
