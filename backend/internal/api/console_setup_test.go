package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/consoleauth"
)

// 设置向导 / 切换 / 改密 / 换绑的接口测试。
// 全部走 registerAdminRoutes 挂载出的真实路由接线（含中间件顺序），
// 认证状态是内存版 Store —— 与真实存储的差异只在持久化。

// postJSON 向指定路径发 JSON POST。
func postJSON(r *gin.Engine, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.1:1234"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func cookieOf(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == consoleCookieName {
			return c
		}
	}
	t.Fatal("响应没有会话 Cookie")
	return nil
}

func bodyJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON: %v\n%s", err, w.Body.String())
	}
	return m
}

func codeFrom(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := consoleauth.Code(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func mustTOTPLogin(t *testing.T, s *Server, r *gin.Engine, at time.Time) *http.Cookie {
	t.Helper()
	plain, err := s.authStore.TOTPSecretPlainForTest()
	if err != nil {
		t.Fatal(err)
	}
	w := postJSON(r, "/api/admin/auth/login", `{"code":"`+codeFrom(t, plain, at)+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("TOTP 登录失败: %d %s", w.Code, w.Body.String())
	}
	return cookieOf(t, w)
}

func TestSetupWizardWithTOTP(t *testing.T) {
	s, r := newAuthTestServer(t, testAdminKey)
	// 未登录直接取向导：401
	if w := postJSON(r, "/api/admin/auth/setup/totp/init", `{}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("未持引导会话应 401，实际 %d", w.Code)
	}
	// 引导密钥登录 → 拿到向导会话
	w := postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("引导密钥登录失败: %d %s", w.Code, w.Body.String())
	}
	boot := cookieOf(t, w)

	// status：未初始化、无 pending
	w = getSession(r, "/api/admin/auth/setup/status", boot)
	m := bodyJSON(t, w)
	if m["method"] != "unset" || m["has_pending"] != false {
		t.Fatalf("向导状态不正确: %v", m)
	}

	// init：拿到 otpauth URI
	w = postJSON(r, "/api/admin/auth/setup/totp/init", `{}`, boot)
	if w.Code != http.StatusOK {
		t.Fatalf("init 失败: %d %s", w.Code, w.Body.String())
	}
	uri := bodyJSON(t, w)["otpauth_uri"].(string)
	if !strings.HasPrefix(uri, "otpauth://totp/LLM%20Relay:admin?") {
		t.Fatalf("URI 形状不对: %s", uri)
	}
	// 从 pending 解出明文密钥。窗口纪律：W = 当前 30 秒窗口起点，
	// 一次窗口的码只能消费一次（防重放），后续动作依次用 W+1…
	// Verify 的 ±1 窗口意味着 W-1/W/W+1 的码当下都合法。
	plain, err := s.authStore.PendingPlainForTest()
	if err != nil {
		t.Fatal(err)
	}
	W := time.Now().Truncate(totpWindowSeconds())
	at := W

	// 错码 confirm：拒绝且方式仍是 unset
	if w := postJSON(r, "/api/admin/auth/setup/totp/confirm", `{"code":"000000"}`, boot); w.Code != http.StatusUnauthorized {
		t.Fatalf("错码应 401，实际 %d", w.Code)
	}
	if s.authMethod() != consoleauth.MethodUnset {
		t.Fatal("失败的确认不应变更认证方式")
	}

	// 正码 confirm：完成向导、签发正式会话
	w = postJSON(r, "/api/admin/auth/setup/totp/confirm", `{"code":"`+codeFrom(t, plain, at)+`"}`, boot)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm 失败: %d %s", w.Code, w.Body.String())
	}
	if s.authMethod() != consoleauth.MethodTOTP {
		t.Fatal("confirm 后应为 totp 方式")
	}
	if s.adminKeyMatches(testAdminKey) && s.authMethod() == consoleauth.MethodTOTP {
		// 引导密钥已退役：再用它登录必须失败
		if w := postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`); w.Code != http.StatusUnauthorized {
			t.Fatalf("向导完成后引导密钥应失效，实际 %d", w.Code)
		}
	}

	// 新会话有效；boot 会话因版本轮换失效
	newSess := cookieOf(t, w)
	if w := getSession(r, "/api/admin/system/info", newSess); w.Code != http.StatusOK {
		t.Fatalf("新会话应有效: %d", w.Code)
	}
	if w := getSession(r, "/api/admin/system/info", boot); w.Code != http.StatusUnauthorized {
		t.Fatalf("引导会话应因版本轮换失效: %d", w.Code)
	}

	// TOTP 登录（不带密钥）走通；带密钥字段被拒（方式守卫先于验码）
	w = postJSON(r, "/api/admin/auth/login", `{"code":"`+codeFrom(t, plain, at.Add(totpWindowSeconds()))+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("纯动态码登录失败: %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(r, "/api/admin/auth/login", `{"key":"x","code":"`+codeFrom(t, plain, at.Add(-totpWindowSeconds()))+`"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("2FA 模式下带密钥应拒绝，实际 %d", w.Code)
	}
}

func TestSetupWizardWithKey(t *testing.T) {
	s, r := newAuthTestServer(t, testAdminKey)
	w := postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`)
	boot := cookieOf(t, w)

	// 弱密钥被拒
	if w := postJSON(r, "/api/admin/auth/setup/key", `{"key":"short"}`, boot); w.Code != http.StatusBadRequest {
		t.Fatalf("弱密钥应 400，实际 %d", w.Code)
	}
	// 与 RELAY_SECRET 相同被拒
	if w := postJSON(r, "/api/admin/auth/setup/key", `{"key":"test-relay-secret"}`, boot); w.Code != http.StatusBadRequest {
		t.Fatalf("与主密钥相同应 400，实际 %d", w.Code)
	}
	// 正常设置
	w = postJSON(r, "/api/admin/auth/setup/key", `{"key":"my-own-key-0123456789"}`, boot)
	if w.Code != http.StatusOK {
		t.Fatalf("设置密钥失败: %d %s", w.Code, w.Body.String())
	}
	if s.authMethod() != consoleauth.MethodKey {
		t.Fatal("设置密钥后应为 key 方式")
	}
	// 旧引导密钥失效，新密钥可登录
	if w := postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("引导密钥应失效，实际 %d", w.Code)
	}
	if w := postJSON(r, "/api/admin/auth/login", `{"key":"my-own-key-0123456789"}`); w.Code != http.StatusOK {
		t.Fatalf("新密钥登录失败: %d %s", w.Code, w.Body.String())
	}
	// 已初始化后再调向导接口：409
	sess := cookieOf(t, w)
	if w := postJSON(r, "/api/admin/auth/setup/key", `{"key":"another-key-0123456789"}`, sess); w.Code != http.StatusConflict {
		t.Fatalf("已初始化后向导接口应 409，实际 %d", w.Code)
	}
}

func TestSwitchKeyToTOTPAndBack(t *testing.T) {
	s, r := newAuthTestServer(t, testAdminKey)
	// 先完成向导（密钥方式）
	w := postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`)
	boot := cookieOf(t, w)
	setupResp := postJSON(r, "/api/admin/auth/setup/key", `{"key":"my-own-key-0123456789"}`, boot)
	if setupResp.Code != http.StatusOK {
		t.Fatalf("向导失败: %s", setupResp.Body.String())
	}
	// 注意取 setup 响应的新 Cookie：向导轮换了凭据版本，boot 已失效
	sess := cookieOf(t, setupResp)

	// 未登录调用切换：401
	if w := postJSON(r, "/api/admin/auth/switch/to-totp/init", `{}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录切换应 401，实际 %d", w.Code)
	}
	// init → 扫码 → confirm。窗口纪律同前：W 起步，每窗口一码。
	if w := postJSON(r, "/api/admin/auth/switch/to-totp/init", `{}`, sess); w.Code != http.StatusOK {
		t.Fatalf("切换 init 失败: %s", w.Body.String())
	}
	plain, err := s.authStore.PendingPlainForTest()
	if err != nil {
		t.Fatal(err)
	}
	W := time.Now().Truncate(totpWindowSeconds())
	w = postJSON(r, "/api/admin/auth/switch/to-totp/confirm", `{"code":"`+codeFrom(t, plain, W)+`"}`, sess)
	if w.Code != http.StatusOK {
		t.Fatalf("切换 confirm 失败: %d %s", w.Code, w.Body.String())
	}
	if s.authMethod() != consoleauth.MethodTOTP {
		t.Fatal("切换后应为 totp")
	}
	totpSess := cookieOf(t, w)

	// 旧密钥不能再登录（二选一）
	if w := postJSON(r, "/api/admin/auth/login", `{"key":"my-own-key-0123456789"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("切到 2FA 后密钥登录应失效，实际 %d", w.Code)
	}

	// 切回密钥：错码拒绝；对码 + 弱密钥拒绝；全对成功
	curPlain, err := s.authStore.TOTPSecretPlainForTest()
	if err != nil {
		t.Fatal(err)
	}
	if w := postJSON(r, "/api/admin/auth/switch/to-key",
		`{"current_code":"000000","new_key":"brand-new-key-0123456789"}`, totpSess); w.Code != http.StatusUnauthorized {
		t.Fatalf("错码切换应 401，实际 %d", w.Code)
	}
	// 弱新密钥的请求在验码**之前**就被 400 拦下（handler 先校验密钥强度），
	// W+1 的码没有被消费 —— 最终切换继续用它
	if w := postJSON(r, "/api/admin/auth/switch/to-key",
		`{"current_code":"`+codeFrom(t, curPlain, W.Add(totpWindowSeconds()))+`","new_key":"short"}`, totpSess); w.Code != http.StatusBadRequest {
		t.Fatalf("弱新密钥应 400，实际 %d", w.Code)
	}
	w = postJSON(r, "/api/admin/auth/switch/to-key",
		`{"current_code":"`+codeFrom(t, curPlain, W.Add(totpWindowSeconds()))+`","new_key":"brand-new-key-0123456789"}`, totpSess)
	if w.Code != http.StatusOK {
		t.Fatalf("切回密钥失败: %d %s", w.Code, w.Body.String())
	}
	if s.authMethod() != consoleauth.MethodKey {
		t.Fatal("切回后应为 key")
	}
	if !s.authStore.MatchKey("brand-new-key-0123456789") {
		t.Fatal("切换设置的新密钥应生效")
	}
}

func TestChangeKeyFlow(t *testing.T) {
	s, r := newAuthTestServer(t, testAdminKey)
	w := postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`)
	boot := cookieOf(t, w)
	_ = postJSON(r, "/api/admin/auth/setup/key", `{"key":"my-own-key-0123456789"}`, boot)
	sess := cookieOf(t, postJSON(r, "/api/admin/auth/login", `{"key":"my-own-key-0123456789"}`))

	// 旧密钥错：401 且不变
	if w := postJSON(r, "/api/admin/auth/password/change",
		`{"current_key":"wrong-key","new_key":"changed-key-0123456789"}`, sess); w.Code != http.StatusUnauthorized {
		t.Fatalf("旧密钥错应 401，实际 %d", w.Code)
	}
	if !s.authStore.MatchKey("my-own-key-0123456789") {
		t.Fatal("失败的改密不应变更密钥")
	}
	// 正确改密
	w = postJSON(r, "/api/admin/auth/password/change",
		`{"current_key":"my-own-key-0123456789","new_key":"changed-key-0123456789"}`, sess)
	if w.Code != http.StatusOK {
		t.Fatalf("改密失败: %d %s", w.Code, w.Body.String())
	}
	// 旧密钥失效、新密钥可登录、旧会话失效（版本轮换）
	if w := postJSON(r, "/api/admin/auth/login", `{"key":"my-own-key-0123456789"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("旧密钥应失效，实际 %d", w.Code)
	}
	if w := postJSON(r, "/api/admin/auth/login", `{"key":"changed-key-0123456789"}`); w.Code != http.StatusOK {
		t.Fatalf("新密钥登录失败: %d", w.Code)
	}
	if w := getSession(r, "/api/admin/system/info", sess); w.Code != http.StatusUnauthorized {
		t.Fatalf("改密前持有的会话应失效: %d", w.Code)
	}
}

func TestRebindTOTPFlow(t *testing.T) {
	s, r := newAuthTestServer(t, testAdminKey)
	w := postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`)
	boot := cookieOf(t, w)
	// 向导绑 2FA
	if w = postJSON(r, "/api/admin/auth/setup/totp/init", `{}`, boot); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	p1, _ := s.authStore.PendingPlainForTest()
	at := time.Now().Truncate(time.Second)
	sess := cookieOf(t, postJSON(r, "/api/admin/auth/setup/totp/confirm", `{"code":"`+codeFrom(t, p1, at)+`"}`, boot))

	// 换绑 init：当前码错误拒绝
	if w := postJSON(r, "/api/admin/auth/totp/rebind/init", `{"current_code":"000000"}`, sess); w.Code != http.StatusUnauthorized {
		t.Fatalf("错码换绑应 401，实际 %d", w.Code)
	}
	// 当前码正确 → 新 pending。窗口纪律：W 起步，每窗口一码；
	// W 窗口的码已被 confirm 消费，init 用 W+1。
	curPlain, _ := s.authStore.TOTPSecretPlainForTest()
	W := time.Now().Truncate(totpWindowSeconds())
	if w = postJSON(r, "/api/admin/auth/totp/rebind/init", `{"current_code":"`+codeFrom(t, curPlain, W.Add(totpWindowSeconds()))+`"}`, sess); w.Code != http.StatusOK {
		t.Fatalf("换绑 init 失败: %d %s", w.Code, w.Body.String())
	}
	p2, _ := s.authStore.PendingPlainForTest()
	if p2 == p1 {
		t.Fatal("换绑应生成新密钥")
	}
	// 确认新码 → 换绑完成，旧密钥失效
	w = postJSON(r, "/api/admin/auth/totp/rebind/confirm", `{"code":"`+codeFrom(t, p2, W.Add(totpWindowSeconds()))+`"}`, sess)
	if w.Code != http.StatusOK {
		t.Fatalf("换绑 confirm 失败: %d %s", w.Code, w.Body.String())
	}
	newPlain, _ := s.authStore.TOTPSecretPlainForTest()
	if newPlain == p1 {
		t.Fatal("换绑后绑定的应是新密钥")
	}
	// 旧密钥的码不能再登录（绑定已换，窗口宽容也救不了它）
	if w := postJSON(r, "/api/admin/auth/login", `{"code":"`+codeFrom(t, p1, W.Add(totpWindowSeconds()))+`"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("旧密钥的码应失效，实际 %d", w.Code)
	}
	// 新密钥的码可登录：confirm 已消费 p2 的 W+1 码，登录改用
	// 未消费、且对「现在」（窗口 W）仍在 ±1 宽容内的 W 码
	if w := postJSON(r, "/api/admin/auth/login", `{"code":"`+codeFrom(t, p2, W)+`"}`); w.Code != http.StatusOK {
		t.Fatalf("新密钥登录失败: %d %s", w.Code, w.Body.String())
	}
}

func TestTOTPReplayRejectedOnLogin(t *testing.T) {
	s, r := newAuthTestServer(t, testAdminKey)
	w := postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`)
	boot := cookieOf(t, w)
	_ = postJSON(r, "/api/admin/auth/setup/totp/init", `{}`, boot)
	p1, _ := s.authStore.PendingPlainForTest()
	at := time.Now().Truncate(totpWindowSeconds())
	sess := cookieOf(t, postJSON(r, "/api/admin/auth/setup/totp/confirm", `{"code":"`+codeFrom(t, p1, at)+`"}`, boot))
	_ = sess

	code := codeFrom(t, p1, at.Add(totpWindowSeconds()))
	if w := postJSON(r, "/api/admin/auth/login", `{"code":"`+code+`"}`); w.Code != http.StatusOK {
		t.Fatalf("首次登录应成功: %d %s", w.Code, w.Body.String())
	}
	w = postJSON(r, "/api/admin/auth/login", `{"code":"`+code+`"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("同码重放应 401，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "已被使用") {
		t.Fatalf("重放提示应说明原因: %s", w.Body.String())
	}
}

// 验码类接口（切换 / 换绑 / 改密 / 向导确认）必须与登录共用同一道限流闸门。
//
// 这些接口验的都是 6 位动态码或密钥 —— 和登录是同一个「猜数字」问题。
// 曾经它们只有记账（writeTOTPErr → loginFailure）没有检查：拿到一个有效
// 会话的人可以无限次穷举 rebind/totp/init 的当前码，10⁶ 的空间在几小时
// 内撞开；更现实的是把同一 IP 的计数刷爆，把丢手机后想自救的主人锁在门外。
// 这条测试盯的就是「锁定后必须 429，而不是继续验码」。
func TestCredentialEndpointsRespectLoginRateLimit(t *testing.T) {
	oldDelay := loginFailureDelay
	loginFailureDelay = 0
	t.Cleanup(func() { loginFailureDelay = oldDelay })

	s, r := newAuthTestServer(t, testAdminKey)
	// 先完成 2FA 绑定，拿到一个合法会话（模拟「会话已被窃取」的场景）
	w := postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`)
	boot := cookieOf(t, w)
	_ = postJSON(r, "/api/admin/auth/setup/totp/init", `{}`, boot)
	plain, err := s.authStore.PendingPlainForTest()
	if err != nil {
		t.Fatal(err)
	}
	W := time.Now().Truncate(totpWindowSeconds())
	w = postJSON(r, "/api/admin/auth/setup/totp/confirm", `{"code":"`+codeFrom(t, plain, W)+`"}`, boot)
	if w.Code != http.StatusOK {
		t.Fatalf("绑定失败: %d %s", w.Code, w.Body.String())
	}
	sess := cookieOf(t, w)

	// 用错码把限流打满（这些接口现在也记账）
	for i := 0; i < loginMaxFailures; i++ {
		if w := postJSON(r, "/api/admin/auth/totp/rebind/init", `{"current_code":"000000"}`, sess); w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错码应 401，实际 %d %s", i+1, w.Code, w.Body.String())
		}
	}

	// 锁定后：即便是合法会话 + 正确格式的码，也必须 429 而不是继续验码。
	//
	// 不在列表里的两条，原因各不同：
	//   · switch/to-totp/confirm —— 它挂在 requireMethod(key) 上，
	//     当前是 2FA 模式，中间件先回 409，根本走不到验码逻辑。
	//     该方向在当前方式下不可达，不需要限流保护（方向可达时
	//     handler 里的 loginLocked 已经生效，见上面那句）。
	//   · setup/totp/confirm —— requireSetupSession + method 守卫，
	//     已初始化后一律 409，同样不可达。
	cases := []struct{ path, body string }{
		{"/api/admin/auth/totp/rebind/init", `{"current_code":"123456"}`},
		{"/api/admin/auth/totp/rebind/confirm", `{"code":"123456"}`},
		{"/api/admin/auth/switch/to-key", `{"current_code":"123456","new_key":"a-long-enough-new-key"}`},
	}
	for _, c := range cases {
		w := postJSON(r, c.path, c.body, sess)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("%s 在锁定期应 429，实际 %d %s", c.path, w.Code, w.Body.String())
		}
		if w.Header().Get("Retry-After") == "" {
			t.Fatalf("%s 的 429 应带 Retry-After 头", c.path)
		}
	}
}

// 同一道闸门在密钥模式下的可达路径：改密要验旧密钥（可穷举），
// 切到 2FA 的 confirm 要验新码（可穷举）。这两条挂在 requireMethod(key)，
// 只有密钥模式才走得到。
func TestKeyModeCredentialEndpointsRespectRateLimit(t *testing.T) {
	oldDelay := loginFailureDelay
	loginFailureDelay = 0
	t.Cleanup(func() { loginFailureDelay = oldDelay })

	_, r := newAuthTestServer(t, testAdminKey)
	// 用引导密钥完成向导并选「密钥」方式
	boot := cookieOf(t, postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`))
	userKey := "user-chosen-key-0123456789"
	w := postJSON(r, "/api/admin/auth/setup/key", `{"key":"`+userKey+`"}`, boot)
	if w.Code != http.StatusOK {
		t.Fatalf("设置密钥失败: %d %s", w.Code, w.Body.String())
	}
	sess := cookieOf(t, w)

	// 用错误旧密钥把限流打满
	for i := 0; i < loginMaxFailures; i++ {
		if w := postJSON(r, "/api/admin/auth/password/change", `{"current_key":"wrong","new_key":"another-long-key-0123"}`, sess); w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错密钥应 401，实际 %d %s", i+1, w.Code, w.Body.String())
		}
	}

	for _, c := range []struct{ path, body string }{
		{"/api/admin/auth/password/change", `{"current_key":"wrong","new_key":"another-long-key-0123"}`},
		{"/api/admin/auth/switch/to-totp/confirm", `{"code":"123456"}`},
	} {
		w := postJSON(r, c.path, c.body, sess)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("%s 在锁定期应 429，实际 %d %s", c.path, w.Code, w.Body.String())
		}
	}
}

func TestAuthStatusExposesMethod(t *testing.T) {
	_, r := newAuthTestServer(t, testAdminKey)
	w := getSession(r, "/api/admin/auth/status", nil)
	m := bodyJSON(t, w)
	if m["enabled"] != true || m["method"] != "unset" || m["needs_setup"] != true {
		t.Fatalf("unset 状态不正确: %v", m)
	}
	// 完成向导后：needs_setup 变 false
	boot := cookieOf(t, postJSON(r, "/api/admin/auth/login", `{"key":"`+testAdminKey+`"}`))
	_ = postJSON(r, "/api/admin/auth/setup/key", `{"key":"my-own-key-0123456789"}`, boot)
	m = bodyJSON(t, getSession(r, "/api/admin/auth/status", nil))
	if m["method"] != "key" || m["needs_setup"] != false {
		t.Fatalf("key 状态不正确: %v", m)
	}
}

func totpWindowSeconds() time.Duration { return 30 * time.Second }

func getSession(r *gin.Engine, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "192.0.2.1:1234"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
