package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"guanlan/server/internal/collect"
	"guanlan/server/internal/store"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	col := collect.NewCollector(16)
	col.Start(0) // 不采集（间隔 0 会被夹到最小值；测试只关心鉴权）
	return New(col, s, Options{
		Dev:       false,
		Version:   "test",
		DbPath:    t.TempDir() + "/t.db",
		SetConfig: func(intervalMs, dbMs int64) error { return nil },
	})
}

func get(h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func post(h http.Handler, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestGateAdminOnly(t *testing.T) {
	h := newTestServer(t)
	// 无头：403
	if w := get(h, "/api/realtime", nil); w.Code != 403 {
		t.Fatalf("无头 = %d, want 403", w.Code)
	}
	// 伪造数值 1：仍 403（网关真实值是 true）
	if w := get(h, "/api/realtime", map[string]string{"X-Trim-Isadmin": "1"}); w.Code != 403 {
		t.Fatalf("Isadmin=1 = %d, want 403", w.Code)
	}
	// 管理员 true：放行
	if w := get(h, "/api/realtime", map[string]string{"X-Trim-Isadmin": "true"}); w.Code != 200 {
		t.Fatalf("Isadmin=true = %d, want 200", w.Code)
	}
	// healthz 永远放行
	if w := get(h, "/api/healthz", nil); w.Code != 200 {
		t.Fatalf("healthz = %d, want 200", w.Code)
	}
}

func TestWriteRequiresCSRFHeader(t *testing.T) {
	h := newTestServer(t)
	admin := map[string]string{"X-Trim-Isadmin": "true", "Content-Type": "application/json"}
	// 缺 X-Requested-With：403
	if w := post(h, "/api/config", `{"intervalMs":2000}`, admin); w.Code != 403 {
		t.Fatalf("无 CSRF 头 = %d, want 403", w.Code)
	}
	// 带头但越界：400
	admin["X-Requested-With"] = "guanlan"
	if w := post(h, "/api/config", `{"intervalMs":99999}`, admin); w.Code != 400 {
		t.Fatalf("越界 = %d, want 400", w.Code)
	}
	// 正常：200
	if w := post(h, "/api/config", `{"intervalMs":2000}`, admin); w.Code != 200 {
		t.Fatalf("合法 = %d, want 200", w.Code)
	}
}

func TestTCPReadOnlyChannel(t *testing.T) {
	h := newTestServer(t)
	tcp := MarkTCP(h)
	// TCP GET 只读：放行
	if w := get(tcp, "/api/realtime", nil); w.Code != 200 {
		t.Fatalf("TCP GET = %d, want 200", w.Code)
	}
	// TCP 写：403（即使伪造管理员头也无网关可信链路）
	if w := post(tcp, "/api/config", `{"intervalMs":2000}`,
		map[string]string{"X-Trim-Isadmin": "true", "X-Requested-With": "guanlan", "Content-Type": "application/json"}); w.Code != 403 {
		t.Fatalf("TCP 伪造管理员写 = %d, want 403", w.Code)
	}
	// ctx 标记不可被请求伪造：普通 handler 上同样请求仍 403（对照）
	if w := post(h, "/api/config", `{"intervalMs":2000}`,
		map[string]string{"X-Requested-With": "guanlan", "Content-Type": "application/json"}); w.Code != 403 {
		t.Fatalf("非 TCP 无管理员 = %d, want 403", w.Code)
	}
	_ = context.Background()
}

func TestDataClearRequiresConfirm(t *testing.T) {
	h := newTestServer(t)
	admin := map[string]string{"X-Trim-Isadmin": "true", "X-Requested-With": "guanlan", "Content-Type": "application/json"}
	// 错误确认词：400
	if w := post(h, "/api/data/clear", `{"confirm":"yes"}`, admin); w.Code != 400 {
		t.Fatalf("错误确认词 = %d, want 400", w.Code)
	}
	// 正确确认词：200
	if w := post(h, "/api/data/clear", `{"confirm":"CLEAR"}`, admin); w.Code != 200 {
		t.Fatalf("CLEAR = %d, want 200", w.Code)
	}
}

func TestSeriesLevelWhitelist(t *testing.T) {
	h := newTestServer(t)
	admin := map[string]string{"X-Trim-Isadmin": "true"}
	// 注入尝试：level 白名单回落 l0，不报错不注入
	w := get(h, "/api/series?metrics=cpu&start=1&end=2&level=l2%27%20OR%201=1--", admin)
	if w.Code == 500 {
		t.Fatalf("level 注入导致 500")
	}
	// 空参数：400
	if w := get(h, "/api/series?metrics=&start=0&end=0", admin); w.Code != 400 {
		t.Fatalf("空参数 = %d, want 400", w.Code)
	}
}
