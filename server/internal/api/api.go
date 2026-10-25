// Package api 提供 HTTP 接口与内嵌前端静态资源。
//
// 鉴权模型：fnOS 统一网关注入 X-Trim-Isadmin 头；非管理员访问 /api/*
// 一律 403。/healthz 与静态页面本身放行（页面加载后由前端呈现提示）。
// dev 模式（本机直跑）跳过校验，fpk 生产环境必须 dev=false。
package api

import (
	"compress/gzip"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"guanlan/server/internal/alert"
	"guanlan/server/internal/collect"
	"guanlan/server/internal/store"
)

//go:embed all:dist
var distFS embed.FS

type Options struct {
	Dev     bool
	Version string
	// SetConfig 运行时修改采集/落盘间隔（毫秒，0 表示不变），由 main 注入
	SetConfig func(intervalMs, dbMs int64) error
	// Alert 告警评估器（可空：禁用告警接口）
	Alert *alert.Evaluator
	// DbPath SQLite 路径（meta 里报库大小用）
	DbPath string
}

type Server struct {
	col *collect.Collector
	st  *store.Store
	opt Options
	pub *publicIPProber
}

func New(col *collect.Collector, st *store.Store, opt Options) http.Handler {
	s := &Server{col: col, st: st, opt: opt, pub: newPublicIPProber()}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/healthz", s.healthz)
	mux.HandleFunc("/api/realtime", s.gate(s.realtime))
	mux.HandleFunc("/api/ring", s.gate(s.ring))
	mux.HandleFunc("/api/series", s.gate(s.series))
	mux.HandleFunc("/api/meta", s.gate(s.meta))
	mux.HandleFunc("/api/config", s.gate(s.config))
	mux.HandleFunc("/api/processes", s.gate(s.processes))
	mux.HandleFunc("/api/alerts", s.gate(s.alerts))
	mux.HandleFunc("/api/alerts/rules", s.gate(s.alertRules))
	mux.HandleFunc("/api/alerts/ack", s.gate(s.alertAck))

	// 静态资源 + SPA fallback
	dist, _ := fs.Sub(distFS, "dist")
	fileServer := http.FileServer(http.FS(dist))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := dist.Open(p); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA：无对应文件回落 index.html
		r2 := new(http.Request)
		*r2 = *r
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})

	return s.gzip(mux)
}

// gate 管理员校验中间件：生产模式仅放行 X-Trim-Isadmin=1。
func (s *Server) gate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 网关注入的管理员标识值为字符串 true（非 1）
		if !s.opt.Dev && !strings.EqualFold(r.Header.Get("X-Trim-Isadmin"), "true") {
			http.Error(w, `{"error":"admin_only"}`, http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// gzip 压缩 JSON 响应（环形缓冲/序列数据体积较大）。
func (s *Server) gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") ||
			r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		next.ServeHTTP(gzWriter{w, gz}, r)
	})
}

type gzWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (w gzWriter) Write(b []byte) (int, error) { return w.gz.Write(b) }
func (w gzWriter) WriteHeader(status int)      { w.ResponseWriter.WriteHeader(status) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// config GET 返回当前采集配置；POST 修改（毫秒），范围外拒绝。
func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		rd := s.retainDays()
		writeJSON(w, map[string]any{
			"intervalMs":    s.col.Interval().Milliseconds(),
			"dbIntervalMs":  s.dbIntervalMs(),
			"minIntervalMs": 100,
			"maxIntervalMs": 15000,
			"dbChoicesMs":   []int64{5000, 10000, 15000, 30000, 60000},
			"retainDays":    rd,
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
		return
	}
	// 防跨站：自定义头必须存在
	if r.Header.Get("X-Requested-With") != "guanlan" {
		http.Error(w, `{"error":"csrf"}`, http.StatusForbidden)
		return
	}
	var req struct {
		IntervalMs   *int64 `json:"intervalMs"`
		DbIntervalMs *int64 `json:"dbIntervalMs"`
		RetainDays   *struct {
			L0 int `json:"l0"`
			L1 int `json:"l1"`
			L2 int `json:"l2"`
		} `json:"retainDays"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 512)).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_json"}`, http.StatusBadRequest)
		return
	}
	if req.IntervalMs != nil && (*req.IntervalMs < 100 || *req.IntervalMs > 15000) {
		http.Error(w, `{"error":"interval_out_of_range"}`, http.StatusBadRequest)
		return
	}
	if req.DbIntervalMs != nil {
		ok := false
		for _, c := range []int64{5000, 10000, 15000, 30000, 60000} {
			if *req.DbIntervalMs == c {
				ok = true
				break
			}
		}
		if !ok {
			http.Error(w, `{"error":"db_interval_invalid"}`, http.StatusBadRequest)
			return
		}
	}
	if req.RetainDays != nil {
		rd := *req.RetainDays
		for _, v := range []int{rd.L0, rd.L1, rd.L2} {
			if v < 1 || v > 3650 || rd.L0 < 1 {
				http.Error(w, `{"error":"retain_invalid"}`, http.StatusBadRequest)
				return
			}
		}
		if err := s.st.SetMeta("retain_l0", strconv.Itoa(rd.L0)); err != nil ||
			s.st.SetMeta("retain_l1", strconv.Itoa(rd.L1)) != nil ||
			s.st.SetMeta("retain_l2", strconv.Itoa(rd.L2)) != nil {
			http.Error(w, `{"error":"save_failed"}`, http.StatusInternalServerError)
			return
		}
	}
	if req.IntervalMs != nil || req.DbIntervalMs != nil {
		if s.opt.SetConfig == nil {
			http.Error(w, `{"error":"readonly"}`, http.StatusInternalServerError)
			return
		}
		if err := s.opt.SetConfig(deref(req.IntervalMs), deref(req.DbIntervalMs)); err != nil {
			http.Error(w, `{"error":"save_failed"}`, http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]any{
		"intervalMs":   s.col.Interval().Milliseconds(),
		"dbIntervalMs": s.dbIntervalMs(),
		"retainDays":   s.retainDays(),
	})
}

func (s *Server) retainDays() map[string]int {
	def := map[string]int{"l0": 30, "l1": 180, "l2": 365}
	for _, k := range []string{"l0", "l1", "l2"} {
		if v, ok := s.st.GetMeta("retain_" + k); ok {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 3650 {
				def[k] = n
			}
		}
	}
	return def
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func (s *Server) dbIntervalMs() int64 {
	if v, ok := s.st.GetMeta("db_interval_ms"); ok {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			return ms
		}
	}
	return 15000
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok")
}

func (s *Server) realtime(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.col.Latest())
}

func (s *Server) ring(w http.ResponseWriter, r *http.Request) {
	sec, _ := strconv.ParseFloat(r.URL.Query().Get("sec"), 64)
	if sec <= 0 || sec > 900 {
		sec = 900
	}
	writeJSON(w, s.col.Snapshot(sec))
}

func (s *Server) series(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	names := strings.Split(q.Get("metrics"), ",")
	start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
	end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
	level := q.Get("level")
	if len(names) == 0 || names[0] == "" || start <= 0 || end <= start {
		http.Error(w, `{"error":"bad_params"}`, http.StatusBadRequest)
		return
	}
	// 每请求指标数上限，防止一次拉爆
	if len(names) > 24 {
		names = names[:24]
	}
	out, err := s.st.Query(level, names, start, end)
	if err != nil {
		http.Error(w, `{"error":"query_failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"start": start, "end": end, "level": level, "series": out,
	})
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	latest := s.col.Latest()
	pubV4, pubV6, pubChecked := s.pub.snapshot()
	writeJSON(w, map[string]any{
		"version":         s.opt.Version,
		"ifaces":          s.col.Ifaces(),
		"disks":           keysDisk(latest),
		"temps":           keysTemp(latest),
		"metrics":         s.st.KnownMetrics(),
		"lanIPs":          lanIPs(),
		"publicIPv4":      pubV4,
		"publicIPv6":      pubV6,
		"publicCheckedAt": pubChecked.Unix(),
		"storage":         s.st.Stats(s.opt.DbPath),
	})
}

func keysDisk(sm collect.Sample) []string {
	out := make([]string, 0, len(sm.Disks))
	for k := range sm.Disks {
		out = append(out, k)
	}
	return out
}

func keysTemp(sm collect.Sample) []string {
	out := make([]string, 0, len(sm.Temps))
	for k := range sm.Temps {
		out = append(out, k)
	}
	return out
}
