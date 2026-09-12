package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
)

// registerBackupRoutes 挂载配置备份接口。
//
// 备份的是「配置」而不是「数据」：渠道、模型、绑定、密钥与手工定价。
// 调用日志与统计属于运行数据，随数据库卷一起备份更合适，不做进这里。
func registerBackupRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/backup")
	r.GET("/export", s.exportConfig)
	r.POST("/import", s.importConfig)
}

// channelExport / apiKeyExport 在实体之上补回被 json:"-" 隐藏的敏感字段。
//
// 不能直接序列化实体：Channel.APIKeyEnc 与 APIKey.KeyHash 都带 json:"-"，
// 目的是不把密文泄漏到管理接口，但导出也会被一并静默丢掉——
// 恢复出来的渠道一个都调不通、密钥一个都认不了，而这正是备份最该保住的东西。
// 这里靠外层同名字段遮蔽内层字段，让它们以显式列名出现在备份里。
type channelExport struct {
	model.Channel
	APIKeyEnc string `json:"api_key_enc"`
}

type apiKeyExport struct {
	model.APIKey
	KeyHash string `json:"key_hash"`
}

// proxyExport 与 channelExport 同理：代理密码的 json tag 是 "-"，
// 不显式带出来备份里就没有它。
type proxyExport struct {
	model.Proxy
	PasswordEnc string `json:"password_enc"`
}

// backupBundle 是配置备份的载体。
type backupBundle struct {
	Version    int       `json:"version"`
	App        string    `json:"app"`
	ExportedAt time.Time `json:"exported_at"`
	// SecretFingerprint 是加密主密钥的短指纹。渠道密钥在备份里始终是密文，
	// 只有同一把 RELAY_SECRET 才能解出原文；靠这个指纹可以提前判断能否恢复。
	SecretFingerprint string               `json:"secret_fingerprint"`
	Groups            []model.ChannelGroup `json:"channel_groups"`
	Channels          []channelExport      `json:"channels"`
	// Bindings 就是渠道的模型白名单。老备份里还带着 models/providers 两个数组，
	// 结构体里已经没有了 —— Go 解析时会忽略不认识的字段，所以旧备份仍能导入，
	// 只是其中的 models 不会再生效（绑定的 public_name 已经随白名单落库）。
	Bindings []model.ChannelModel `json:"channel_models"`
	// Templates 字段随模板管理一起下线。旧备份里仍然带着 channel_templates 数组，
	// Go 解析时会忽略不认识的字段：旧备份照样能导入，只是其中的模板不再生效
	// Proxies 里的密码是密文，靠 proxyExport 显式带出来 ——
	// model.Proxy.PasswordEnc 的 json tag 是 "-"，直接序列化会把密码整个丢掉，
	// 恢复出来的代理会变成「没有密码」，而界面上看不出任何异常。
	Proxies  []proxyExport        `json:"proxies"`
	APIKeys  []apiKeyExport       `json:"api_keys"`
	Pricings []model.ModelPricing `json:"pricings"`
	// ManualPricings 是旧备份文件里的字段名，只为能继续读出来
	ManualPricings []model.ModelPricing `json:"manual_pricings"`
}

// secretFingerprint 取主密钥的短哈希，用于判断备份能否在本实例解开。
func secretFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:16]
}

func (s *Server) exportConfig(c *gin.Context) {
	db := s.deps.Store.DB()
	b := backupBundle{
		Version:           1,
		App:               "llm-relay",
		ExportedAt:        time.Now().UTC(),
		SecretFingerprint: secretFingerprint(s.deps.Config.Security.Secret),
	}
	// 逐个取，任一失败都应整体失败——半个备份比没有备份更危险
	steps := []struct {
		name string
		run  func() error
	}{
		{"分组", func() error { return db.Order("id").Find(&b.Groups).Error }},
		{
			"渠道", func() error {
				var rows []model.Channel
				if err := db.Order("id").Find(&rows).Error; err != nil {
					return err
				}
				for _, ch := range rows {
					b.Channels = append(b.Channels, channelExport{Channel: ch, APIKeyEnc: ch.APIKeyEnc})
				}
				return nil
			},
		},
		{"模型白名单", func() error { return db.Order("id").Find(&b.Bindings).Error }},
		{
			"代理", func() error {
				var rows []model.Proxy
				if err := db.Order("id").Find(&rows).Error; err != nil {
					return err
				}
				for _, p := range rows {
					b.Proxies = append(b.Proxies, proxyExport{Proxy: p, PasswordEnc: p.PasswordEnc})
				}
				return nil
			},
		},
		{
			"密钥", func() error {
				var rows []model.APIKey
				if err := db.Order("id").Find(&rows).Error; err != nil {
					return err
				}
				for _, k := range rows {
					b.APIKeys = append(b.APIKeys, apiKeyExport{APIKey: k, KeyHash: k.KeyHash})
				}
				return nil
			},
		},
		{
			// 定价全部手工录入，都是不可再生的数据，必须整体导出
			"模型定价", func() error {
				return db.Order("id").Find(&b.Pricings).Error
			},
		},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			writeUpstreamError(c, http.StatusInternalServerError,
				"导出"+step.name+"失败: "+err.Error(), "internal_error")
			return
		}
	}

	c.Header("Content-Disposition",
		"attachment; filename=llm-relay-backup-"+time.Now().Format("20060102-150405")+".json")
	c.JSON(http.StatusOK, b)
}

// importReport 汇报本次导入实际做了什么，避免「导入成功」但什么都没进去。
type importReport struct {
	Created  map[string]int `json:"created"`
	Skipped  map[string]int `json:"skipped"`
	Warnings []string       `json:"warnings"`
}

func (s *Server) importConfig(c *gin.Context) {
	var b backupBundle
	if err := c.ShouldBindJSON(&b); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "备份文件解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if b.App != "llm-relay" {
		writeUpstreamError(c, http.StatusBadRequest, "这不是 llm-relay 的备份文件", "invalid_request_error")
		return
	}

	report := importReport{Created: map[string]int{}, Skipped: map[string]int{}}
	db := s.deps.Store.DB()

	// 主密钥不同则渠道密钥解不开，必须提前告知而不是等用户发现调用全 401
	local := secretFingerprint(s.deps.Config.Security.Secret)
	if b.SecretFingerprint != "" && b.SecretFingerprint != local {
		report.Warnings = append(report.Warnings,
			"备份来自不同的加密主密钥（备份 "+b.SecretFingerprint+" / 本机 "+local+
				"），渠道密钥无法解密，导入后需要重新填写上游密钥")
	}

	// 按自然键合并：已存在的不覆盖，避免把本机正在用的配置改掉
	groupIDMap := map[uint]uint{}
	for i := range b.Groups {
		gr := b.Groups[i]
		oldID := gr.ID
		var exist model.ChannelGroup
		if err := db.Where("name = ?", gr.Name).First(&exist).Error; err == nil {
			groupIDMap[oldID] = exist.ID
			report.Skipped["分组"]++
			continue
		}
		gr.ID = 0
		if err := db.Create(&gr).Error; err != nil {
			report.Warnings = append(report.Warnings, "分组 "+gr.Name+" 导入失败: "+err.Error())
			continue
		}
		groupIDMap[oldID] = gr.ID
		report.Created["分组"]++
	}

	// 代理同样按名字合并。渠道要引用代理，所以这张映射表先建好
	proxyIDMap := map[uint]uint{}
	for i := range b.Proxies {
		px := b.Proxies[i].Proxy
		// 密文被 json:"-" 挡住过，导入时必须显式写回
		px.PasswordEnc = b.Proxies[i].PasswordEnc
		oldID := px.ID
		var exist model.Proxy
		if err := db.Where("name = ?", px.Name).First(&exist).Error; err == nil {
			proxyIDMap[oldID] = exist.ID
			report.Skipped["代理"]++
			continue
		}
		px.ID = 0
		// 导入出来的代理一律先标成「未测试」：它在本机根本没拨过，
		// 沿用备份里的「正常」会让用户以为已经验证过
		px.LastStatus = "unknown"
		px.LastError = ""
		px.LastLatencyMs = 0
		px.LastTestedAt = nil
		if err := db.Create(&px).Error; err != nil {
			report.Warnings = append(report.Warnings, "代理 "+px.Name+" 导入失败: "+err.Error())
			continue
		}
		proxyIDMap[oldID] = px.ID
		report.Created["代理"]++
	}

	channelIDMap := map[uint]uint{}
	for i := range b.Channels {
		ch := b.Channels[i].Channel
		// 密文被 json:"-" 挡住过，导入时必须显式写回，否则渠道没有密钥
		ch.APIKeyEnc = b.Channels[i].APIKeyEnc
		oldID := ch.ID
		var exist model.Channel
		if err := db.Where("name = ?", ch.Name).First(&exist).Error; err == nil {
			channelIDMap[oldID] = exist.ID
			report.Skipped["渠道"]++
			continue
		}
		ch.ID = 0
		// 映射不到就落到本机默认分组。
		// 原来是「映射不到就保留旧 ID」—— 那个 ID 在本机可能指向另一个分组，
		// 或者根本不存在，渠道会因此静默地不参与路由。
		if mapped, ok := groupIDMap[ch.GroupID]; ok {
			ch.GroupID = mapped
		} else {
			ch.GroupID = defaultGroupID(s)
			report.Warnings = append(report.Warnings,
				"渠道 "+ch.Name+" 的原始分组在本机不存在，已归入默认分组")
		}
		// 代理同样要重新映射。不映射的话，备份里指向代理 #2 的渠道
		// 恢复后会指向本机的 #2 —— 那是另一条线路，流量会从非预期的出口出去，
		// 而界面上只显示一个代理名，看不出问题；映射不到就置 0（直连）并报警，
		// 因为「直连」至少是可见的
		if ch.ProxyID != 0 {
			if mapped, ok := proxyIDMap[ch.ProxyID]; ok {
				ch.ProxyID = mapped
			} else {
				ch.ProxyID = 0
				report.Warnings = append(report.Warnings,
					"渠道 "+ch.Name+" 的出站代理在本机不存在，已改为直连")
			}
		}
		if err := db.Create(&ch).Error; err != nil {
			report.Warnings = append(report.Warnings, "渠道 "+ch.Name+" 导入失败: "+err.Error())
			continue
		}
		channelIDMap[oldID] = ch.ID
		report.Created["渠道"]++
	}

	for i := range b.Bindings {
		bd := b.Bindings[i]
		newCh, ok := channelIDMap[bd.ChannelID]
		if !ok {
			report.Skipped["模型白名单"]++
			continue
		}
		if strings.TrimSpace(bd.PublicName) == "" {
			// 旧备份里的绑定只带 model_id，那份模型清单在新结构里没有对应物，
			// 只能跳过 —— 但我们明确说清楚跳过了什么，不静默丢数据
			report.Warnings = append(report.Warnings,
				"有一条旧格式的模型绑定（只有 model_id）无法转换，已跳过")
			report.Skipped["模型白名单"]++
			continue
		}
		var exist model.ChannelModel
		if err := db.Where("channel_id = ? AND public_name = ?", newCh, bd.PublicName).
			First(&exist).Error; err == nil {
			report.Skipped["模型白名单"]++
			continue
		}
		bd.ID = 0
		bd.ChannelID = newCh
		if strings.TrimSpace(bd.UpstreamName) == "" {
			bd.UpstreamName = bd.PublicName
		}
		// 模型级的出站代理同理：映射不到就回到「跟随渠道」
		if bd.ProxyID != 0 {
			if mapped, ok := proxyIDMap[bd.ProxyID]; ok {
				bd.ProxyID = mapped
			} else {
				bd.ProxyID = 0
				report.Warnings = append(report.Warnings,
					"模型 "+bd.PublicName+" 单独指定的出站代理在本机不存在，已改为跟随渠道")
			}
		}
		if err := db.Create(&bd).Error; err != nil {
			report.Warnings = append(report.Warnings, "模型白名单导入失败: "+err.Error())
			continue
		}
		report.Created["模型白名单"]++
	}

	// 密钥只有哈希，本身无法找回明文；导入后原密钥可直接继续使用
	for i := range b.APIKeys {
		k := b.APIKeys[i].APIKey
		k.KeyHash = b.APIKeys[i].KeyHash
		if strings.TrimSpace(k.KeyHash) == "" {
			report.Warnings = append(report.Warnings,
				"密钥 "+k.Name+" 缺少哈希无法恢复，需要重新签发")
			continue
		}
		var exist model.APIKey
		if err := db.Where("key_hash = ?", k.KeyHash).First(&exist).Error; err == nil {
			report.Skipped["密钥"]++
			continue
		}
		k.ID = 0
		if err := db.Create(&k).Error; err != nil {
			report.Warnings = append(report.Warnings, "密钥 "+k.Name+" 导入失败: "+err.Error())
			continue
		}
		report.Created["密钥"]++
	}

	// 新备份读 pricings，旧备份读 manual_pricings：后者当年只存手工来源，
	// 正好是现在定价的全部
	pricings := b.Pricings
	if len(pricings) == 0 {
		pricings = b.ManualPricings
	}
	for i := range pricings {
		p := pricings[i]
		var exist model.ModelPricing
		if err := db.Where("model_key = ?", p.ModelKey).First(&exist).Error; err == nil {
			report.Skipped["模型定价"]++
			continue
		}
		p.ID = 0
		// 旧备份里没有 multiplier 字段（那时还没有固定倍率），反序列化后是 0。
		// 0 会被 GORM 从 INSERT 里省掉，而这一列是 not null 且没有默认值 ——
		// 不归一的话「恢复旧备份」会直接写不进去
		if p.Multiplier <= 0 {
			p.Multiplier = 1
		}
		if err := db.Create(&p).Error; err != nil {
			report.Warnings = append(report.Warnings, "定价 "+p.ModelKey+" 导入失败: "+err.Error())
			continue
		}
		report.Created["模型定价"]++
	}

	if s.deps.Pricing != nil {
		s.deps.Pricing.Invalidate()
	}

	c.JSON(http.StatusOK, gin.H{
		"created": report.Created, "skipped": report.Skipped, "warnings": report.Warnings,
	})
}
