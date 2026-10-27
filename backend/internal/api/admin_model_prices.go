package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"llm-relay/internal/model"
)

// 模型级定价的管理接口（2026-10-27 从渠道白名单里拆出来）。
//
// 为什么单独一个文件：单价的归属变了 —— 它不再是「渠道 × 模型」的属性，
// 而是模型本身的属性；渠道只保留倍率。写入规则（非负、两币种独立）
// 与渠道那套（倍率区间、时段规则）互不相干，混在一个文件里只会让
// 两边都难读。
//
// 接口形态刻意做成「整表读 + 单条写」：
//   - GET  /admin/model-prices          读全表（页面一次拿完，量级几十到几百）
//   - PUT  /admin/model-prices/:name    按模型名 upsert（一条一行地改）
//   - DELETE /admin/model-prices/:name  删掉一条（回到「未定价」）
//
// 不做批量提交：模型价是按模型独立维护的，一次改一个才是真实用法；
// 批量接口会带来「一半成功一半失败」的中间态，而价格写坏的表现是
// 账单金额不对，事后极难发现。

// modelPricePayload 是单条模型价的写入载荷。
//
// 金额用字符串传：JSON 浮点会把 0.15 变成 0.14999999999999999，
// 而单价要落进 numeric(18,8) 的列里。空串表示 0（该档不收费）。
type modelPricePayload struct {
	ModelName          string `json:"model_name"`
	USDInputPer1M      string `json:"usd_input_per_1m"`
	USDOutputPer1M     string `json:"usd_output_per_1m"`
	USDCacheReadPer1M  string `json:"usd_cache_read_per_1m"`
	USDCacheWritePer1M string `json:"usd_cache_write_per_1m"`
	RMBInputPer1M      string `json:"rmb_input_per_1m"`
	RMBOutputPer1M     string `json:"rmb_output_per_1m"`
	RMBCacheReadPer1M  string `json:"rmb_cache_read_per_1m"`
	RMBCacheWritePer1M string `json:"rmb_cache_write_per_1m"`
}

// listModelPrices 返回全部模型价。
//
// 同时附带「哪些模型名出现在渠道白名单里但没配价」：漏配的后果是
// 这笔调用被记成 0 元，账面上完全看不出异常，只能靠界面点名。
func (s *Server) listModelPrices(c *gin.Context) {
	db := s.deps.Store.DB()
	var rows []model.ModelPrice
	if err := db.Order("model_name").Find(&rows).Error; err != nil {
		writeInternalError(c, err)
		return
	}

	// 白名单里出现过的模型名 ∪ 已定价的模型名：前者是「该配价的」，
	// 后者是「配了但可能已经没渠道在用」—— 两者都要在页面上看得见。
	var whitelistNames []string
	if err := db.Model(&model.ChannelModel{}).
		Where("enabled = ?", true).
		Distinct().Order("public_name").
		Pluck("public_name", &whitelistNames).Error; err != nil {
		writeInternalError(c, err)
		return
	}

	// 渠道币种分布：界面要提示「这个模型只配了美元价，但有人民币渠道在用」。
	type chanCurrency struct {
		ChannelID uint
		Currency  string
	}
	var cc []chanCurrency
	// 列别名必须写成 channel_id：结构体字段是 ChannelID，GORM 按
	// 「channel_id」映射。Select("id, currency") 拿到的字段名不匹配，
	// ChannelID 恒为 0 —— 所有渠道都落到 currencyOf[0]，币种键变成空串
	// （实测踩过：接口返回 {"":true}，界面上「渠道币种」提示全错，
	// 而且不会报错，只是提示永远不对）。
	if err := db.Model(&model.Channel{}).Select("id AS channel_id, currency").Find(&cc).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	currencyOf := map[uint]string{}
	for _, r := range cc {
		cur := model.CurrencyUSD
		if v, ok := model.NormalizeCurrency(r.Currency); ok {
			cur = v
		}
		currencyOf[r.ChannelID] = cur
	}
	// 模型名 -> 用到它的渠道币种（去重后的数组）
	//
	// 输出 []string 而不是 map[string]bool：前端要回答的是「这个模型在哪些
	// 币种的渠道里出现过」，按集合成员判断就够了。曾经返回的是
	// map[string]map[string]bool，前端按 string[] 调 .includes() 直接抛
	// TypeError（打开编辑弹窗就崩），而两侧都不会报错 —— 契约形状不一致
	// 这类问题在类型检查里看不出来，只能靠约定。
	//
	// 顺序按 SupportedCurrencies 定：它同时是下拉框与图例的顺序，
	// 界面上的「美元 / 人民币」不该在两次刷新之间自己换位置。
	type cm struct {
		ChannelID  uint
		PublicName string
	}
	var cms []cm
	if err := db.Model(&model.ChannelModel{}).Select("channel_id, public_name").Find(&cms).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	usedSet := map[string]map[string]bool{}
	for _, r := range cms {
		if usedSet[r.PublicName] == nil {
			usedSet[r.PublicName] = map[string]bool{}
		}
		usedSet[r.PublicName][currencyOf[r.ChannelID]] = true
	}
	usedCurrencies := sortedCurrencyLists(usedSet)

	priced := map[string]bool{}
	for _, r := range rows {
		priced[r.ModelName] = true
	}
	unpriced := make([]string, 0)
	for _, name := range whitelistNames {
		if !priced[name] {
			unpriced = append(unpriced, name)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"items": rows,
		"total": len(rows),
		// 白名单里有、但没定价的模型（前端把它们也列出来，标「未定价」）
		"unpriced_models": unpriced,
		// 模型 -> 它在渠道里实际用到的币种，供界面提示「另一档还没填」
		"used_currencies": usedCurrencies,
	})
}

// sortedCurrencyLists 把「模型 -> 币种集合」摊平成「模型 -> 币种数组」。
//
// 抽成纯函数是因为它的输出形状是**跨语言的契约**：前端按 string[] 调
// .includes()，而 Go 侧若返回 map[string]bool，序列化成 {"CNY":true} 后
// 前端会抛 TypeError（打开编辑弹窗就崩）。两侧代码各自都合法、
// 类型检查也看不出来，只能靠一条针对形状的断言钉住。
//
// 顺序按 SupportedCurrencies 而不是 map 遍历顺序：它同时是下拉框与图例的
// 顺序，界面上的「美元 / 人民币」不该在两次刷新之间自己换位置。
func sortedCurrencyLists(used map[string]map[string]bool) map[string][]string {
	out := make(map[string][]string, len(used))
	for name, set := range used {
		list := make([]string, 0, len(set))
		for _, c := range model.SupportedCurrencies {
			if set[c] {
				list = append(list, c)
			}
		}
		out[name] = list
	}
	return out
}

// upsertModelPrice 按模型名写入（或更新）一条模型价。
func (s *Server) upsertModelPrice(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		writeBadRequest(c, "模型名不能为空")
		return
	}
	var p modelPricePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeBadRequest(c, "请求体格式不正确")
		return
	}
	// URL 里的模型名是权威的：请求体里的 model_name 只在校验时参考，
	// 避免「路径说 A、体里说 B」时写到哪个都不对。
	p.ModelName = name

	row, errMsg := buildModelPrice(p)
	if errMsg != "" {
		writeBadRequest(c, errMsg)
		return
	}

	db := s.deps.Store.DB()
	// 按模型名 upsert：ModelName 上有唯一索引，冲突即更新。
	// SQL 本体在 model.UpsertModelPrice（列名只在那里出现一次）——
	// 这里此前手抄了 8 个列名，与备份导入、启动迁移各抄一份，共五处。
	if err := model.UpsertModelPrice(db, row); err != nil {
		writeInternalError(c, err)
		return
	}
	s.invalidatePricing()

	// 回读整行：让前端拿到落库后的真实值（而不是它提交的那份）
	var saved model.ModelPrice
	if err := db.Where("model_name = ?", row.ModelName).First(&saved).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "item": saved})
}

// deleteModelPrice 删除一条模型价（该模型回到「未定价」，费用记 0）。
func (s *Server) deleteModelPrice(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		writeBadRequest(c, "模型名不能为空")
		return
	}
	db := s.deps.Store.DB()
	res := db.Where("model_name = ?", name).Delete(&model.ModelPrice{})
	if res.Error != nil {
		if errors.Is(res.Error, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
				"code": http.StatusNotFound, "message": "这个模型没有配价", "type": "not_found_error",
			}})
			return
		}
		writeInternalError(c, res.Error)
		return
	}
	s.invalidatePricing()
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": res.RowsAffected})
}

// buildModelPrice 把字符串载荷解析成实体，返回 (实体, 错误文案)。
// 错误文案为空表示通过。
//
// 校验规则集中在这里，与渠道那套分开：
//   - 八个单价各自非负（parseDecimal 已拒绝负数）；
//   - 数字格式必须能解析成 decimal（不静默当 0 —— 那会让「填错」和「不收费」
//     变成同一件事）；
//   - 同一币种的四档**不强制**至少填一个：允许一个模型只有输入价
//     （很多中转站只按输入计价），缺的档按 0 算。
func buildModelPrice(p modelPricePayload) (model.ModelPrice, string) {
	row := model.ModelPrice{ModelName: p.ModelName}
	fields := []struct {
		label string
		raw   string
		dst   *decimal.Decimal
	}{
		{"美元输入单价", p.USDInputPer1M, &row.USDInputPer1M},
		{"美元输出单价", p.USDOutputPer1M, &row.USDOutputPer1M},
		{"美元缓存读单价", p.USDCacheReadPer1M, &row.USDCacheReadPer1M},
		{"美元缓存写单价", p.USDCacheWritePer1M, &row.USDCacheWritePer1M},
		{"人民币输入单价", p.RMBInputPer1M, &row.RMBInputPer1M},
		{"人民币输出单价", p.RMBOutputPer1M, &row.RMBOutputPer1M},
		{"人民币缓存读单价", p.RMBCacheReadPer1M, &row.RMBCacheReadPer1M},
		{"人民币缓存写单价", p.RMBCacheWritePer1M, &row.RMBCacheWritePer1M},
	}
	for _, f := range fields {
		raw := strings.TrimSpace(f.raw)
		if raw == "" {
			*f.dst = decimal.Zero
			continue
		}
		d, err := parseDecimal(raw)
		if err != nil {
			return model.ModelPrice{}, fmt.Sprintf("%s 不是合法金额（%s）", f.label, raw)
		}
		*f.dst = d
	}
	return row, ""
}

// registerModelPriceRoutes 挂载模型定价的管理路由。
func registerModelPriceRoutes(admin *gin.RouterGroup, s *Server) {
	g := admin.Group("/model-prices")
	g.GET("", s.listModelPrices)
	// 模型名可能含斜杠以外的任意字符（claude-3-5-sonnet、gpt-5.1 等），
	// 用 :name 单段即可；带斜杠的模型名在真实数据里不存在，
	// 真出现了走 catch-all 反而会与别的路由抢匹配。
	g.PUT("/:name", s.upsertModelPrice)
	g.DELETE("/:name", s.deleteModelPrice)
}
