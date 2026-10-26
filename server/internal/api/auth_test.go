package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func basic(u, p string) map[string]string {
	token := base64.StdEncoding.EncodeToString([]byte(u + ":" + p))
	return map[string]string{"Authorization": "Basic " + token}
}

// TestTCPBasicAuth 覆盖：未配置放行 → 配置后 401 → 正确凭据 200 →
// healthz 始终免认证 → 关闭后恢复放行。
func TestTCPBasicAuth(t *testing.T) {
	h := newTestServer(t)
	tcp := MarkTCP(h)

	if rr := get(tcp, "/api/meta", nil); rr.Code != http.StatusOK {
		t.Fatalf("未配置密码时 TCP 应放行，得到 %d", rr.Code)
	}

	admin := map[string]string{"X-Trim-Isadmin": "true", "X-Requested-With": "guanlan"}
	cfg := `{"authEnabled":true,"authUser":"admin","authPass":"s3cret-pass"}`
	if rr := post(h, "/api/config", cfg, admin); rr.Code != http.StatusOK {
		t.Fatalf("设置密码失败: %d %s", rr.Code, rr.Body.String())
	}

	rr := get(tcp, "/api/meta", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("配置后无凭据应 401，得到 %d", rr.Code)
	}
	if rr.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("401 必须带 WWW-Authenticate 才会触发浏览器登录框")
	}

	if rr := get(tcp, "/api/meta", basic("admin", "s3cret-pass")); rr.Code != http.StatusOK {
		t.Fatalf("正确凭据应 200，得到 %d", rr.Code)
	}
	if rr := get(tcp, "/api/meta", basic("admin", "wrong")); rr.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401，得到 %d", rr.Code)
	}
	// 静态页面同样需要认证（浏览器打开页面即弹窗）
	if rr := get(tcp, "/", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("页面本身应 401，得到 %d", rr.Code)
	}

	// healthz 免认证（红线）
	if rr := get(tcp, "/api/healthz", nil); rr.Code != http.StatusOK {
		t.Fatalf("healthz 应免认证，得到 %d", rr.Code)
	}

	// 网关通道（非 TCP）不要求 Basic Auth：仍靠 X-Trim-Isadmin
	if rr := get(h, "/api/meta", admin); rr.Code != http.StatusOK {
		t.Fatalf("网关管理员应 200，得到 %d", rr.Code)
	}

	// 关闭认证后恢复放行
	if rr := post(h, "/api/config", `{"authEnabled":false}`, admin); rr.Code != http.StatusOK {
		t.Fatalf("关闭认证失败: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get(tcp, "/api/meta", nil); rr.Code != http.StatusOK {
		t.Fatalf("关闭后应恢复放行，得到 %d", rr.Code)
	}
}

// TestAuthConfigExposed 密码不能回显；只回显用户名与开关。
func TestAuthConfigExposed(t *testing.T) {
	h := newTestServer(t)
	admin := map[string]string{"X-Trim-Isadmin": "true", "X-Requested-With": "guanlan"}
	post(h, "/api/config", `{"authEnabled":true,"authUser":"bob","authPass":"12345678"}`, admin)
	rr := get(h, "/api/config", admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("读配置失败 %d", rr.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["authEnabled"] != true || m["authUser"] != "bob" {
		t.Fatalf("配置回显不对: %v", m)
	}
	for k := range m {
		if strings.Contains(strings.ToLower(k), "pass") || strings.Contains(strings.ToLower(k), "hash") {
			t.Fatalf("配置里不应出现密码/摘要字段: %s", k)
		}
	}
}

// TestAuthRejectsTooShort 少于 4 位的密码拒绝。
func TestAuthRejectsTooShort(t *testing.T) {
	h := newTestServer(t)
	admin := map[string]string{"X-Trim-Isadmin": "true", "X-Requested-With": "guanlan"}
	rr := post(h, "/api/config", `{"authEnabled":true,"authUser":"a","authPass":"123"}`, admin)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("短密码应 400，得到 %d", rr.Code)
	}
}
