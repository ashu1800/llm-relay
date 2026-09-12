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

// buildPricingRow 把请求载荷转成定价实体。
func buildPricingRow(p pricingPayload) (model.ModelPricing, error) {
	var row model.ModelPricing
	if strings.TrimSpace(p.ModelKey) == "" {
		return row, fmt.Errorf("model_key 必填")
	}
	row.ModelKey = strings.TrimSpace(p.ModelKey)
	row.MatchType = orDefault(p.MatchType, "exact")
	row.PeakRules = p.PeakRules

	fields := []struct {
		name string
		raw  string
		dst  *decimal.Decimal
	}{
		{"input_per_1m", p.InputPer1M, &row.InputPer1M},
		{"output_per_1m", p.OutputPer1M, &row.OutputPer1M},
		{"cache_read_per_1m", p.CacheReadPer1M, &row.CacheReadPer1M},
		{"cache_write_per_1m", p.CacheWritePer1M, &row.CacheWritePer1M},
	}
	for _, f := range fields {
		if strings.TrimSpace(f.raw) == "" {
			continue
		}
		d, err := parseDecimal(f.raw)
		if err != nil {
			return row, fmt.Errorf("%s 不是合法数字", f.name)
		}
		*f.dst = d
	}
	return row, nil
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
