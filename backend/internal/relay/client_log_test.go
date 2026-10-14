package relay

import "testing"

// 落库：客户端三字段必须原样进到日志行。BuildLog 是所有日志路径
// （成功 / 上游报错 / 无渠道 / 重试失败 / 兜底）的唯一出口，漏复制不会
// 报错、只是列表里整列都是空的 —— 属于本项目里典型的一类静默错误，
// 所以要有专门一条盯着（与 TestBuildLogCarriesTiming 同一个动机）。
func TestBuildLogCarriesClient(t *testing.T) {
	req := &RelayRequest{
		TraceID: "t-client", PublicModel: "deepseek-chat",
		ClientID:   "claude-code",
		ClientName: "Claude Code",
		ClientUA:   "claude-cli/2.1.260 (external, cli)",
	}
	res := &RelayResult{Attempt: &Attempt{StatusCode: 200}}

	entry := BuildLog(req, res, Usage{}, 200, "", Timing{})

	if entry.ClientID != "claude-code" || entry.ClientName != "Claude Code" ||
		entry.ClientUA != "claude-cli/2.1.260 (external, cli)" {
		t.Fatalf("客户端三字段应原样落库，实际 id=%q name=%q ua=%q",
			entry.ClientID, entry.ClientName, entry.ClientUA)
	}
}

// 没认出客户端的行（空 UA 或历史行）三字段都是空串，与「当时没记」
// 是同一种形态 —— 前端只认这一种空值语义并显示 —。
func TestBuildLogClientEmptyWhenUnknown(t *testing.T) {
	req := &RelayRequest{TraceID: "t-client-2", PublicModel: "deepseek-chat"}
	entry := BuildLog(req, &RelayResult{}, Usage{}, 200, "", Timing{})
	if entry.ClientID != "" || entry.ClientName != "" || entry.ClientUA != "" {
		t.Fatalf("没认出客户端时应为空串，实际 id=%q name=%q ua=%q",
			entry.ClientID, entry.ClientName, entry.ClientUA)
	}
}
