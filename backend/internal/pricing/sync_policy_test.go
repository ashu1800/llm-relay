package pricing

import (
	"strings"
	"testing"
)

// 写入策略的两种键形态要分清。
func TestLiteLLMEntryPolicy(t *testing.T) {
	cases := []struct {
		key        string
		providerID uint
		wantSelf   bool
		wantBare   bool
		why        string
	}{
		{"deepseek/deepseek-v4-flash", 2, true, true, "已接入模型商：全名与派生裸名都写"},
		{"tencent/deepseek-v4-flash", 0, true, false, "未接入：全名保留，裸名不写（否则会盖掉 DeepSeek 的价）"},
		{"gpt-4o", 1, true, false, "无前缀且已接入：键本身就是裸名，写"},
		{"command-r-plus", 0, false, false, "无前缀且未接入：不写，否则当轮就被清理"},
		{"", 0, false, false, "空键"},
		{"/leading", 0, false, false, "以斜杠开头的畸形键按裸名处理"},
	}
	for _, c := range cases {
		self, bare := litellmEntryPolicy(c.key, c.providerID)
		if self != c.wantSelf || bare != c.wantBare {
			t.Errorf("key=%q pid=%d 得到 (self=%v bare=%v)，期望 (self=%v bare=%v) —— %s",
				c.key, c.providerID, self, bare, c.wantSelf, c.wantBare, c.why)
		}
	}
}

// 不变式：任何会被写出的裸名条目，providerID 必须 > 0。
//
// 破坏它的后果不是报错，而是每轮同步「先写入 N 条、紧接着 pruneOrphanBareNames
// 又清理 N 条」—— 数据总数不变，但同步历史里永远显示「新增 N」，
// 看起来像价格表在持续膨胀。实测就是每轮空转 341 条。
func TestLiteLLMEntryPolicyNeverEmitsUnownedBareNames(t *testing.T) {
	keys := []string{"a/b", "a", "x/y/z", "gpt-4o", "deepseek/deepseek-v4-flash", "command-r-plus"}
	for _, key := range keys {
		for _, pid := range []uint{0, 1, 2} {
			self, bare := litellmEntryPolicy(key, pid)
			if self && !strings.Contains(key, "/") && pid == 0 {
				t.Errorf("key=%q pid=%d：会写出无主的裸名，当轮即被清理", key, pid)
			}
			if bare && pid == 0 {
				t.Errorf("key=%q pid=%d：会写出无主的派生裸名，当轮即被清理", key, pid)
			}
		}
	}
}
