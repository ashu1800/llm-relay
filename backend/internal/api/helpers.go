package api

import (
	"encoding/json"
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
func applyPriceFields(row *model.ChannelModel, in whitelistItem) error {
	fields := []struct {
		name string
		raw  string
		dst  *decimal.Decimal
	}{
		{"输入单价", in.InputPer1M, &row.InputPer1M},
		{"输出单价", in.OutputPer1M, &row.OutputPer1M},
		{"缓存读单价", in.CacheReadPer1M, &row.CacheReadPer1M},
		{"缓存写单价", in.CacheWritePer1M, &row.CacheWritePer1M},
	}
	for _, f := range fields {
		// 空字符串就是 0：白名单是整表提交的，不填即表示这个模型不算钱
		if strings.TrimSpace(f.raw) == "" {
			*f.dst = decimal.Zero
			continue
		}
		d, err := parseDecimal(f.raw)
		if err != nil {
			return fmt.Errorf("%s 不是合法数字（%s）", f.name, strings.TrimSpace(f.raw))
		}
		*f.dst = d
	}

	// 倍率归一：不填、填 0 都表示「没配，按 1 算」。
	// 刻意不把 0 当成「免费」——那会让「忘了填」和「故意填 0」变成同一件事
	m := 1.0
	if in.Multiplier != nil {
		m = *in.Multiplier
	}
	if m < 0 || m > pricing.MaxMultiplier {
		return fmt.Errorf("固定倍率需要在 0 到 %g 之间（1 表示原价）", pricing.MaxMultiplier)
	}
	if m == 0 {
		m = 1
	}
	row.Multiplier = m

	rules, err := pricing.NormalizeRules(in.PeakRules)
	if err != nil {
		return err
	}
	row.PeakRules = rules
	return nil
}

// nowUTC 统一用 UTC 落库，避免容器时区差异导致统计错位。
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

func trimLeadingSlash(p string) string {
	return strings.TrimPrefix(p, "/")
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

// defaultErrorBody 生成 OpenAI 结构的错误响应体。
func defaultErrorBody(status int, message string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "upstream_error", "code": status},
	})
	return raw
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
