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
// 备份的是「配置」而不是「数据」：渠道、模型、绑定、模板、密钥与手工定价。
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

// backupBundle 是配置备份的载体。
type backupBundle struct {
	Version    int       `json:"version"`
	App        string    `json:"app"`
	ExportedAt time.Time `json:"exported_at"`
	// SecretFingerprint 是加密主密钥的短指纹。渠道密钥在备份里始终是密文，
	// 只有同一把 RELAY_SECRET 才能解出原文；靠这个指纹可以提前判断能否恢复。
	SecretFingerprint string                  `json:"secret_fingerprint"`
	Providers         []model.Provider        `json:"providers"`
	Groups            []model.ChannelGroup    `json:"channel_groups"`
	Channels          []channelExport         `json:"channels"`
	Models            []model.Model           `json:"models"`
	Bindings          []model.ChannelModel    `json:"channel_models"`
	Templates         []model.ChannelTemplate `json:"channel_templates"`
	APIKeys           []apiKeyExport          `json:"api_keys"`
	ManualPricings    []model.ModelPricing    `json:"manual_pricings"`
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
		{"模型商", func() error { return db.Order("id").Find(&b.Providers).Error }},
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
		{"模型", func() error { return db.Order("id").Find(&b.Models).Error }},
		{"模型绑定", func() error { return db.Order("id").Find(&b.Bindings).Error }},
		{"渠道模板", func() error { return db.Order("id").Find(&b.Templates).Error }},
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
			// 只导手工定价：自动同步的来源重新同步即可，导出它们只会让备份文件膨胀
			"手工定价", func() error {
				return db.Where("source = ?", "manual").Order("id").Find(&b.ManualPricings).Error
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
	for i := range b.Providers {
		p := b.Providers[i]
		var exist model.Provider
		if err := db.Where("code = ?", p.Code).First(&exist).Error; err == nil {
			report.Skipped["模型商"]++
			continue
		}
		p.ID = 0
		if err := db.Create(&p).Error; err != nil {
			report.Warnings = append(report.Warnings, "模型商 "+p.Code+" 导入失败: "+err.Error())
			continue
		}
		report.Created["模型商"]++
	}

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
		if err := db.Create(&ch).Error; err != nil {
			report.Warnings = append(report.Warnings, "渠道 "+ch.Name+" 导入失败: "+err.Error())
			continue
		}
		channelIDMap[oldID] = ch.ID
		report.Created["渠道"]++
	}

	modelIDMap := map[uint]uint{}
	for i := range b.Models {
		m := b.Models[i]
		oldID := m.ID
		var exist model.Model
		if err := db.Where("public_name = ?", m.PublicName).First(&exist).Error; err == nil {
			modelIDMap[oldID] = exist.ID
			report.Skipped["模型"]++
			continue
		}
		m.ID = 0
		if err := db.Create(&m).Error; err != nil {
			report.Warnings = append(report.Warnings, "模型 "+m.PublicName+" 导入失败: "+err.Error())
			continue
		}
		modelIDMap[oldID] = m.ID
		report.Created["模型"]++
	}

	for i := range b.Bindings {
		bd := b.Bindings[i]
		newCh, ok1 := channelIDMap[bd.ChannelID]
		newMd, ok2 := modelIDMap[bd.ModelID]
		if !ok1 || !ok2 {
			report.Skipped["模型绑定"]++
			continue
		}
		var exist model.ChannelModel
		if err := db.Where("channel_id = ? AND model_id = ?", newCh, newMd).
			First(&exist).Error; err == nil {
			report.Skipped["模型绑定"]++
			continue
		}
		bd.ID = 0
		bd.ChannelID = newCh
		bd.ModelID = newMd
		if err := db.Create(&bd).Error; err != nil {
			report.Warnings = append(report.Warnings, "模型绑定导入失败: "+err.Error())
			continue
		}
		report.Created["模型绑定"]++
	}

	for i := range b.Templates {
		t := b.Templates[i]
		var exist model.ChannelTemplate
		if err := db.Where("name = ?", t.Name).First(&exist).Error; err == nil {
			report.Skipped["渠道模板"]++
			continue
		}
		t.ID = 0
		if err := db.Create(&t).Error; err != nil {
			report.Warnings = append(report.Warnings, "模板 "+t.Name+" 导入失败: "+err.Error())
			continue
		}
		report.Created["渠道模板"]++
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

	for i := range b.ManualPricings {
		p := b.ManualPricings[i]
		var exist model.ModelPricing
		if err := db.Where("model_key = ? AND source = ?", p.ModelKey, p.Source).
			First(&exist).Error; err == nil {
			report.Skipped["手工定价"]++
			continue
		}
		p.ID = 0
		if err := db.Create(&p).Error; err != nil {
			report.Warnings = append(report.Warnings, "定价 "+p.ModelKey+" 导入失败: "+err.Error())
			continue
		}
		report.Created["手工定价"]++
	}

	if s.deps.Pricing != nil {
		s.deps.Pricing.Invalidate()
	}

	c.JSON(http.StatusOK, gin.H{
		"created": report.Created, "skipped": report.Skipped, "warnings": report.Warnings,
	})
}
