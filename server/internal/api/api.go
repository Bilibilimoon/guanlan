// Package api 提供 HTTP 接口与内嵌前端静态资源。
//
// 鉴权模型：fnOS 统一网关注入 X-Trim-Isadmin 头；非管理员访问 /api/*
// 一律 403。/healthz 与静态页面本身放行（页面加载后由前端呈现提示）。
// dev 模式（本机直跑）跳过校验，fpk 生产环境必须 dev=false。
package api

import (
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

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
	// Disk 磁盘空间守卫（可空：不做阈值保护）
	Disk *store.DiskGuard
}

type Server struct {
	col *collect.Collector
	st  *store.Store
	opt Options
	pub *publicIPProber

	authMu    sync.Mutex
	authCache map[[32]byte]time.Time
}

func New(col *collect.Collector, st *store.Store, opt Options) http.Handler {
	s := &Server{col: col, st: st, opt: opt, pub: newPublicIPProber(), authCache: map[[32]byte]time.Time{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/healthz", s.healthz)
	mux.HandleFunc("/api/realtime", s.gateRO(s.realtime))
	mux.HandleFunc("/api/ring", s.gateRO(s.ring))
	mux.HandleFunc("/api/series", s.gateRO(s.series))
	mux.HandleFunc("/api/meta", s.gateRO(s.meta))
	mux.HandleFunc("/api/config", s.gate(s.config))
	mux.HandleFunc("/api/config/export", s.gate(s.configExport))
	mux.HandleFunc("/api/config/import", s.gate(s.configImport))
	mux.HandleFunc("/api/data/clear", s.gate(s.dataClear))
	mux.HandleFunc("/api/history/export", s.gate(s.historyExport))
	mux.HandleFunc("/api/history/import", s.gate(s.historyImport))
	mux.HandleFunc("/api/processes", s.gateRO(s.processes))
	mux.HandleFunc("/api/alerts", s.gateRO(s.alerts))
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

	return securityHeaders(s.authTCP(s.gzip(mux)))
}

// gate 管理员校验中间件（写接口）：生产模式仅放行网关注入 X-Trim-Isadmin: true。
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

// gateRO 只读接口：管理员可用；TCP 直连（经 MarkTCP 标记）仅放行 GET——
// 局域网只读监控可用，写/管理操作永远拿不到。
func (s *Server) gateRO(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opt.Dev || strings.EqualFold(r.Header.Get("X-Trim-Isadmin"), "true") {
			h(w, r)
			return
		}
		if v, _ := r.Context().Value(tcpROKey).(bool); v && r.Method == http.MethodGet {
			h(w, r)
			return
		}
		http.Error(w, `{"error":"admin_only"}`, http.StatusForbidden)
	}
}

type tcpCtxKey int

const tcpROKey tcpCtxKey = 1

// MarkTCP 包装 TCP listener 的 handler：进入只读通道（context 只在本进程
// 设置，无法从外部伪造），并**剥掉全部 X-Trim-* 头**——网关身份头只在
// Unix socket 链路上可信，TCP 直连者伪造同名头也必须无效。
func MarkTCP(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k := range r.Header {
			if strings.HasPrefix(k, "X-Trim-") {
				r.Header.Del(k)
			}
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tcpROKey, true)))
	})
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
		authUser, _, _, authOn := s.authConfigured()
		out := map[string]any{
			"intervalMs":    s.col.Interval().Milliseconds(),
			"dbIntervalMs":  s.dbIntervalMs(),
			"minIntervalMs": 100,
			"maxIntervalMs": 15000,
			"dbChoicesMs":   []int64{5000, 10000, 15000, 30000, 60000},
			"retainDays":    rd,
			"authEnabled":   authOn,
			"authUser":      authUser,
		}
		if s.opt.Disk != nil {
			ds := s.opt.Disk.Status()
			out["diskMinGb"] = ds["minGb"]
			out["diskFreeGb"] = ds["freeGb"]
			out["diskPaused"] = ds["paused"]
		}
		writeJSON(w, out)
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
		AuthEnabled *bool    `json:"authEnabled"`
		AuthUser    *string  `json:"authUser"`
		AuthPass    *string  `json:"authPass"`
		DiskMinGb   *float64 `json:"diskMinGb"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2048)).Decode(&req); err != nil {
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
	if req.AuthEnabled != nil {
		if *req.AuthEnabled {
			user := "admin"
			if req.AuthUser != nil && strings.TrimSpace(*req.AuthUser) != "" {
				user = strings.TrimSpace(*req.AuthUser)
			}
			pass := ""
			if req.AuthPass != nil {
				pass = *req.AuthPass
			}
			if len(user) > 32 || len(pass) < 4 || len(pass) > 128 {
				http.Error(w, `{"error":"auth_credentials_invalid"}`, http.StatusBadRequest)
				return
			}
			if err := s.setAuth(user, pass); err != nil {
				http.Error(w, `{"error":"save_failed"}`, http.StatusInternalServerError)
				return
			}
		} else if err := s.clearAuth(); err != nil {
			http.Error(w, `{"error":"save_failed"}`, http.StatusInternalServerError)
			return
		}
	}
	if req.DiskMinGb != nil {
		v := *req.DiskMinGb
		if v < 0 || v > 100000 {
			http.Error(w, `{"error":"disk_min_invalid"}`, http.StatusBadRequest)
			return
		}
		if err := s.st.SetMeta("disk_min_gb", strconv.FormatFloat(v, 'f', 1, 64)); err != nil {
			http.Error(w, `{"error":"save_failed"}`, http.StatusInternalServerError)
			return
		}
		if s.opt.Disk != nil {
			s.opt.Disk.Invalidate()
		}
	}
	authUser, _, _, authOn := s.authConfigured()
	out := map[string]any{
		"intervalMs":   s.col.Interval().Milliseconds(),
		"dbIntervalMs": s.dbIntervalMs(),
		"retainDays":   s.retainDays(),
		"authEnabled":  authOn,
		"authUser":     authUser,
	}
	if s.opt.Disk != nil {
		ds := s.opt.Disk.Status()
		out["diskMinGb"] = ds["minGb"]
		out["diskFreeGb"] = ds["freeGb"]
		out["diskPaused"] = ds["paused"]
	}
	writeJSON(w, out)
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
	out := map[string]any{
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
	}
	if s.opt.Disk != nil {
		out["disk"] = s.opt.Disk.Status()
	}
	writeJSON(w, out)
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
