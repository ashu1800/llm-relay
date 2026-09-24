package api

import (
	"strings"
	"testing"
)

// 日志排序键的测试（2026-09-24 UI 审评 P1-9）。
//
// 这里钉住三件事，它们各自对应一类真实会犯的错：
//  1. sort 是**白名单**：认不出的键一律退回默认，绝不把它拼进 SQL
//     （sort 直接来自查询串，拼进去就是注入面）；
//  2. 每个键都带 id 兜底：排序值相同的行顺序不定，翻页时同一行会
//     出现在两页上或整个消失 —— 分页 + 排序必须构成全序；
//  3. cost 把币种放在金额**前面**：站里不做汇率换算，
//     $0.0007 排在 ¥2.35 前面是错的（实测库里两种币都在）。
func TestOrderLogs(t *testing.T) {
	// ---- 1. 白名单之外一律退回默认 ----
	// 把「非法的键不会被当成排序指令」这件事写成断言：它们要么不在表里，
	// 要么值就是默认那条。表里若真混进了这些键，下面第一条就会炸。
	for _, bad := range []string{
		"id", "random", "1; DROP TABLE request_logs",
		"total_ms DESC --", "elapsed, (SELECT 1)", "ELAPSED", "cost;", " ",
	} {
		if clause, ok := logSortClause[bad]; ok {
			t.Errorf("sort=%q 不该出现在白名单里（会变成排序指令），实际得到：%s", bad, clause)
		}
	}
	// 注入片段绝不能出现在任何一条子句里
	for key, clause := range logSortClause {
		upper := strings.ToUpper(clause)
		if strings.Contains(upper, "DROP") || strings.Contains(clause, "--") || strings.Contains(clause, ";") {
			t.Errorf("排序键 %q 的子句里出现了可疑片段：%s", key, clause)
		}
	}

	// ---- 2. 每个键都必须带 id 兜底（全序）----
	for key, clause := range logSortClause {
		if !strings.Contains(clause, "id DESC") {
			t.Errorf("排序键 %q 缺少 id 兜底：%s（相同值的行顺序不定，翻页会重复或漏行）", key, clause)
		}
	}

	// ---- 3. cost 必须先按币种分组 ----
	// 站里明确不做汇率换算，estimated_cost 只是 numeric —— 直接比大小
	// 会把币种当同一个单位，得出「$0.0007 比 ¥2.35 贵」。
	for _, key := range []string{"cost", "-cost"} {
		clause := logSortClause[key]
		ci := strings.Index(clause, "cost_currency")
		mi := strings.Index(clause, "estimated_cost")
		if ci < 0 || mi < 0 || ci > mi {
			t.Errorf("排序键 %q 没有先按币种分组：%s（不同币种的钱不能直接比大小）", key, clause)
		}
	}

	// ---- 4. 方向 ----
	if !strings.Contains(logSortClause["-elapsed"], "total_ms DESC") {
		t.Errorf("`-elapsed` 应当是「最慢的在前」：%s", logSortClause["-elapsed"])
	}
	if !strings.Contains(logSortClause["elapsed"], "total_ms ASC") {
		t.Errorf("`elapsed` 应当是「最快的在前」：%s", logSortClause["elapsed"])
	}
	if !strings.Contains(logSortClause["-status"], "status_code DESC") {
		t.Errorf("`-status` 应当是「状态码大的在前」：%s", logSortClause["-status"])
	}
}
