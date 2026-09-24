package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/config"
)

// 管理台登录鉴权的测试。分两组：
//   - 令牌签发/校验的纯逻辑（不经过 HTTP）；
//   - 走 registerAdminRoutes 挂载出的真实路由接线打 httptest，
//     保证「公开子组 + 受保护子组」的中间件顺序也被测到。

const testAdminKey = "test-admin-key-0123456789abcdef"

// newAuthTestServer 构造只带鉴权所需依赖的 Server 与真实路由。
// systemInfo 只读 deps.Config，不需要数据库。
func newAuthTestServer(t *testing.T, adminKey string) (*Server, *gin.Engine) {
	t.Helper()
	s := &Server{
		deps: &Deps{Config: &config.Config{
			Server:   config.ServerConfig{Port: 8888},
			Security: config.SecurityConfig{Secret: "test-relay-secret", AdminKey: adminKey, SessionTTL: time.Hour},
		}},
		startedAt: time.Now(),
		keys:      newKeyCache(),
		lastTouch: map[uint]time.Time{},
	}
	s.initConsoleAuth(s.deps.Config, nil)

	r := gin.New()
	s.registerAdminRoutes(r)
	return s, r
}

func loginRequest(r *gin.Engine, key string, headers map[string]string) *httptest.ResponseRecorder {
	body := `{"key":"` + key + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---- 令牌 ----

func TestSessionTokenRoundtrip(t *testing.T) {
	s, _ := newAuthTestServer(t, testAdminKey)
	token := s.signSessionToken(time.Now().Add(s.sessionTTL))
	if !s.verifySessionToken(token) {
		t.Fatal("刚签发的令牌应当校验通过")
	}
}

func TestSessionTokenExpiry(t *testing.T) {
	s, _ := newAuthTestServer(t, testAdminKey)
	token := s.signSessionToken(time.Now().Add(-time.Minute))
	if s.verifySessionToken(token) {
		t.Fatal("过期令牌应当被拒绝")
	}
}

func TestSessionTokenRejectsTampered(t *testing.T) {
	s, _ := newAuthTestServer(t, testAdminKey)
	token := s.signSessionToken(time.Now().Add(s.sessionTTL))
	// 篡改载荷的一个字符（保持 base64 合法）：签名对不上即拒绝
	payloadIdx := strings.Index(token, ".") + 1
	flipped := byte('A')
	if token[payloadIdx] == 'A' {
		flipped = 'B'
	}
	tampered := token[:payloadIdx] + string(flipped) + token[payloadIdx+1:]
	if s.verifySessionToken(tampered) {
		t.Fatal("篡改后的令牌应当被拒绝")
	}
}

func TestSessionTokenRejectsForeignSignature(t *testing.T) {
	// 同一把管理密钥、不同的 RELAY_SECRET：会话签名密钥不同，令牌互不通用。
	// 这保证泄露其中一个派生量不等于泄露全部实例的会话签发能力。
	s1, _ := newAuthTestServer(t, testAdminKey)
	s2 := &Server{
		deps: &Deps{Config: &config.Config{
			Security: config.SecurityConfig{Secret: "another-secret", AdminKey: testAdminKey, SessionTTL: time.Hour},
		}},
		startedAt: time.Now(),
		keys:      newKeyCache(),
		lastTouch: map[uint]time.Time{},
	}
	s2.initConsoleAuth(s2.deps.Config, nil)
	token := s1.signSessionToken(time.Now().Add(time.Hour))
	if s2.verifySessionToken(token) {
		t.Fatal("别的 RELAY_SECRET 签发的令牌应当被拒绝")
	}
}

// 版本绑定是轮换吊销的根基：换掉管理密钥后，旧会话必须即刻失效。
func TestSessionTokenInvalidAfterKeyRotation(t *testing.T) {
	s, _ := newAuthTestServer(t, testAdminKey)
	token := s.signSessionToken(time.Now().Add(time.Hour))

	rotated := &Server{
		deps: &Deps{Config: &config.Config{
			Security: config.SecurityConfig{Secret: "test-relay-secret", AdminKey: "rotated-" + testAdminKey, SessionTTL: time.Hour},
		}},
		startedAt: time.Now(),
		keys:      newKeyCache(),
		lastTouch: map[uint]time.Time{},
	}
	rotated.initConsoleAuth(rotated.deps.Config, nil)
	if rotated.verifySessionToken(token) {
		t.Fatal("轮换管理密钥后旧令牌应当全部失效")
	}
}

// ---- 中间件与路由 ----

func TestRequireConsoleAuth(t *testing.T) {
	t.Run("未配置密钥时放行（旧部署兼容）", func(t *testing.T) {
		s, r := newAuthTestServer(t, "")
		if s.consoleAuthEnabled() {
			t.Fatal("未配置 AdminKey 时鉴权应处于关闭状态")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/system/info", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("鉴权关闭时应放行，实际 %d", w.Code)
		}
	})

	t.Run("启用后无 Cookie 返回 401", func(t *testing.T) {
		_, r := newAuthTestServer(t, testAdminKey)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/system/info", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("无会话应返回 401，实际 %d", w.Code)
		}
	})

	t.Run("有效会话放行", func(t *testing.T) {
		s, r := newAuthTestServer(t, testAdminKey)
		token := s.signSessionToken(time.Now().Add(s.sessionTTL))
		req := httptest.NewRequest(http.MethodGet, "/api/admin/system/info", nil)
		req.AddCookie(&http.Cookie{Name: consoleCookieName, Value: token})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("有效会话应放行，实际 %d", w.Code)
		}
	})

	t.Run("垃圾与会话过期的 Cookie 一律 401", func(t *testing.T) {
		s, r := newAuthTestServer(t, testAdminKey)
		for name, token := range map[string]string{
			"垃圾值":   "garbage.token.value",
			"过期令牌":   s.signSessionToken(time.Now().Add(-time.Second)),
			"伪造签名":   s.signSessionToken(time.Now().Add(time.Hour)) + "x",
			"空字符串":   "",
		} {
			req := httptest.NewRequest(http.MethodGet, "/api/admin/system/info", nil)
			req.AddCookie(&http.Cookie{Name: consoleCookieName, Value: token})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s 应返回 401，实际 %d", name, w.Code)
			}
		}
	})
}

// ---- 登录接口 ----

func TestConsoleLogin(t *testing.T) {
	t.Run("正确密钥签发会话 Cookie", func(t *testing.T) {
		_, r := newAuthTestServer(t, testAdminKey)
		w := loginRequest(r, testAdminKey, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("正确密钥应登录成功，实际 %d：%s", w.Code, w.Body.String())
		}
		cookies := w.Result().Cookies()
		var cookie *http.Cookie
		for _, c := range cookies {
			if c.Name == consoleCookieName {
				cookie = c
			}
		}
		if cookie == nil {
			t.Fatal("登录成功应下发会话 Cookie")
		}
		if !cookie.HttpOnly {
			t.Error("会话 Cookie 必须 HttpOnly，防脚本读取")
		}
		if cookie.Path != "/" {
			t.Error("会话 Cookie 应作用于全站")
		}
		if cookie.MaxAge <= 0 {
			t.Errorf("会话 Cookie 应带正的 Max-Age，实际 %d", cookie.MaxAge)
		}
		if !strings.Contains(cookie.Raw, "SameSite=Lax") {
			t.Errorf("会话 Cookie 应为 SameSite=Lax，实际 %s", cookie.Raw)
		}
	})

	t.Run("HTTPS 请求带 Secure 标志", func(t *testing.T) {
		_, r := newAuthTestServer(t, testAdminKey)
		w := loginRequest(r, testAdminKey, map[string]string{"X-Forwarded-Proto": "https"})
		if w.Code != http.StatusOK {
			t.Fatalf("登录应成功，实际 %d", w.Code)
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == consoleCookieName && !c.Secure {
				t.Error("HTTPS 下的会话 Cookie 必须带 Secure 标志")
			}
		}
	})

	t.Run("错误密钥 401 且不签发 Cookie", func(t *testing.T) {
		_, r := newAuthTestServer(t, testAdminKey)
		w := loginRequest(r, "wrong-key-wrong-key", nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("错误密钥应返回 401，实际 %d", w.Code)
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == consoleCookieName {
				t.Error("登录失败不应下发会话 Cookie")
			}
		}
	})

	t.Run("鉴权关闭时登录返回 400", func(t *testing.T) {
		_, r := newAuthTestServer(t, "")
		w := loginRequest(r, testAdminKey, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("未启用鉴权时登录应返回 400，实际 %d", w.Code)
		}
	})
}

// 限流是公网部署下唯一的暴力破解闸门：窗口内错满 5 次后，
// 连「正确的密钥」也要等到窗口结束 —— 否则攻击者可以混着试。
func TestConsoleLoginRateLimit(t *testing.T) {
	oldDelay := loginFailureDelay
	loginFailureDelay = 0
	t.Cleanup(func() { loginFailureDelay = oldDelay })

	s, r := newAuthTestServer(t, testAdminKey)
	for i := 0; i < loginMaxFailures; i++ {
		w := loginRequest(r, "wrong-key-"+strings.Repeat("x", i), nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误应返回 401，实际 %d", i+1, w.Code)
		}
	}

	// 第 6 次（无论密钥对错）都进入锁定期
	w := loginRequest(r, testAdminKey, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("错满 %d 次后应返回 429，实际 %d", loginMaxFailures, w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("429 响应应带 Retry-After 头")
	}

	// 锁定期内受保护接口同样进不去（登录拿不到新会话）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/system/info", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("锁定期内管理接口应仍返回 401，实际 %d", w.Code)
	}

	// 成功登录清零失败计数：人手抖几次不应被终身追责
	s.loginGuards.recordSuccess("192.0.2.1")
	if wait, locked := s.loginGuards.blocked("192.0.2.1"); locked {
		t.Fatalf("成功登录后不应仍处于锁定期（wait=%v）", wait)
	}
}

// ---- 状态接口 ----

func TestConsoleAuthStatus(t *testing.T) {
	t.Run("启用且未登录", func(t *testing.T) {
		_, r := newAuthTestServer(t, testAdminKey)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/auth/status", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status 应始终可达，实际 %d", w.Code)
		}
		var body struct {
			Enabled       bool `json:"enabled"`
			Authenticated bool `json:"authenticated"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !body.Enabled || body.Authenticated {
			t.Fatalf("应报告 enabled=true authenticated=false，实际 %+v", body)
		}
	})

	t.Run("关闭时对前端表现为已登录", func(t *testing.T) {
		_, r := newAuthTestServer(t, "")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/auth/status", nil))
		var body struct {
			Enabled       bool `json:"enabled"`
			Authenticated bool `json:"authenticated"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Enabled || !body.Authenticated {
			t.Fatalf("关闭时应报告 enabled=false authenticated=true，实际 %+v", body)
		}
	})
}

// ---- 登出 ----

func TestConsoleLogout(t *testing.T) {
	_, r := newAuthTestServer(t, testAdminKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/admin/auth/logout", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("登出应返回 200，实际 %d", w.Code)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == consoleCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("登出应下发清除指令的会话 Cookie")
	}
	if cookie.MaxAge != -1 {
		t.Errorf("登出的 Cookie 应带 Max-Age=-1（浏览器据以删除），实际 %d", cookie.MaxAge)
	}
}
