package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log"
	"net"
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
	// 同一来源连续失败达到该次数后进入冷却，冷却期内直接拒绝、不再计算哈希
	authMaxFails = 5
	// 失败表内存上限：超过就清理过期项，仍超限则整体清空（丢限速状态不影响正确性）
	authFailLimit = 4096
)

// 冷却时长做成变量，便于单测缩短；生产固定 60 秒。
var authCooldown = 60 * time.Second

type authFail struct {
	n      int
	until  time.Time
	logged bool // 阈值日志只打一次，避免刷日志
}

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
// 失败限速按来源 IP（RemoteAddr，绝不信任 X-Forwarded-For）：连续失败 5 次后
// 冷却 60 秒，冷却期内直接 401、不跑哈希，避免错误请求打满 CPU。
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
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r)
		if s.authThrottled(ip) {
			s.rejectAuth(w)
			return
		}
		if s.authOK(r, user, salt, hash) {
			s.clearAuthFail(ip)
			next.ServeHTTP(w, r)
			return
		}
		s.noteAuthFail(ip)
		s.rejectAuth(w)
	})
}

func (s *Server) rejectAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Guanlan", charset="UTF-8"`)
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

// clientIP 取 TCP 对端地址；不读任何转发头（可伪造）。
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// authThrottled 该来源是否仍在冷却期。
func (s *Server) authThrottled(ip string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	f, ok := s.authFails[ip]
	return ok && time.Now().Before(f.until)
}

// noteAuthFail 记一次失败：首次失败与"进入冷却"各打一条脱敏日志（只记 IP）。
func (s *Server) noteAuthFail(ip string) {
	now := time.Now()
	s.failMu.Lock()
	if len(s.authFails) > authFailLimit {
		for k, v := range s.authFails {
			if !now.Before(v.until) && v.n == 1 {
				delete(s.authFails, k)
			}
		}
		if len(s.authFails) > authFailLimit {
			s.authFails = map[string]*authFail{}
		}
	}
	f := s.authFails[ip]
	if f == nil {
		f = &authFail{}
		s.authFails[ip] = f
	}
	f.n++
	var msg string
	switch {
	case f.n == 1:
		msg = "TCP 认证失败：来源 " + ip
	case f.n >= authMaxFails:
		// 冷却结束后再错一次立即重新锁定（限速真正生效），日志只在第一次锁定时打
		f.until = now.Add(authCooldown)
		if !f.logged {
			f.logged = true
			msg = "TCP 认证失败次数过多，来源 " + ip + " 暂停认证 " + authCooldown.String()
		}
	}
	s.failMu.Unlock()
	if msg != "" {
		log.Print(msg)
	}
}

func (s *Server) clearAuthFail(ip string) {
	s.failMu.Lock()
	delete(s.authFails, ip)
	s.failMu.Unlock()
}

// resetAuthCache 清空"校验通过"缓存（改密码/关闭认证后旧凭据必须立即失效）。
func (s *Server) resetAuthCache() {
	s.authMu.Lock()
	s.authCache = map[[32]byte]time.Time{}
	s.authMu.Unlock()
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
	if err := s.st.SetMeta("auth_hash", hash); err != nil {
		return err
	}
	// 旧凭据的"校验通过"缓存必须立即失效，否则改密码后旧密码还能用 15 分钟
	s.resetAuthCache()
	return nil
}

func (s *Server) clearAuth() error {
	for _, k := range []string{"auth_user", "auth_salt", "auth_hash"} {
		if err := s.st.SetMeta(k, ""); err != nil {
			return err
		}
	}
	s.resetAuthCache()
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
