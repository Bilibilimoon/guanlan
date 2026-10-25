package api

import (
	"encoding/json"
	"io"
	"net/http"

	"guanlan/server/internal/alert"
)

// alerts GET：未读数 + 活跃告警 + 近期历史 + 当前规则
func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	if s.opt.Alert == nil {
		http.Error(w, `{"error":"alerts_disabled"}`, http.StatusNotImplemented)
		return
	}
	unread, active, recent := s.opt.Alert.Snapshot()
	writeJSON(w, map[string]any{
		"unread": unread,
		"active": active,
		"recent": recent,
		"rules":  s.opt.Alert.Rules(),
	})
}

// alertRules PUT：整体替换规则表
func (s *Server) alertRules(w http.ResponseWriter, r *http.Request) {
	if s.opt.Alert == nil {
		http.Error(w, `{"error":"alerts_disabled"}`, http.StatusNotImplemented)
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Requested-With") != "guanlan" {
		http.Error(w, `{"error":"csrf"}`, http.StatusForbidden)
		return
	}
	var req struct{ Rules []alert.Rule }
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil || len(req.Rules) == 0 || len(req.Rules) > 32 {
		http.Error(w, `{"error":"bad_json"}`, http.StatusBadRequest)
		return
	}
	for _, rule := range req.Rules {
		if rule.Op != ">" && rule.Op != "<" {
			http.Error(w, `{"error":"bad_op"}`, http.StatusBadRequest)
			return
		}
		if rule.ForMin < 0 || rule.ForMin > 720 {
			http.Error(w, `{"error":"bad_for"}`, http.StatusBadRequest)
			return
		}
		if rule.Level != "warn" && rule.Level != "crit" {
			rule.Level = "warn"
		}
	}
	s.opt.Alert.SaveRules(req.Rules)
	writeJSON(w, map[string]any{"saved": len(req.Rules)})
}

// alertAck POST {id}
func (s *Server) alertAck(w http.ResponseWriter, r *http.Request) {
	if s.opt.Alert == nil {
		http.Error(w, `{"error":"alerts_disabled"}`, http.StatusNotImplemented)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Requested-With") != "guanlan" {
		http.Error(w, `{"error":"csrf"}`, http.StatusForbidden)
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 128)).Decode(&req); err != nil || req.ID <= 0 {
		http.Error(w, `{"error":"bad_json"}`, http.StatusBadRequest)
		return
	}
	if err := s.opt.Alert.Ack(req.ID); err != nil {
		http.Error(w, `{"error":"ack_failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
