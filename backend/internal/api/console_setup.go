package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/consoleauth"
)

// 设置向导、认证方式切换、密钥改密与 2FA 换绑的接口。
//
// 接口分两层，权限来源不同：
//   - 向导层（/auth/setup/*）：认证方式为 unset 时，调用者只可能有
//     引导会话（用 RELAY_ADMIN_KEY 换来的）—— 它就是这一层的准入凭证。
//     method 已是 key/totp 后这些接口全部拒绝：向导是「第一次」用的，
//     之后的一切变更走切换/改密/换绑，那条路要求**旧凭据**参与验证。
//   - 受保护层（/auth/switch/*、/auth/password/*、/auth/totp/*）：
//     要求有效会话（requireConsoleAuth 已在路由组上）；从一种方式
//     切到另一种时还要求旧方式的凭据 —— 劫持了浏览器会话的人可以
//     看到界面，但不该能把主人的 2FA 静默换成自己的密钥。
//
// 所有成功变更都重签会话 Cookie：consoleauth 层在变更时轮换凭据版本，
// 旧令牌（含可能被复制的）立即失效，当前浏览器无感续期。

// registerAuthSetupRoutes 挂载向导与凭据自管接口。
// 由 registerAdminRoutes 调用；setup 组挂公开侧（要求向导会话），
// 其余挂 admin 受保护组。
func registerAuthSetupRoutes(adminPublic, admin *gin.RouterGroup, s *Server) {
	setup := adminPublic.Group("/auth/setup", s.requireSetupSession())
	{
		setup.GET("/status", s.setupStatus)
		setup.POST("/totp/init", s.setupTOTPInit)
		setup.POST("/totp/confirm", s.setupTOTPConfirm)
		setup.POST("/key", s.setupKey)
	}
	auth := admin.Group("/auth")
	{
		// 密钥 → 2FA：init 免旧凭据（有效会话即准入，与登录后才能
		// 打开设置页一致）；confirm 验新码（证明手机真的绑上了）
		auth.POST("/switch/to-totp/init", s.requireMethod(consoleauth.MethodKey), s.switchToTOTPInit)
		auth.POST("/switch/to-totp/confirm", s.requireMethod(consoleauth.MethodKey), s.switchToTOTPConfirm)
		// 2FA → 密钥：当前码 + 新密钥一次提交（旧的码先验，新密钥再设）
		auth.POST("/switch/to-key", s.requireMethod(consoleauth.MethodTOTP), s.switchToKey)
		// 密钥改密
		auth.POST("/password/change", s.requireMethod(consoleauth.MethodKey), s.changeKey)
		// 2FA 换绑：init 验当前码（证明是主人），confirm 验新码
		auth.POST("/totp/rebind/init", s.requireMethod(consoleauth.MethodTOTP), s.rebindTOTPInit)
		auth.POST("/totp/rebind/confirm", s.requireMethod(consoleauth.MethodTOTP), s.rebindTOTPConfirm)
	}
}

// requireSetupSession 要求「向导会话」：method=unset 时用引导密钥
// 换来的会话（或鉴权整体关闭的本地部署直接放行）；method 已是
// key/totp 后，向导接口一律 409（见各 handler 的守卫）—— 那时候该走
// 切换/改密/换绑，那条路要求当前凭据参与验证。
func (s *Server) requireSetupSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 鉴权整体关闭（unset 且无引导密钥的旧本地部署）：向导没有
		// 可保护的东西，放行 —— 与 requireConsoleAuth 的旧行为一致。
		// 这类部署要去初始化，authStore 必须可用（库已就绪）；
		// 库不可用时由 handler 的 guardAuthStore 回 503 讲清楚原因。
		if s.adminKeyHash == nil && s.authMethod() == consoleauth.MethodUnset {
			c.Next()
			return
		}
		token, err := c.Cookie(consoleCookieName)
		if err != nil || !s.verifySessionToken(token) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{
				"code":    http.StatusUnauthorized,
				"message": "请先用引导密钥登录，再进行初始化设置",
				"type":    "unauthorized_error",
			}})
			return
		}
		c.Next()
	}
}

// requireMethod 是受保护层的方式守卫：方式不符直接 409，
// 错误信息里带上当前方式，让前端能据此渲染正确的界面。
func (s *Server) requireMethod(m consoleauth.Method) gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.authMethod() != m {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{
				"code":    http.StatusConflict,
				"message": "当前认证方式不是 " + methodLabel(m),
				"type":    "conflict_error",
			}})
			return
		}
		c.Next()
	}
}

func methodLabel(m consoleauth.Method) string {
	switch m {
	case consoleauth.MethodTOTP:
		return "动态码（2FA）"
	case consoleauth.MethodKey:
		return "管理密钥"
	default:
		return "未初始化"
	}
}

// guardAuthStore 统一处理 authStore 缺失（fail closed）。
func (s *Server) guardAuthStore(c *gin.Context) bool {
	if s.authStore == nil {
		writeAuthUnavailable(c)
		return false
	}
	return true
}

// ---- 设置向导 ----

// setupStatus 返回向导进度：有没有进行中的绑定（决定前端显示二维码
// 还是重新生成按钮）。
func (s *Server) setupStatus(c *gin.Context) {
	if !s.guardAuthStore(c) {
		return
	}
	st := s.authStore.Snapshot()
	if st.Method != consoleauth.MethodUnset {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"code":    http.StatusConflict,
			"message": "认证方式已初始化，无需再次设置",
			"type":    "conflict_error",
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"method":      string(st.Method),
		"has_pending": st.Pending != nil,
		"has_key":     s.adminKeyHash != nil,
	})
}

// setupTOTPInit 生成待绑定的 TOTP 密钥，返回 otpauth URI（前端渲染
// 二维码）。重复调用覆盖 pending：用户重新扫码即以最后一次为准。
func (s *Server) setupTOTPInit(c *gin.Context) {
	if !s.guardAuthStore(c) {
		return
	}
	if s.authMethod() != consoleauth.MethodUnset {
		writeConflict(c, "认证方式已初始化，请使用「切换/换绑」功能")
		return
	}
	_, uri, err := s.authStore.SetupTOTPInit()
	if err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"otpauth_uri": uri, "issuer": consoleauth.TOTPIssuer})
}

// setupTOTPConfirm 验证动态码并提交 2FA。成功即完成向导：
// 引导密钥退役，当前浏览器拿到正式会话。
func (s *Server) setupTOTPConfirm(c *gin.Context) {
	if !s.guardAuthStore(c) {
		return
	}
	if s.loginLocked(c) {
		return
	}
	if s.authMethod() != consoleauth.MethodUnset {
		writeConflict(c, "认证方式已初始化，请使用「切换/换绑」功能")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Code) == "" {
		writeBadRequest(c, "请输入认证器上的 6 位动态码")
		return
	}
	if err := s.authStore.SetupTOTPConfirm(strings.TrimSpace(body.Code), time.Now()); err != nil {
		s.writeTOTPErr(c, err)
		return
	}
	s.respondCredChanged(c, "2FA 绑定完成，引导密钥已失效")
}

// setupKey 设置密钥登录方式（用户手动填写，不再自动生成）。
// 成功即完成向导。
func (s *Server) setupKey(c *gin.Context) {
	if !s.guardAuthStore(c) {
		return
	}
	if s.authMethod() != consoleauth.MethodUnset {
		writeConflict(c, "认证方式已初始化，请使用「修改密钥」功能")
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeBadRequest(c, "请求体格式不正确")
		return
	}
	key := body.Key
	if msg := s.validateNewKey(key); msg != "" {
		writeBadRequest(c, msg)
		return
	}
	if err := s.authStore.SetupKey(key); err != nil {
		writeInternalError(c, err)
		return
	}
	s.respondCredChanged(c, "管理密钥已设置，自动生成的引导密钥已失效")
}

// ---- 认证方式切换 ----

// switchToTOTPInit（密钥 → 2FA 第一步）：生成 pending 密钥与二维码。
func (s *Server) switchToTOTPInit(c *gin.Context) {
	if !s.guardAuthStore(c) {
		return
	}
	_, uri, err := s.authStore.SetupTOTPInit()
	if err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"otpauth_uri": uri, "issuer": consoleauth.TOTPIssuer})
}

// switchToTOTPConfirm（密钥 → 2FA 第二步）：验新码提交。
func (s *Server) switchToTOTPConfirm(c *gin.Context) {
	if !s.guardAuthStore(c) {
		return
	}
	// 与登录同一个限流闸门：这里的验码同样是「猜 6 位数字」，
	// 少了它，拿到一个有效会话的人可以无限次穷举（见 consoleLogin 的说明）。
	if s.loginLocked(c) {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Code) == "" {
		writeBadRequest(c, "请输入认证器上的 6 位动态码")
		return
	}
	if err := s.authStore.SwitchToTOTPConfirm(strings.TrimSpace(body.Code), time.Now()); err != nil {
		s.writeTOTPErr(c, err)
		return
	}
	s.respondCredChanged(c, "已切换到动态码（2FA）登录，原密钥已失效")
}

// switchToKey（2FA → 密钥）：验当前动态码 + 设新密钥，一次提交。
func (s *Server) switchToKey(c *gin.Context) {
	if !s.guardAuthStore(c) {
		return
	}
	if s.loginLocked(c) {
		return
	}
	var body struct {
		CurrentCode string `json:"current_code"`
		NewKey      string `json:"new_key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeBadRequest(c, "请求体格式不正确")
		return
	}
	if msg := s.validateNewKey(body.NewKey); msg != "" {
		writeBadRequest(c, msg)
		return
	}
	if err := s.authStore.SwitchToKey(strings.TrimSpace(body.CurrentCode), body.NewKey, time.Now()); err != nil {
		s.writeTOTPErr(c, err)
		return
	}
	s.respondCredChanged(c, "已切换到密钥登录，原动态码绑定已失效")
}

// ---- 密钥改密 ----

func (s *Server) changeKey(c *gin.Context) {
	if !s.guardAuthStore(c) {
		return
	}
	if s.loginLocked(c) {
		return
	}
	var body struct {
		CurrentKey string `json:"current_key"`
		NewKey     string `json:"new_key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeBadRequest(c, "请求体格式不正确")
		return
	}
	if msg := s.validateNewKey(body.NewKey); msg != "" {
		writeBadRequest(c, msg)
		return
	}
	if err := s.authStore.ChangeKey(body.CurrentKey, body.NewKey); err != nil {
		if errors.Is(err, consoleauth.ErrWrongKey) {
			s.loginFailure(c, "当前密钥不正确")
			return
		}
		if errors.Is(err, consoleauth.ErrWrongMethod) {
			writeConflict(c, "当前认证方式不是管理密钥")
			return
		}
		writeInternalError(c, err)
		return
	}
	s.respondCredChanged(c, "密钥已修改，旧密钥与旧会话全部失效")
}

// ---- 2FA 换绑 ----

func (s *Server) rebindTOTPInit(c *gin.Context) {
	if !s.guardAuthStore(c) {
		return
	}
	if s.loginLocked(c) {
		return
	}
	var body struct {
		CurrentCode string `json:"current_code"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.CurrentCode) == "" {
		writeBadRequest(c, "请先输入当前认证器上的 6 位动态码")
		return
	}
	_, uri, err := s.authStore.RebindTOTPInit(strings.TrimSpace(body.CurrentCode), time.Now())
	if err != nil {
		s.writeTOTPErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"otpauth_uri": uri, "issuer": consoleauth.TOTPIssuer})
}

func (s *Server) rebindTOTPConfirm(c *gin.Context) {
	if !s.guardAuthStore(c) {
		return
	}
	if s.loginLocked(c) {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Code) == "" {
		writeBadRequest(c, "请输入新认证器上的 6 位动态码")
		return
	}
	if err := s.authStore.RebindTOTPConfirm(strings.TrimSpace(body.Code), time.Now()); err != nil {
		s.writeTOTPErr(c, err)
		return
	}
	s.respondCredChanged(c, "动态码绑定已更换，原认证器的 6 位码全部失效")
}

// ---- 共用小件 ----

// validateNewKey 校验用户手动设置的密钥，返回空串表示通过。
// 口径与 config.validate 的启动校验一致（≥16 位、非占位值），
// 另加「不能与加密主密钥相同」—— 那条检查在启动时对新密钥是
// 看不见的（密钥不落配置），必须在设置时就地卡住。
// 密钥取原值不 trim：登录比对用的就是原值，设置时悄悄 trim 会造成
// 「输入的」和「生效的」不一致。
func (s *Server) validateNewKey(key string) string {
	trimmed := strings.TrimSpace(key)
	if len(trimmed) == 0 {
		return "密钥不能为空"
	}
	// 占位值检查放在长度之前：CHANGE_ME 只有 9 位，先报「太短」会把
	// 「你还在用示例值」这个更要紧的信息盖住（与前端 keyStrengthError 同序）。
	if key == "CHANGE_ME" {
		return "不能使用占位值 CHANGE_ME"
	}
	if len(key) < 16 {
		return "密钥长度不足 16 位，公网部署会被暴力破解；请使用更长的随机字符串"
	}
	if sec := s.keySecret(); sec != "" && key == sec {
		return "密钥不能与加密主密钥（RELAY_SECRET）相同"
	}
	return ""
}

// keySecret 取当前配置的加密主密钥（供 validateNewKey 比对）。
func (s *Server) keySecret() string {
	if s.deps == nil || s.deps.Config == nil {
		return ""
	}
	return s.deps.Config.Security.Secret
}

// respondCredChanged 凭据变更成功后的统一响应：重签会话 + 成功消息。
func (s *Server) respondCredChanged(c *gin.Context, message string) {
	exp, err := s.issueSession(c)
	if err != nil {
		// 变更已生效但重签失败：用户需要重新登录。这不是静默故障 ——
		// 前端会收到 503 并跳登录页，旧会话已因版本轮换失效。
		writeAuthUnavailable(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"message":    message,
		"method":     string(s.authMethod()),
		"expires_at": exp.UTC().Format(time.RFC3339),
	})
}

// writeTOTPErr 把 consoleauth 的错误翻译成 HTTP 响应。
// 放在 *Server 上是因为改密路径需要走 loginFailure（限流记账）。
func (s *Server) writeTOTPErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, consoleauth.ErrNotInitialized):
		writeConflict(c, "没有进行中的绑定，请重新生成二维码")
	case errors.Is(err, consoleauth.ErrReplayed):
		s.loginFailure(c, err.Error())
	case errors.Is(err, consoleauth.ErrInvalidCode):
		s.loginFailure(c, "动态码不正确")
	case errors.Is(err, consoleauth.ErrWrongKey):
		s.loginFailure(c, "密钥不正确")
	case errors.Is(err, consoleauth.ErrWrongMethod):
		writeConflict(c, "当前认证方式不支持该操作")
	default:
		writeInternalError(c, err)
	}
}

// writeConflict / writeBadRequest 是本文件的响应小件，
// 与 helpers.go 的 writeUpstreamError 同款形状（规则 15：不漏库细节）。
func writeConflict(c *gin.Context, message string) {
	c.JSON(http.StatusConflict, gin.H{"error": gin.H{
		"code":    http.StatusConflict,
		"message": message,
		"type":    "conflict_error",
	}})
}

func writeBadRequest(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"code":    http.StatusBadRequest,
		"message": message,
		"type":    "invalid_request_error",
	}})
}
