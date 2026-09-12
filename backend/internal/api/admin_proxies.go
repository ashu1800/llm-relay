package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/proxy"
)

// 代理测试的超时。10 秒是个折中：跨国代理握手 + 目标站点响应，
// 正常都在 2 秒内；超过 10 秒还没结果的话，用户等在这里也没意义。
const proxyTestTimeout = 10 * time.Second

// registerProxyRoutes 挂载代理管理接口。
func registerProxyRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/proxies")
	r.GET("", s.listProxies)
	r.POST("", s.createProxy)
	// 注意顺序：/test 必须在 /:id 之前注册，否则会被当成 id=test
	r.POST("/test", s.testProxyDraft)
	r.PUT("/:id", s.updateProxy)
	r.DELETE("/:id", s.deleteProxy)
	r.POST("/:id/test", s.testProxySaved)
}

type proxyPayload struct {
	Name     *string `json:"name"`
	Protocol *string `json:"protocol"`
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	Username *string `json:"username"`
	// Password 的三态（区分「没传」与「传了空串」是刻意的）：
	//   null / 不传 -> 不改（编辑时留空表示保留原密码）
	//   ""          -> 清空密码
	//   其它        -> 设为新密码
	Password *string `json:"password"`
	Enabled  *bool   `json:"enabled"`
}

// proxyView 是给界面看的形状：不含密文，但告诉界面「有没有配密码」。
func proxyView(p model.Proxy) gin.H {
	return gin.H{
		"id": p.ID, "name": p.Name, "protocol": p.Protocol,
		"host": p.Host, "port": p.Port, "username": p.Username,
		"has_password": p.PasswordEnc != "",
		"enabled":      p.Enabled,
		"last_status":  p.LastStatus, "last_latency_ms": p.LastLatencyMs,
		"last_error": p.LastError, "last_tested_at": p.LastTestedAt,
		"created_at": p.CreatedAt, "updated_at": p.UpdatedAt,
	}
}

func (s *Server) listProxies(c *gin.Context) {
	var items []model.Proxy
	if err := s.deps.Store.DB().Order("id").Find(&items).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, p := range items {
		out = append(out, proxyView(p))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "total": len(out)})
}

func (s *Server) createProxy(c *gin.Context) {
	var p proxyPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	name := strings.TrimSpace(derefString(p.Name))
	if name == "" {
		writeUpstreamError(c, http.StatusBadRequest, "代理名称必填", "invalid_request_error")
		return
	}
	cfg := proxy.Config{
		Protocol: derefString(p.Protocol),
		Host:     strings.TrimSpace(derefString(p.Host)),
		Username: strings.TrimSpace(derefString(p.Username)),
	}
	if cfg.Protocol == "" {
		cfg.Protocol = model.ProxyProtocolSOCKS5
	}
	if p.Port != nil {
		cfg.Port = *p.Port
	}
	if err := cfg.Validate(); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	row := model.Proxy{
		Name: name, Protocol: cfg.Protocol, Host: cfg.Host, Port: cfg.Port,
		Username: cfg.Username, Enabled: enabled,
		// 新建时状态未知：界面显示「未测试」而不是假的「正常」
		LastStatus: "unknown",
	}
	if pw := derefString(p.Password); pw != "" {
		enc, err := s.deps.Cipher.Encrypt(pw)
		if err != nil {
			writeUpstreamError(c, http.StatusInternalServerError, "加密代理密码失败: "+err.Error(), "internal_error")
			return
		}
		row.PasswordEnc = enc
	}
	if err := s.deps.Store.DB().Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			writeUpstreamError(c, http.StatusConflict, "代理名已存在: "+name, "invalid_request_error")
			return
		}
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	c.JSON(http.StatusOK, proxyView(row))
}

func (s *Server) updateProxy(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	db := s.deps.Store.DB()
	var cur model.Proxy
	if err := db.First(&cur, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "代理不存在", "not_found_error")
		return
	}
	var p proxyPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}

	updates := map[string]any{}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		if name == "" {
			writeUpstreamError(c, http.StatusBadRequest, "代理名称不能为空", "invalid_request_error")
			return
		}
		updates["name"] = name
	}
	// 协议/地址/端口三者要一起校验：只改端口时也要拿旧的地址一起验，
	// 否则会出现「地址非法但只改端口就被放过去」的半截配置
	next := proxy.Config{
		Protocol: cur.Protocol, Host: cur.Host, Port: cur.Port, Username: cur.Username,
	}
	if p.Protocol != nil {
		next.Protocol = *p.Protocol
	}
	if p.Host != nil {
		next.Host = strings.TrimSpace(*p.Host)
	}
	if p.Port != nil {
		next.Port = *p.Port
	}
	if err := next.Validate(); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if p.Protocol != nil {
		updates["protocol"] = next.Protocol
	}
	if p.Host != nil {
		updates["host"] = next.Host
	}
	if p.Port != nil {
		updates["port"] = next.Port
	}
	if p.Username != nil {
		updates["username"] = strings.TrimSpace(*p.Username)
	}
	if p.Enabled != nil {
		updates["enabled"] = *p.Enabled
	}
	if p.Password != nil {
		if *p.Password == "" {
			updates["password_enc"] = ""
		} else {
			enc, err := s.deps.Cipher.Encrypt(*p.Password)
			if err != nil {
				writeUpstreamError(c, http.StatusInternalServerError, "加密代理密码失败: "+err.Error(), "internal_error")
				return
			}
			updates["password_enc"] = enc
		}
		// 凭据变了，上一次的测试结论就不再代表这份配置
		updates["last_status"] = "unknown"
	}
	// 连接参数变了同理：旧的成功记录不能继续显示
	if p.Protocol != nil || p.Host != nil || p.Port != nil {
		updates["last_status"] = "unknown"
	}
	if len(updates) == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}
	if err := db.Model(&model.Proxy{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		if isUniqueViolation(err) {
			writeUpstreamError(c, http.StatusConflict, "代理名已存在", "invalid_request_error")
			return
		}
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	// 改了连接参数就把旧状态清干净，免得界面显示的成功记录与当前配置对不上
	if _, ok := updates["last_status"]; ok {
		if err := db.Model(&model.Proxy{}).Where("id = ?", id).Updates(map[string]any{
			"last_status": "unknown", "last_error": "", "last_latency_ms": 0, "last_tested_at": nil,
		}).Error; err != nil {
			writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates)})
}

func (s *Server) deleteProxy(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	db := s.deps.Store.DB()
	// 被渠道引用时不许删：删掉之后渠道会静默变成直连，
	// 而用户以为它还在走代理 —— 「配置看起来生效、实际没生效」是最难查的一类问题
	var used int64
	if err := db.Model(&model.Channel{}).Where("proxy_id = ?", id).Count(&used).Error; err != nil {
		writeUpstreamError(c, http.StatusInternalServerError, err.Error(), "internal_error")
		return
	}
	if used > 0 {
		writeUpstreamError(c, http.StatusConflict,
			"还有 "+itoa(int(used))+" 个渠道在用这个代理，请先改成别的代理或直连", "invalid_request_error")
		return
	}
	deleteByID(c, db, &model.Proxy{}, id, "代理不存在")
}

// testProxySaved 测试已保存的代理，并把结果写回该行。
func (s *Server) testProxySaved(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	db := s.deps.Store.DB()
	var row model.Proxy
	if err := db.First(&row, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "代理不存在", "not_found_error")
		return
	}
	cfg := s.proxyConfigOf(row)
	var body struct {
		TestURL string `json:"test_url"`
	}
	_ = c.ShouldBindJSON(&body)

	res := proxy.Test(c.Request.Context(), cfg, body.TestURL, proxyTestTimeout)
	s.recordProxyTest(db, row.ID, res)
	c.JSON(http.StatusOK, proxyTestView(res))
}

// testProxyDraft 测试**还没保存**的配置：新建/编辑时点「测试」用。
//
// 没有这个接口的话，用户只能先保存一份错的配置再测 —— 而错的配置
// 如果在用（渠道指着它），保存动作本身就会影响线上的转发。
func (s *Server) testProxyDraft(c *gin.Context) {
	var body struct {
		ID       uint   `json:"id"`
		Protocol string `json:"protocol"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		TestURL  string `json:"test_url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	cfg := proxy.Config{
		Protocol: body.Protocol, Host: strings.TrimSpace(body.Host),
		Port: body.Port, Username: strings.TrimSpace(body.Username),
		Password: body.Password,
	}
	// 编辑已有代理时密码留空表示「沿用原密码」：这里补上解密后的值，
	// 否则用户会看到「明明没改密码却测不通」
	if cfg.Password == "" && body.ID > 0 {
		var row model.Proxy
		if err := s.deps.Store.DB().First(&row, body.ID).Error; err == nil {
			cfg.Password = s.proxyConfigOf(row).Password
			if cfg.Username == "" {
				cfg.Username = row.Username
			}
		}
	}
	if err := cfg.Validate(); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	res := proxy.Test(c.Request.Context(), cfg, body.TestURL, proxyTestTimeout)
	c.JSON(http.StatusOK, proxyTestView(res))
}

// proxyConfigOf 把库里的行转成拨号配置（密码在这里解密）。
func (s *Server) proxyConfigOf(p model.Proxy) proxy.Config {
	cfg := proxy.Config{Protocol: p.Protocol, Host: p.Host, Port: p.Port, Username: p.Username}
	if p.PasswordEnc != "" {
		if plain, err := s.deps.Cipher.Decrypt(p.PasswordEnc); err == nil {
			cfg.Password = plain
		}
		// 解不开时留空：测试会以「认证失败」告终，
		// 比在这里直接报「解密失败」更接近用户能采取的行动（重填密码）
	}
	return cfg
}

// recordProxyTest 把测试结果写回该行。写失败只记日志，不影响本次测试结论。
func (s *Server) recordProxyTest(db *gorm.DB, id uint, res proxy.TestResult) {
	status := "fail"
	if res.OK {
		status = "ok"
	}
	now := time.Now()
	if err := db.Model(&model.Proxy{}).Where("id = ?", id).Updates(map[string]any{
		"last_status": status, "last_latency_ms": res.LatencyMs,
		"last_error": res.Error, "last_tested_at": &now,
	}).Error; err != nil {
		// 结果已经拿到了，写不进去只说明这一列没更新，不该让测试本身失败
		return
	}
}

func proxyTestView(res proxy.TestResult) gin.H {
	return gin.H{
		"ok": res.OK, "latency_ms": res.LatencyMs,
		"status_code": res.StatusCode, "error": res.Error,
		"tested_at": time.Now(),
	}
}

// checkProxyExists 校验渠道要引用的代理存在；id 为 0 表示直连，直接放行。
func checkProxyExists(s *Server, id uint) error {
	if id == 0 {
		return nil
	}
	var n int64
	if err := s.deps.Store.DB().Model(&model.Proxy{}).Where("id = ?", id).Count(&n).Error; err != nil {
		return errors.New("校验代理失败: " + err.Error())
	}
	if n == 0 {
		return errors.New("指定的代理不存在")
	}
	return nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// isUniqueViolation 判断是不是唯一约束冲突（Postgres 23505）。
//
// 靠错误码而不是 GORM 的错误类型：驱动包装几层之后类型断言经常失效，
// 而错误码是驱动直接带出来的，最可靠。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key")
}
