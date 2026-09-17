package relay

import (
	"strings"
	"testing"
)

// TruncateRunes 是超长错误体的统一出口（日志列 + 客户端提示），
// 两个性质必须有测试锁住：按字符数截（多字节字符不切坏）、短串原样返回。
func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"短串原样返回", "上游返回 429", 800, "上游返回 429"},
		{"等于上限原样返回", "abcdef", 6, "abcdef"},
		{"超长英文截断加省略号", "abcdef", 3, "abc…"},
		{"中文按字符数不按字节", strings.Repeat("错", 2000), 1000, strings.Repeat("错", 1000) + "…"},
		{"中英混排", "错" + strings.Repeat("a", 2000), 500, "错" + strings.Repeat("a", 499) + "…"},
		{"零上限得空串", "abc", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TruncateRunes(c.in, c.limit)
			if got != c.want {
				t.Fatalf("TruncateRunes(len=%d, limit=%d) 长度 got=%d want=%d（首 16 字符 got=%q want=%q）",
					len(c.in), c.limit, len(got), len(c.want), clip(got), clip(c.want))
			}
		})
	}
}

func clip(s string) string {
	r := []rune(s)
	if len(r) <= 16 {
		return s
	}
	return string(r[:16])
}

// BuildLog 的 Error 兜底：超长 errMsg 进日志必须已被压进 varchar(1024)，
// 否则整条失败日志被丢弃 —— 这正是引入 TruncateRunes 的原因。
func TestBuildLogTruncatesErrorColumn(t *testing.T) {
	req := &RelayRequest{TraceID: "t1", PublicModel: "m"}
	res := &RelayResult{TraceID: "t1"}
	long := strings.Repeat("网关错误页", 400) // 2000 字符，超 varchar(1024)
	entry := BuildLog(req, res, Usage{}, 502, long, 1, 2)
	if n := len([]rune(entry.Error)); n > 1024 {
		t.Fatalf("BuildLog Error 未截断：%d 字符（>1024 会撑爆日志列）", n)
	}
	if !strings.HasSuffix(entry.Error, "…") {
		t.Fatal("截断后的错误应以省略号结尾，表明被截过")
	}
}
