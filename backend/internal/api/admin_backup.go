package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/pricing"
)

// maxImportBodyBytes 是配置导入的请求体上限。
//
// 比常规管理接口（8 MB）宽，因为备份里含全部分组、渠道、模型白名单与价格、
// 密钥与代理；但仍要有上限 —— 无界的 JSON 解析可以被用来打满内存。
const maxImportBodyBytes = 64 << 20

// registerBackupRoutes 挂载配置备份接口。
//
// 备份的是「配置」而不是「数据」：渠道、模型（含各自的价格）、绑定、代理与密钥。
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
	// 与渠道密钥同理：KeyEnc 的 json tag 是 "-"，不显式带出来备份里就没有明文，
	// 恢复之后「查看密钥」全是「无法找回」
	KeyEnc string `json:"key_enc"`
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
	Proxies []proxyExport  `json:"proxies"`
	APIKeys []apiKeyExport `json:"api_keys"`
	// Pricings 是**只读**的旧字段：价格已改随渠道模型配置（见 model.ChannelModel），
	// 新备份不再导出它。保留定义只为能把老备份里的价格捞出来应用到对应的渠道模型上，
	// 否则「导入旧备份」会静默丢掉全部价格。
	Pricings       []legacyPricing `json:"pricings"`
	ManualPricings []legacyPricing `json:"manual_pricings"`
}

// legacyPricing 是旧备份里定价条目的形状（model_pricings 表已下线）。
// 字段名与当时的实体一致，这样老备份原样解析得出来。
type legacyPricing struct {
	ModelKey        string          `json:"model_key"`
	Currency        string          `json:"currency"`
	InputPer1M      decimal.Decimal `json:"input_per_1m"`
	OutputPer1M     decimal.Decimal `json:"output_per_1m"`
	CacheReadPer1M  decimal.Decimal `json:"cache_read_per_1m"`
	CacheWritePer1M decimal.Decimal `json:"cache_write_per_1m"`
	PeakRules       model.JSONList  `json:"peak_rules"`
	Multiplier      float64         `json:"multiplier"`
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
				// 按 (分组, 优先级序号) 导出：weight 是组内的故障转移顺序，
				// 导入时按文件顺序依次追加就能还原同一套顺序。
				// 只按 id 排会让备份→恢复把优先级顺序打乱（顺序是拿拖拽调出来的，
				// 丢了没法从 id 推回去）
				if err := db.Order("group_id, weight, id").Find(&rows).Error; err != nil {
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
					b.APIKeys = append(b.APIKeys, apiKeyExport{APIKey: k, KeyHash: k.KeyHash, KeyEnc: k.KeyEnc})
				}
				return nil
			},
		},
		// 「模型定价」这一步已取消：价格随渠道的模型白名单一起导出
		// （见 channelExport.Models），单独再导一份只会出现两份真相。
		// b.Pricings 保持为空数组，老版本的导入逻辑读到空值不会报错。
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
	// 导入需要比常规管理接口更大的额度：备份包含全部分组、渠道、
	// 模型白名单（含价格）、密钥与代理，条数多时 JSON 会明显变大。
	// 放宽到 64 MB 而不是取消上限 —— 无界才是问题所在。
	if c.Request.Body != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxImportBodyBytes)
	}
	var b backupBundle
	if err := c.ShouldBindJSON(&b); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "备份文件解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	if b.App != "llm-relay" {
		writeUpstreamError(c, http.StatusBadRequest, "这不是 llm-relay 的备份文件", "invalid_request_error")
		return
	}
	// 版本检查：当前导出的是 version 1。更大的版本说明备份来自**更新的程序**
	//（比如降级部署后导入了升级时的备份），结构体字段对不上会静默丢字段 ——
	// 按不认识的字段整体忽略的 Go 语义，丢的还是最关键的那些。
	// 明确拒掉比「导入成功但缺了一半」可见得多。
	if b.Version > 1 {
		writeUpstreamError(c, http.StatusBadRequest,
			fmt.Sprintf("备份版本（v%d）比当前程序（v1）新，请先把程序升级到导出该备份的版本再导入", b.Version),
			"invalid_request_error")
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
		// 策略也要收敛：备份可能是「加权随机」还在的时候导出的，直接落库会
		// 让分组表单显示空白（下拉里已经没有这一项了）。启动时的迁移虽然也
		// 会收拾它，但那要等到下次重启，中间这段时间界面上是坏的
		gr.Strategy = normalizeStrategy(gr.Strategy)
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
		// 老备份里没有 currency 字段（那时全站按美元口径算），空值落到列默认值
		// 虽然结果一样，但显式写出来，免得下一个人以为这里漏了一件事
		if ch.Currency == "" {
			ch.Currency = model.CurrencyUSD
		}
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
		// 本机也可能压根没有默认分组（用户把它删了，见 store.Seed）：
		// 那就落到 id 最小的分组，但**落到哪个分组必须写进报告** ——
		// 悄悄改变归属正是这段代码一开始要避免的事
		if mapped, ok := groupIDMap[ch.GroupID]; ok {
			ch.GroupID = mapped
		} else if gid, ok := defaultGroupID(s); ok {
			ch.GroupID = gid
			report.Warnings = append(report.Warnings,
				"渠道 "+ch.Name+" 的原始分组在本机不存在，已归入默认分组")
		} else {
			var fallback model.ChannelGroup
			if err := db.Order("id").First(&fallback).Error; err != nil {
				report.Warnings = append(report.Warnings,
					"渠道 "+ch.Name+" 的原始分组在本机不存在，本机也没有任何分组，该渠道已跳过")
				continue
			}
			ch.GroupID = fallback.ID
			report.Warnings = append(report.Warnings,
				"渠道 "+ch.Name+" 的原始分组在本机不存在，本机也没有默认分组，已归入「"+fallback.Name+"」")
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
		// 权重不能照抄文件里的值：它是组内优先级序号，且库上有
		// (group_id, weight) 唯一索引。备份是逐条 Create 的，文件里若有多条
		// 同序号（老备份一定有，那时同分组同权重是常态），第二条就会撞索引、
		// 整条渠道导入失败。改为逐条排到目标分组末尾 —— 导出时已按
		// (group_id, weight) 排序，所以文件顺序天然还原出原来的优先级顺序。
		if err := db.Transaction(func(tx *gorm.DB) error {
			w, err := appendChannelToGroup(tx, ch.GroupID)
			if err != nil {
				return err
			}
			ch.Weight = w
			return tx.Create(&ch).Error
		}); err != nil {
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
		// 价格与倍率要过与 API 保存路径同一套校验：备份文件可以手工编辑，
		// 负单价一旦进来就是负费用进统计；时段规则同理（永不命中的窗口
		// 会让人以为配好了双倍计费）。校验不过的整条跳过并点名，不静默
		if err := validateImportedPricing(&bd); err != nil {
			report.Warnings = append(report.Warnings,
				"模型 "+bd.PublicName+" 的价格配置不合法，已跳过导入: "+err.Error())
			report.Skipped["模型白名单"]++
			continue
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

	// 鉴权只认哈希，所以没有明文也能用；密文一并带回去是为了
	// 「查看密钥」在恢复后依然可用（老备份里没有这个字段，导入后就是看不到明文）
	for i := range b.APIKeys {
		k := b.APIKeys[i].APIKey
		k.KeyHash = b.APIKeys[i].KeyHash
		k.KeyEnc = b.APIKeys[i].KeyEnc
		if strings.TrimSpace(k.KeyHash) == "" {
			report.Warnings = append(report.Warnings,
				"密钥 "+k.Name+" 缺少哈希无法恢复，需要重新签发")
			continue
		}
		// 分组白名单必须跟着映射：条目存的是**备份库**的分组 ID（更老的备份
		// 里是名字），与本机同名分组的 ID 几乎一定不同 —— 不映射的话，
		// 恢复出来的白名单会指向本机的另一个分组（静默指错，比悬空更糟：
		// 限制还在、限的是别人）或不存在的分组（一调用就 403）。
		// 数字条目只认 groupIDMap：备份里的 ID 3 在本机往往是别的分组，
		// 拿它直接查本库等于主动指错。名字条目（老备份）按名字查。
		// 都映射不到的条目丢弃并写进报告 —— 限制虽然变宽，但报告点名了它，
		// 比「看起来恢复成功、一调用就 403」可见得多。
		if len(k.AllowedGroups) > 0 {
			refs := model.StringList{}
			for _, raw := range k.AllowedGroups {
				item := strings.TrimSpace(raw)
				if item == "" {
					continue
				}
				mapped := false
				if n, err := strconv.ParseUint(item, 10, 32); err == nil {
					if nid, ok := groupIDMap[uint(n)]; ok {
						refs = append(refs, strconv.FormatUint(uint64(nid), 10))
						mapped = true
					}
				} else {
					var g model.ChannelGroup
					if err := db.Where("name = ?", item).First(&g).Error; err == nil {
						refs = append(refs, strconv.FormatUint(uint64(g.ID), 10))
						mapped = true
					}
				}
				if !mapped {
					report.Warnings = append(report.Warnings,
						"密钥 "+k.Name+" 的分组白名单条目「"+item+"」在本机没有对应分组，该条限制已移除")
				}
			}
			k.AllowedGroups = refs
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
	// 老备份里的价格：按模型名写到对应的渠道模型上。
	// 新备份没有这个数组，这段对它是空转。
	for i := range pricings {
		p := pricings[i]
		// 旧备份里没有 multiplier 字段（那时还没有固定倍率），反序列化后是 0；
		// 0 的语义是「没配」，由引擎归一到 1
		res := db.Model(&model.ChannelModel{}).
			Where("public_name = ?", p.ModelKey).
			Where("COALESCE(input_per1_m,0) = 0 AND COALESCE(output_per1_m,0) = 0").
			Where("COALESCE(cache_read_per1_m,0) = 0 AND COALESCE(cache_write_per1_m,0) = 0").
			Updates(map[string]any{
				"input_per1_m": p.InputPer1M, "output_per1_m": p.OutputPer1M,
				"cache_read_per1_m": p.CacheReadPer1M, "cache_write_per1_m": p.CacheWritePer1M,
				"multiplier": p.Multiplier, "peak_rules": p.PeakRules,
			})
		if res.Error != nil {
			report.Warnings = append(report.Warnings, "定价 "+p.ModelKey+" 导入失败: "+res.Error.Error())
			continue
		}
		if res.RowsAffected == 0 {
			// 没有渠道在用这个模型（或它已经有价了）：如实说明，
			// 不要让人以为价格恢复成功了
			report.Warnings = append(report.Warnings,
				"备份里的定价 "+p.ModelKey+" 没有对应的渠道模型（或该模型已有价格），已跳过")
			report.Skipped["模型定价"]++
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

// validateImportedPricing 校验并规整备份导入的模型价格，与 API 保存路径
// （applyPriceFields）同一套口径。
//
// 备份文件是 JSON，可以手工编辑：负单价一旦落库就是负费用进统计，
// 看板上「省了钱」的假象比没有统计更糟。API 路径早有校验，导入路径此前
// 绕过了它。校验之外还做两步归一（与 API 路径一致）：
//   - 倍率 0 归一成 1 ——「没配」与「故意填 0」不做区分，引擎对 0 本就
//     等价于没配，归一后库里不留歧义值；
//   - 时段规则写回 NormalizeRules 规整后的结果 —— 手编辑过的脏规则
//     （days 带小数、label 超长被截）落库前收干净，前端编辑器读到
//     的与 API 保存的形态一致。
func validateImportedPricing(bd *model.ChannelModel) error {
	for name, d := range map[string]decimal.Decimal{
		"输入单价": bd.InputPer1M, "输出单价": bd.OutputPer1M,
		"缓存读单价": bd.CacheReadPer1M, "缓存写单价": bd.CacheWritePer1M,
	} {
		if d.IsNegative() {
			return fmt.Errorf("%s为负数", name)
		}
	}
	if bd.Multiplier < 0 || bd.Multiplier > pricing.MaxMultiplier {
		return fmt.Errorf("固定倍率超出 0 到 %g 的范围", pricing.MaxMultiplier)
	}
	if bd.Multiplier == 0 {
		bd.Multiplier = 1
	}
	rules, err := pricing.NormalizeRules(bd.PeakRules)
	if err != nil {
		return err
	}
	bd.PeakRules = rules
	return nil
}
