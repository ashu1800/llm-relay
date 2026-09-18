package api

// 分组日预算的检查与提醒（可靠性三件套之三：省钱）。
//
// 口径上有一铁律：**金额不做跨币种折算**。预算按币种分别设额
// （DailyBudget = {"CNY": 50, "USD": 10}），超支判定逐币种独立进行，
// 与看板金额（costs 按币种分开、永不相加）完全同一套逻辑。
//
// 提醒策略（站主拍板）：80% 预警、100% 超支各喊一次，**每天每档一次**，
// 只提醒不拦截 —— 预算配错一刀切断会全站瘫痪，还会腰斩正在流式的响应。
// 去重状态在内存（重启归零）：最坏情况是重启后重复提醒一次，保守无害。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
)

// normalizeBudgetAmounts 把 jsonb 里的预算读成干净的「币种 -> 金额」。
// jsonb 的数字解出来是 float64；币种键统一大写，非法/非正的条目直接丢弃。
func normalizeBudgetAmounts(raw model.JSONMap) map[string]float64 {
	out := map[string]float64{}
	for k, v := range raw {
		cur := strings.ToUpper(strings.TrimSpace(k))
		f, ok := v.(float64)
		if cur == "" || !ok || f <= 0 {
			continue
		}
		out[cur] = f
	}
	return out
}

// normalizeDailyBudget 校验客户端提交的预算。返回 nil 表示「清除预算」：
// 提交空对象与提交 null 同义 —— 「把预算删掉」是合法操作，不能被当成漏传。
func normalizeDailyBudget(raw map[string]float64) (model.JSONMap, error) {
	out := model.JSONMap{}
	for k, v := range raw {
		cur := strings.ToUpper(strings.TrimSpace(k))
		if cur == "" {
			return nil, errors.New("预算币种不能为空")
		}
		if v < 0 {
			return nil, errors.New("预算金额不能为负数（清除预算请提交空对象）")
		}
		if v > 0 {
			out[cur] = v
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// budgetAlertState 记录「某个预算的某个档位最近一次提醒发生在哪天」。
type budgetAlertState struct {
	mu   sync.Mutex
	sent map[string]string // key = 分组ID|币种|档位 -> "2006-01-02"
}

var budgetAlerts = &budgetAlertState{sent: map[string]string{}}

// shouldAlert 今天这一档还没喊过就返回 true 并占下名额。
func (s *budgetAlertState) shouldAlert(groupID uint, currency, level string, today string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%d|%s|%s", groupID, currency, level)
	if s.sent[key] == today {
		return false
	}
	s.sent[key] = today
	return true
}

// budgetStatus 是一次预算检查在某一币种上的结论。
type budgetStatus struct {
	Currency string  `json:"currency"`
	Limit    float64 `json:"limit"`
	Spent    float64 `json:"spent"`
	Ratio    float64 `json:"ratio"`
	// Level："80" 预警 / "100" 超支。空 = 未达任何档位
	Level string `json:"level"`
}

// groupSpentToday 聚合某分组今日各币种的花费。
// 口径与看板一致：estimated_cost 按 cost_currency 分组求和，
// 时间范围与 /stats/summary 的 today 完全同一份 resolveRange。
func (s *Server) groupSpentToday(ctx context.Context, groupID uint, start, end time.Time) map[string]float64 {
	out := map[string]float64{}
	qctx, cancel := context.WithTimeout(ctx, liveQueryTimeout)
	defer cancel()
	var rows []struct {
		CostCurrency string
		Spent        float64
	}
	err := s.deps.Store.DB().WithContext(qctx).
		Model(&model.RequestLog{}).
		Select("cost_currency, COALESCE(SUM(estimated_cost), 0) AS spent").
		Where("created_at >= ? AND created_at <= ? AND group_id = ?", start, end, groupID).
		Group("cost_currency").
		Scan(&rows).Error
	if err != nil {
		// 聚合失败静默跳过本轮：宁可晚一拍提醒，也不能拿半份数据喊「超支」
		return out
	}
	for _, r := range rows {
		if r.CostCurrency != "" {
			out[r.CostCurrency] = r.Spent
		}
	}
	return out
}

// checkBudgets 对所有配了预算的分组做一轮检查，跨过档位时广播 budget_alert。
// 挂在 liveStatsLoop 的同一拍：它有现成的「有新日志才醒」的 kick 节奏，
// 预算跟着统计的节拍走，天然不空转。
func (s *Server) checkBudgets(ctx context.Context, start, end time.Time) {
	var groups []model.ChannelGroup
	if err := s.deps.Store.DB().
		Select("id", "name", "daily_budget").
		Where("daily_budget IS NOT NULL").
		Find(&groups).Error; err != nil || len(groups) == 0 {
		return
	}
	now := time.Now()
	today := now.Format("2006-01-02")
	for _, g := range groups {
		amounts := normalizeBudgetAmounts(g.DailyBudget)
		if len(amounts) == 0 {
			continue
		}
		spent := s.groupSpentToday(ctx, g.ID, start, end)
		for cur, limit := range amounts {
			sp := spent[cur]
			ratio := 0.0
			if limit > 0 {
				ratio = sp / limit
			}
			level := ""
			switch {
			case ratio >= 1:
				level = "100"
			case ratio >= 0.8:
				level = "80"
			}
			if level == "" || !budgetAlerts.shouldAlert(g.ID, cur, level, today) {
				continue
			}
			s.deps.Live.broadcast(mustJSON(liveMessage{Type: "budget_alert", Data: gin.H{
				"group_id":   g.ID,
				"group_name": g.Name,
				"currency":   cur,
				"level":      level,
				"limit":      limit,
				"spent":      sp,
				"ratio":      ratio,
				"day":        today,
			}}))
		}
	}
}
