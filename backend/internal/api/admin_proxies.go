package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/netguard"
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
		writeInternalError(c, err)
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
		writeConflictOrInternal(c, err, "代理名已存在: "+name)
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
	// 连接参数变了同理：旧的成功记录不能继续显示。
	// 用户名也算凭据 —— 只改用户名同样会让原来的成功结论失效
	// （实测：改成错的用户名后列表还显示「正常」，而实际转发已经 401）
	if p.Protocol != nil || p.Host != nil || p.Port != nil || p.Username != nil {
		updates["last_status"] = "unknown"
		// 状态相关的字段与主更新合并成**一条** UPDATE：原来分两次写，
		// 不在一个事务里 —— 第二次失败回 500 时第一次已生效，
		// 留下「状态 unknown 却挂着旧错误/旧延迟」的半截状态
		updates["last_error"] = ""
		updates["last_latency_ms"] = 0
		updates["last_tested_at"] = nil
	}
	if len(updates) == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}
	if err := db.Model(&model.Proxy{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		writeConflictOrInternal(c, err, "代理名已存在")
		return
	}
	s.invalidateProxyCaches(id)
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
		writeInternalError(c, err)
		return
	}
	if used > 0 {
		writeUpstreamError(c, http.StatusConflict,
			"还有 "+itoa(int(used))+" 个渠道在用这个代理，请先改成别的代理或直连", "invalid_request_error")
		return
	}
	// 模型级代理也算引用：只查渠道会漏掉「渠道直连、某个模型单独走代理」的配置，
	// 删掉代理后那个模型要到转发时才失败
	var usedByModel int64
	if err := db.Model(&model.ChannelModel{}).Where("proxy_id = ?", id).Count(&usedByModel).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	if usedByModel > 0 {
		writeUpstreamError(c, http.StatusConflict,
			"还有 "+itoa(int(usedByModel))+" 个模型映射在用这个代理，请先改成别的代理或跟随渠道", "invalid_request_error")
		return
	}
	s.invalidateProxyCaches(id)
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
	cfg, cfgErr := s.proxyConfigOf(row)
	if cfgErr != nil {
		// 解不开密码就没法真正验证这条链路，如实说明而不是拿空配置去拨号
		c.JSON(http.StatusOK, proxyTestView(proxy.TestResult{Error: cfgErr.Error()}))
		return
	}
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
		// 用户名与密码都是三态：nil = 这次没提交这个字段（沿用库里的），
		// 空串 = 用户明确清空了它。原来用户名是值类型，
		// 「清空用户名换成免认证代理」时会被回填成旧用户名，
		// 于是「测通了」但保存后根本用不了
		Username *string `json:"username"`
		Password *string `json:"password"`
		TestURL  string  `json:"test_url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	cfg := proxy.Config{
		Protocol: body.Protocol, Host: strings.TrimSpace(body.Host),
		Port: body.Port, Username: strings.TrimSpace(derefString(body.Username)),
		Password: derefString(body.Password),
	}
	// 编辑已有代理时没提交的凭据沿用库里的：否则用户会看到
	// 「明明没改密码却测不通」
	//
	// 但只在「目标仍然是库里那个服务器」时才沿用。否则任何人都能发
	// {id: 1, host: "attacker.com"} 让服务用代理 #1 的密码去连攻击者的机器 ——
	// 管理接口没有鉴权（设计如此），这等于把库里的代理凭据交出去。
	// 改了地址就让用户重新输一次密码，代价很小。
	if body.ID > 0 && (body.Password == nil || body.Username == nil) {
		var row model.Proxy
		if err := s.deps.Store.DB().First(&row, body.ID).Error; err == nil {
			if sameProxyTarget(row, cfg) {
				stored, _ := s.proxyConfigOf(row)
				if body.Password == nil {
					cfg.Password = stored.Password
				}
				if body.Username == nil {
					cfg.Username = stored.Username
				}
			} else if body.Password == nil {
				writeUpstreamError(c, http.StatusBadRequest,
					"改动代理地址后需要重新填写密码：沿用库里的密码只允许在地址不变时使用",
					"invalid_request_error")
				return
			}
		}
	}
	if err := cfg.Validate(); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	// SSRF 防护：test_url 由调用方指定，且状态码与耗时会被回显 ——
	// 不校验的话它就是一个可用的内网端口扫描器。
	if err := checkTestURL(body.TestURL); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	res := proxy.Test(c.Request.Context(), cfg, body.TestURL, proxyTestTimeout)
	c.JSON(http.StatusOK, proxyTestView(res))
}

// sameProxyTarget 判断请求体里的拨号目标是否与库中那一行一致。
// 只比协议/主机/端口，不涉及凭据。
func sameProxyTarget(row model.Proxy, cfg proxy.Config) bool {
	return strings.EqualFold(strings.TrimSpace(row.Protocol), strings.TrimSpace(cfg.Protocol)) &&
		strings.EqualFold(strings.TrimSpace(row.Host), strings.TrimSpace(cfg.Host)) &&
		row.Port == cfg.Port
}

// checkTestURL 校验代理测试目标。
//
// 空串表示用默认测试地址（gstatic 的 204），那是最常见也最安全的用法。
// 显式指定时必须指向公网 —— 否则这个接口能被用来探测内网。
func checkTestURL(raw string) error {
	u := strings.TrimSpace(raw)
	if u == "" {
		return nil
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("测试地址无法解析: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("测试地址只支持 http/https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("测试地址缺少主机名")
	}
	if _, err := netguard.CheckHost(parsed.Hostname()); err != nil {
		return err
	}
	return nil
}

// proxyConfigOf 把库里的行转成拨号配置（密码在这里解密）。
//
// 解不开密文（换过主密钥）时把原因带出去：原来这里是 `cfg, _ :=` 直接吞掉，
// 于是测试接口拿着一个协议/地址全空的配置去拨号，最后报
// 「不支持的代理协议 ""」—— 用户会去检查协议下拉框，而真正的问题是主密钥。
func (s *Server) proxyConfigOf(p model.Proxy) (proxy.Config, error) {
	return proxy.FromEntity(p, s.deps.Cipher)
}

// invalidateProxyCaches 让转发器丢掉这个代理缓存的客户端。
// 地址、端口、密码、启用状态一变就要调 —— 否则旧连接会继续按老配置拨下去，
// 表现为「配置改了却不生效」。
func (s *Server) invalidateProxyCaches(id uint) {
	if s.deps.Service != nil {
		s.deps.Service.InvalidateProxy(id)
	}
}

// recordProxyTest 把测试结果写回该行。写失败只记日志，不影响本次测试结论。
func (s *Server) recordProxyTest(db *gorm.DB, id uint, res proxy.TestResult) {
	status := "fail"
	if res.OK {
		status = "ok"
	}
	now := time.Now()
	err := db.Model(&model.Proxy{}).Where("id = ?", id).Updates(map[string]any{
		"last_status": status, "last_latency_ms": res.LatencyMs,
		"last_error": res.Error, "last_tested_at": &now,
	}).Error
	if err != nil {
		// 结果已经拿到了，写不进去只说明这一列没更新，不该让测试本身失败 ——
		// 但也不能完全静默：列表上会一直显示上一次的结论，不说一声就无从查起
		s.logger().Warn("写回代理测试结果失败", "proxy_id", id, "err", err)
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
	return checkRefExists(s, id, &model.Proxy{}, "代理")
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
