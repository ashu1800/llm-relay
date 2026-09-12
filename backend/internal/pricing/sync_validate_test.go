package pricing

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// 官方页面改版时，解析结果必须整体拒绝而不是写出错价。
//
// 为什么这么严：官方源与已入库的官方行优先级相同（都是 50），upsert 的跳过条件
// 是 existing.Priority > priority —— 同优先级不跳过，所以错值会直接覆盖正确值，
// 而同步历史仍然显示 ok。下面三种形态来自用真实页面做的变异实验。
func TestParseDeepSeekRejectsBrokenPage(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(string) string
		wantHint string
	}{
		{
			name: "删掉缓存未命中的 OFF-PEAK 行",
			mutate: func(s string) string {
				return strings.Replace(s,
					"<tr><td></td><td>1M INPUT TOKENS(CACHE MISS)</td><td>OFF-PEAK</td><td>$0.15</td><td>$0.66</td></tr>",
					"", 1)
			},
			wantHint: "输入价",
		},
		{
			name: "指标行改名导致价格串列",
			mutate: func(s string) string {
				return strings.Replace(s, "CACHE MISS", "CACHE READ", 1)
			},
			wantHint: "输入价",
		},
		{
			name: "缓存命中价高于输入价（列错位）",
			mutate: func(s string) string {
				return strings.Replace(s, "$0.003", "$1.500", 1)
			},
			wantHint: "缓存命中价",
		},
		{
			name: "峰时倍率离谱",
			mutate: func(s string) string {
				return strings.Replace(s, "<td>$0.3</td>", "<td>$30</td>", 1)
			},
			wantHint: "峰时倍率",
		},
	}

	// 先确认原样本是好的，否则下面的失败说明不了问题
	if _, err := parseDeepSeekTable([]byte(deepseekTableFixture)); err != nil {
		t.Fatalf("原样本本身应当能解析成功，实际报错: %v", err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseDeepSeekTable([]byte(c.mutate(deepseekTableFixture)))
			if err == nil {
				t.Fatal("页面已经改坏，解析却成功了 —— 错价会直接覆盖库里正确的价格")
			}
			if !strings.Contains(err.Error(), c.wantHint) {
				t.Errorf("报错信息应点明 %q，实际: %v", c.wantHint, err)
			}
		})
	}
}

// 校验函数本身的行为：边界值不该被误伤。
func TestValidateOfficialBucketBoundaries(t *testing.T) {
	good := officialBucket{
		offInput: dec("0.15"), offOutput: dec("0.6"), offCache: dec("0.003"),
		peakInput: dec("0.3"), peakOutput: dec("1.2"), peakCache: dec("0.006"),
	}
	if err := validateOfficialBucket("ok", good); err != nil {
		t.Fatalf("正常价格不应被拒: %v", err)
	}

	// 没有缓存价（很多模型不单列）不该被当成错误
	noCache := good
	noCache.offCache = dec("0")
	if err := validateOfficialBucket("nocache", noCache); err != nil {
		t.Fatalf("缺缓存价不应被拒: %v", err)
	}

	// 缓存价恰好等于输入价同样是异常（应当更便宜）
	equalCache := good
	equalCache.offCache = dec("0.15")
	if err := validateOfficialBucket("equal", equalCache); err == nil {
		t.Error("缓存价等于输入价应当被拒")
	}
}
