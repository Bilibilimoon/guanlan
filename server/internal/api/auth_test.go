package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// TestAuthPasswordChangeInvalidatesCache 改密码后，旧凭据的"校验通过"缓存必须立即失效。
func TestAuthPasswordChangeInvalidatesCache(t *testing.T) {
	h := newTestServer(t)
	tcp := MarkTCP(h)
	admin := map[string]string{"X-Trim-Isadmin": "true", "X-Requested-With": "guanlan"}

	if rr := post(h, "/api/config", `{"authEnabled":true,"authUser":"admin","authPass":"old-pass"}`, admin); rr.Code != http.StatusOK {
		t.Fatalf("设置密码失败: %d", rr.Code)
	}
	// 这一步会让旧凭据进入 15 分钟缓存
	if rr := get(tcp, "/api/meta", basic("admin", "old-pass")); rr.Code != http.StatusOK {
		t.Fatalf("旧密码登录应 200，得到 %d", rr.Code)
	}
	if rr := post(h, "/api/config", `{"authEnabled":true,"authUser":"admin","authPass":"new-pass"}`, admin); rr.Code != http.StatusOK {
		t.Fatalf("更新密码失败: %d", rr.Code)
	}
	if rr := get(tcp, "/api/meta", basic("admin", "old-pass")); rr.Code != http.StatusUnauthorized {
		t.Fatalf("改密码后旧凭据应立即失效（401），得到 %d", rr.Code)
	}
	if rr := get(tcp, "/api/meta", basic("admin", "new-pass")); rr.Code != http.StatusOK {
		t.Fatalf("新密码应 200，得到 %d", rr.Code)
	}
}

// TestAuthThrottle 连续失败达到阈值后进入冷却：冷却期内即使密码正确也拒绝，
// 冷却结束后恢复；失败计数按来源 IP 记录。
func TestAuthThrottle(t *testing.T) {
	old := authCooldown
	authCooldown = 80 * time.Millisecond
	t.Cleanup(func() { authCooldown = old })

	h := newTestServer(t)
	tcp := MarkTCP(h)
	admin := map[string]string{"X-Trim-Isadmin": "true", "X-Requested-With": "guanlan"}
	if rr := post(h, "/api/config", `{"authEnabled":true,"authUser":"admin","authPass":"correct-pass"}`, admin); rr.Code != http.StatusOK {
		t.Fatalf("设置密码失败: %d", rr.Code)
	}

	for i := 0; i < authMaxFails; i++ {
		if rr := get(tcp, "/api/meta", basic("admin", "wrong")); rr.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误应 401，得到 %d", i+1, rr.Code)
		}
	}
	// 冷却期内不跑哈希，正确密码也直接拒绝——这是限速的关键
	if rr := get(tcp, "/api/meta", basic("admin", "correct-pass")); rr.Code != http.StatusUnauthorized {
		t.Fatalf("冷却期内应拒绝（401），得到 %d", rr.Code)
	}
	time.Sleep(120 * time.Millisecond)
	if rr := get(tcp, "/api/meta", basic("admin", "correct-pass")); rr.Code != http.StatusOK {
		t.Fatalf("冷却结束后应放行（200），得到 %d", rr.Code)
	}
}

// TestAuthThrottleDifferentIPs 不同来源互不影响：一个 IP 被锁不波及另一个。
func TestAuthThrottleDifferentIPs(t *testing.T) {
	h := newTestServer(t)
	tcp := MarkTCP(h)
	admin := map[string]string{"X-Trim-Isadmin": "true", "X-Requested-With": "guanlan"}
	post(h, "/api/config", `{"authEnabled":true,"authUser":"admin","authPass":"correct-pass"}`, admin)

	// 用两个不同来源地址打错
	for i := 0; i < authMaxFails; i++ {
		for _, ip := range []string{"192.0.2.10:1234", "192.0.2.20:1234"} {
			req := httptest.NewRequest("GET", "/api/meta", nil)
			req.RemoteAddr = ip
			req.SetBasicAuth("admin", "wrong")
			w := httptest.NewRecorder()
			tcp.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s 错误密码应 401，得到 %d", ip, w.Code)
			}
		}
	}
	// 两个 IP 都被锁；换第三个 IP 不应受影响（但本测试只证明按 IP 分桶）
	req := httptest.NewRequest("GET", "/api/meta", nil)
	req.RemoteAddr = "192.0.2.30:1234"
	req.SetBasicAuth("admin", "correct-pass")
	w := httptest.NewRecorder()
	tcp.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("未失败过的来源应放行（200），得到 %d", w.Code)
	}
}
