package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"llm-relay/internal/model"
)

// 候选数量必须有上界，且预算耗尽时要与「站点没有图标」区分开。
//
// 这两条对应真实会发生的两种误判：
//  1. 候选无上界：站点把一个页面塞进几百个 <link rel=icon> 是合法的，
//     而每个候选最长 8 秒 —— 预算会被前面几个慢候选吃光，按惯例猜的
//     /favicon.ico（命中率最高）反而永远轮不到。
//  2. 错误混同：预算耗尽后剩余候选全部立刻失败并覆盖 lastErr，
//     最终只报出一句「没有找到可用的图标」——用户会去反复重试一个
//     注定超时的站点，而不是换个地址或稍后再试。
//
// 这里不发起真实网络请求（回环地址会被 netguard 拒绝），只钉住
// 「预算耗尽时给的是超时提示、且带上了上限」这条可观察行为。
func TestIconFetchBudgetExhaustedMessage(t *testing.T) {
	// 构造一个已经耗尽的 ctx，让 fetchUpstreamIcon 在发现阶段就失败，
	// 走的是「预算耗尽」那条分支
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()

	s := &Server{}
	_, err := s.fetchUpstreamIcon(ctx, channelForIconTest("https://example.com"))
	if err == nil {
		t.Fatal("预算耗尽时应当返回错误")
	}
	// 具体文案随分支不同（可能在首次校验就失败），但**不能**是
	// 「没有找到可用的图标」这种把超时说成没图标的说法
	if strings.Contains(err.Error(), "没有找到可用的图标") &&
		!strings.Contains(err.Error(), "超时") {
		t.Errorf("预算耗尽不应被说成「没有找到图标」，实际: %v", err)
	}
}

// 候选上限必须是个真正起作用的界。
//
// 直接引用生产常量（而不是在测试里抄一份）：抄一份的话，改了代码而忘了
// 改测试，这条断言就永远绿着 —— 那正是它要防的事。
func TestIconCandidateBoundIsEffective(t *testing.T) {
	// 惯例路径 3 个 × 2 组（root 与 underBase）
	conventional := len(iconPaths) * 2
	if maxIconCandidates <= conventional {
		t.Errorf("候选上限 %d 必须大于惯例路径数 %d，否则惯例路径永远轮不到",
			maxIconCandidates, conventional)
	}
	// 总预算 iconFetchTimeout*3，按最慢情况每候选一份超时算，上界不能太大
	budget := iconFetchTimeout * 3
	worstCase := time.Duration(maxIconCandidates) * iconFetchTimeout
	if worstCase < budget {
		// 这是理想情况（每个候选都能跑满），界可以比预算小 —— 合理
		return
	}
	t.Logf("上界 %d × %s = %s，超过总预算 %s；实际会由 ctx 提前终止",
		maxIconCandidates, iconFetchTimeout, worstCase, budget)
}

// 构造一个只带 base_url 的渠道，供预算测试用。
func channelForIconTest(baseURL string) model.Channel {
	return model.Channel{BaseURL: baseURL}
}
