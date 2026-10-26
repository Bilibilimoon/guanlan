package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"
)

// TCP 直连（局域网 :9780）的 HTTP Basic 认证：浏览器打开页面时弹原生登录框。
// 凭据存库 meta：auth_user / auth_salt / auth_hash；未设置 auth_user 时视为关闭，
// 保持原来的只读开放行为（fnOS 统一网关通道不受影响，继续用 X-Trim-Isadmin）。
//
// 密码用加盐 SHA-256 迭代 5 万次（不是 bcrypt，但内网监控够用且开销可控）；
// 校验通过后在内存里缓存 Authorization 头 15 分钟，避免 2 秒一次的轮询反复计算。

const (
	authIterations = 50000
	authCacheTTL   = 15 * time.Minute
)

// hashPassword 生成加盐迭代摘要（纯 SHA-256，不引入额外依赖）。
func hashPassword(pass, salt string) string {
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write([]byte(pass))
	sum := h.Sum(nil)
	for i := 0; i < authIterations; i++ {
		h.Reset()
		h.Write(sum)
		h.Write([]byte(salt))
		sum = h.Sum(nil)
	}
	return hex.EncodeToString(sum)
}

func newSalt() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败极罕见；退化到时间戳也比崩溃好
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(b)
}

// authConfigured 读取当前凭据；未配置返回 ok=false（认证关闭）。
func (s *Server) authConfigured() (user, salt, hash string, ok bool) {
	u, okU := s.st.GetMeta("auth_user")
	salt, okS := s.st.GetMeta("auth_salt")
	hash, okH := s.st.GetMeta("auth_hash")
	if !okU || !okS || !okH || u == "" || salt == "" || hash == "" {
		return "", "", "", false
	}
	return u, salt, hash, true
}

// authOK 校验 Authorization 头；成功结果按头的摘要缓存。
func (s *Server) authOK(r *http.Request, user, salt, hash string) bool {
	raw := r.Header.Get("Authorization")
	if raw == "" {
		return false
	}
	key := sha256.Sum256([]byte(raw))
	now := time.Now()
	s.authMu.Lock()
	if exp, hit := s.authCache[key]; hit && now.Before(exp) {
		s.authMu.Unlock()
		return true
	}
	s.authMu.Unlock()

	u, p, ok := r.BasicAuth()
	if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 {
		return false
	}
	got := hashPassword(p, salt)
	if subtle.ConstantTimeCompare([]byte(got), []byte(hash)) != 1 {
		return false
	}
	s.authMu.Lock()
	if len(s.authCache) > 1000 {
		s.authCache = map[[32]byte]time.Time{}
	}
	s.authCache[key] = now.Add(authCacheTTL)
	s.authMu.Unlock()
	return true
}

// authTCP 只拦截 TCP 直连通道（context 标记由 MarkTCP 设置）；
// /api/healthz 免认证（健康检查与网关联通探测依赖它）。
func (s *Server) authTCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, _ := r.Context().Value(tcpROKey).(bool); !v {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		user, salt, hash, ok := s.authConfigured()
		if !ok || s.authOK(r, user, salt, hash) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="Guanlan", charset="UTF-8"`)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

// setAuth 保存/清除凭据（由 /api/config 的管理员通道调用）。
func (s *Server) setAuth(user, pass string) error {
	if user == "" || pass == "" {
		return errBadAuth
	}
	salt := newSalt()
	hash := hashPassword(pass, salt)
	if err := s.st.SetMeta("auth_user", user); err != nil {
		return err
	}
	if err := s.st.SetMeta("auth_salt", salt); err != nil {
		return err
	}
	return s.st.SetMeta("auth_hash", hash)
}

func (s *Server) clearAuth() error {
	for _, k := range []string{"auth_user", "auth_salt", "auth_hash"} {
		if err := s.st.SetMeta(k, ""); err != nil {
			return err
		}
	}
	return nil
}

var errBadAuth = &authError{"auth_credentials_required"}

type authError struct{ msg string }

func (e *authError) Error() string { return e.msg }

// securityHeaders 基础安全响应头（所有响应都带）。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}
