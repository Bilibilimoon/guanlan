package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"guanlan/server/internal/audit"
)

// recordTCP 记录经 TCP 只读通道到达的请求。socket 通道（走 fnOS 网关）的访问
// 全部在 nginx access.log 里，这里只补日志覆盖不到的那部分，避免重复计数。
func (s *Server) recordTCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opt.Access == nil {
			next.ServeHTTP(w, r)
			return
		}
		if v, _ := r.Context().Value(tcpROKey).(bool); !v {
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		s.opt.Access.Record(clientIP(r), r.URL.Path, r.Method, r.UserAgent(), status, time.Now())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap 让 http.ResponseController 能拿到底层 writer（Flush/Hijack 等）。
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type visitorTopic struct {
	Name string `json:"name"`
	N    int64  `json:"n"`
}

type visitorItem struct {
	IP     string         `json:"ip"`
	Class  string         `json:"class"`
	N      int64          `json:"n"`
	Page   int64          `json:"page"`
	S2     int64          `json:"s2"`
	S3     int64          `json:"s3"`
	S4     int64          `json:"s4"`
	S5     int64          `json:"s5"`
	First  int64          `json:"first"`
	Last   int64          `json:"last"`
	UA     string         `json:"ua"`
	Topics []visitorTopic `json:"topics"`
}

// visitors GET /api/visitors?hours=24&ip=&fail=1&limit=
// 返回按来源 IP 汇总的访问记录（只读；TCP 通道 GET 也可用）。
func (s *Server) visitors(w http.ResponseWriter, r *http.Request) {
	if s.opt.Access == nil {
		http.Error(w, `{"error":"disabled"}`, http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	hours := 24
	if n, err := strconv.Atoi(q.Get("hours")); err == nil && n >= 1 && n <= 720 {
		hours = n
	}
	from := audit.HourStart(time.Now().Add(-time.Duration(hours) * time.Hour))
	rows, err := s.st.QueryAccess(from)
	if err != nil {
		http.Error(w, `{"error":"query_failed"}`, http.StatusInternalServerError)
		return
	}
	filter := strings.TrimSpace(q.Get("ip"))
	onlyFail := q.Get("fail") == "1"
	items := make([]visitorItem, 0, len(rows))
	var totalReq, totalFail int64
	for _, row := range rows {
		if filter != "" && !strings.Contains(row.IP, filter) {
			continue
		}
		fails := row.S4 + row.S5
		if onlyFail && fails == 0 {
			continue
		}
		it := visitorItem{
			IP: row.IP, Class: audit.Classify(row.IP),
			N: row.N, Page: row.Page, S2: row.S2, S3: row.S3, S4: row.S4, S5: row.S5,
			First: row.First, Last: row.Last, UA: row.UA,
		}
		for name, n := range row.Topics {
			it.Topics = append(it.Topics, visitorTopic{Name: name, N: n})
		}
		sort.Slice(it.Topics, func(i, j int) bool { return it.Topics[i].N > it.Topics[j].N })
		if len(it.Topics) > 3 {
			it.Topics = it.Topics[:3]
		}
		totalReq += row.N
		totalFail += fails
		items = append(items, it)
	}
	cut := len(items)
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v < cut {
		cut = v
	}
	writeJSON(w, map[string]any{
		"hours":     hours,
		"from":      from,
		"to":        time.Now().Unix(),
		"source":    s.opt.Access.Status(),
		"count":     len(items),
		"totalReq":  totalReq,
		"totalFail": totalFail,
		"items":     items[:cut],
	})
}
