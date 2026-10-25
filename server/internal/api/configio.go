package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"guanlan/server/internal/alert"
)

// configExport GET：导出全部配置为 JSON（供备份/迁移）。
func (s *Server) configExport(w http.ResponseWriter, r *http.Request) {
	var rules []alert.Rule
	if s.opt.Alert != nil {
		rules = s.opt.Alert.Rules()
	}
	rd := s.retainDays()
	writeJSON(w, map[string]any{
		"app":          "guanlan",
		"appVersion":   s.opt.Version,
		"intervalMs":   s.col.Interval().Milliseconds(),
		"dbIntervalMs": s.dbIntervalMs(),
		"retainDays":   rd,
		"rules":        rules,
	})
}

// configImport POST：导入配置 JSON（字段可缺省，只应用存在的部分）。
func (s *Server) configImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Requested-With") != "guanlan" {
		http.Error(w, `{"error":"csrf"}`, http.StatusForbidden)
		return
	}
	var req struct {
		App          string `json:"app"`
		IntervalMs   *int64 `json:"intervalMs"`
		DbIntervalMs *int64 `json:"dbIntervalMs"`
		RetainDays   *struct {
			L0 int `json:"l0"`
			L1 int `json:"l1"`
			L2 int `json:"l2"`
		} `json:"retainDays"`
		Rules *[]alert.Rule `json:"rules"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_json"}`, http.StatusBadRequest)
		return
	}
	if req.App != "" && req.App != "guanlan" {
		http.Error(w, `{"error":"not_guanlan_backup"}`, http.StatusBadRequest)
		return
	}
	applied := []string{}

	if req.IntervalMs != nil && *req.IntervalMs >= 100 && *req.IntervalMs <= 15000 &&
		s.opt.SetConfig != nil {
		if s.opt.SetConfig(*req.IntervalMs, 0) == nil {
			applied = append(applied, "intervalMs")
		}
	}
	if req.DbIntervalMs != nil {
		for _, c := range []int64{5000, 10000, 15000, 30000, 60000} {
			if *req.DbIntervalMs == c && s.opt.SetConfig != nil {
				if s.opt.SetConfig(0, c) == nil {
					applied = append(applied, "dbIntervalMs")
				}
				break
			}
		}
	}
	if req.RetainDays != nil {
		rd := *req.RetainDays
		ok := true
		for _, v := range []int{rd.L0, rd.L1, rd.L2} {
			if v < 1 || v > 3650 {
				ok = false
				break
			}
		}
		if ok {
			if s.st.SetMeta("retain_l0", strconv.Itoa(rd.L0)) == nil &&
				s.st.SetMeta("retain_l1", strconv.Itoa(rd.L1)) == nil &&
				s.st.SetMeta("retain_l2", strconv.Itoa(rd.L2)) == nil {
				applied = append(applied, "retainDays")
			}
		}
	}
	if req.Rules != nil && s.opt.Alert != nil && len(*req.Rules) > 0 && len(*req.Rules) <= 32 {
		valid := true
		for _, rule := range *req.Rules {
			if rule.Op != ">" && rule.Op != "<" || rule.ForMin < 0 || rule.ForMin > 720 {
				valid = false
				break
			}
			if rule.Level != "warn" && rule.Level != "crit" {
				rule.Level = "warn"
			}
		}
		if valid {
			s.opt.Alert.SaveRules(*req.Rules)
			applied = append(applied, "rules")
		}
	}
	writeJSON(w, map[string]any{"applied": applied})
}

// dataClear POST {"confirm":"CLEAR"}：清空全部历史时序与告警记录（保留配置）。
// 要求管理员 + 防跨站头 + 明文确认词，杜绝误删。
func (s *Server) dataClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Requested-With") != "guanlan" {
		http.Error(w, `{"error":"csrf"}`, http.StatusForbidden)
		return
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 128)).Decode(&req); err != nil || req.Confirm != "CLEAR" {
		http.Error(w, `{"error":"confirm_required"}`, http.StatusBadRequest)
		return
	}
	db := s.st.DB()
	for _, t := range []string{"points_l0", "points_l1", "points_l2", "alerts"} {
		if _, err := db.Exec(`DELETE FROM ` + t); err != nil {
			http.Error(w, `{"error":"clear_failed"}`, http.StatusInternalServerError)
			return
		}
	}
	// 降采样游标归零，从新数据重新聚合
	s.st.SetMeta("ds_l1_pos", "0")
	s.st.SetMeta("ds_l2_pos", "0")
	db.Exec(`VACUUM`)
	writeJSON(w, map[string]any{"cleared": true})
}
