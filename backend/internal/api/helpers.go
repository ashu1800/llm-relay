package api

import (
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"llm-relay/internal/model"
	"llm-relay/internal/pricing"
)

// parseDecimal 解析金额字符串，拒绝空值与负数。
func parseDecimal(s string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Zero, err
	}
	if d.IsNegative() {
		return decimal.Zero, fmt.Errorf("金额不能为负")
	}
	return d, nil
}

// applyPriceFields 把价格从载荷写到渠道模型行上。
//
// 校验放在保存路径上而不是事后：时段窗口写错（起止相同、格式不对）不会报错、
// 只会永不命中，用户会以为「配了双倍计费却没生效」，那是最难查的一类问题。
//
// 2026-10-27 起单价不再走这里：它已迁到模型级（见 admin_model_prices.go 的
// applyModelPriceFields）。本函数只剩「倍率 + 时段规则」这两件渠道属性。
func applyPriceFields(row *model.ChannelModel, in whitelistItem) error {
	// 倍率归一：不填、填 0 都表示「没配，按 1 算」。
	// 刻意不把 0 当成「免费」——那会让「忘了填」和「故意填 0」变成同一件事
	m := 1.0
	if in.Multiplier != nil {
		m = *in.Multiplier
	}
	row.Multiplier = m
	// 时段规则先原样落到实体，再交给 validatePricingRow 统一规整 ——
	// 共享核只认实体，不认识载荷形态
	row.PeakRules = in.PeakRules

	return validatePricingRow(row)
}

// priceFields 是四个单价字段的规范清单（展示名 + 读取器）。
//
// 校验（validatePricingRow）与清零告警（wipedPrices）都从这里取字段与文案：
// 加第五个价格字段时只改这一处 —— 漏改任何一处都表现为静默失真
// （「界面填了、库里是 0」，或者「清零了但 WARN 没点名」）。
// validatePricingRow 校验并规整**已经落到实体上**的渠道定价属性
// （倍率 + 时段规则；单价已迁到模型级，见 admin_model_prices.go）。
//
// 这是价格不变式的唯一落点，两条写入路径共用：
//   - API 保存路径（applyPriceFields）：字符串载荷先解析到实体，再走这里；
//   - 备份导入路径（validateImportedPricing）：JSON 直接给出 decimal，也走这里。
//
// 抽出来是因为导入路径此前另抄了一份，两处的错误文案已经开始漂移
// （「为负数」vs「不是合法数字」），而这段规则要拦的是「负单价进统计」
// 这种账面失真 —— 两套口径迟早会漏掉同一个边界。
//
// 做了三件事，缺一不可：
//   - 倍率上界：超过 pricing.MaxMultiplier 直接拦下，不做夹取；
//   - 倍率 0 归一成 1：「没配」与「故意填 0」不做区分，引擎对 0 本就等价于
//     没配，归一后库里不留歧义值（免费模型用单价 0 表达）；
//   - 时段规则写回 NormalizeRules 规整后的结果：手编辑过的脏规则（days 带小数、
//     label 超长被截）落库前收干净，前端编辑器读到的与 API 保存的形态一致。
//
// 单价非负那条校验随单价的归属一起搬到了 admin_model_prices.go
// （validateModelPriceRows）：单价不再是渠道属性，规则也该跟着走。
//
// 时段窗口写错（起止相同、格式不对）必须在这里报错而不是放行：它不会报错、
// 只会永不命中，用户会以为「配了双倍计费却没生效」，那是最难查的一类问题。
func validatePricingRow(row *model.ChannelModel) error {
	if row.Multiplier < 0 || row.Multiplier > pricing.MaxMultiplier {
		return fmt.Errorf("固定倍率需要在 0 到 %g 之间（1 表示原价）", pricing.MaxMultiplier)
	}
	if row.Multiplier == 0 {
		row.Multiplier = 1
	}

	rules, err := pricing.NormalizeRules(row.PeakRules)
	if err != nil {
		return err
	}
	row.PeakRules = rules
	return nil
}

// nowUTC 统一用 UTC 落库，避免进程时区差异导致统计错位。
func nowUTC() time.Time { return time.Now().UTC() }

// copyHeader 复制上游响应头，跳过逐跳首部与长度相关头。
func copyHeader(dst, src http.Header) {
	skip := map[string]bool{
		"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true,
		"Content-Length": true, "Upgrade": true, "Proxy-Authenticate": true,
		"Proxy-Authorization": true, "Te": true, "Trailer": true,
	}
	for k, vals := range src {
		if skip[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func fsReadFile(fsys fs.FS, name string) ([]byte, error) {
	return fs.ReadFile(fsys, name)
}

// isAPIPath 判断路径是否属于接口命名空间，用于避免 SPA 回退吞掉 404。
func isAPIPath(p string) bool {
	for _, prefix := range []string{"/api/", "/v1/", "/v1beta/", "/healthz", "/readyz"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// writeUpstreamError 以 OpenAI 兼容的错误结构返回。
func writeUpstreamError(c *gin.Context, status int, message, errType string) {
	if status <= 0 {
		status = http.StatusBadGateway
	}
	c.JSON(status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    status,
		},
	})
}
