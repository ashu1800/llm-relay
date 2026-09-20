package pricing

import (
	"fmt"
	"strings"
	"time"

	"llm-relay/internal/model"
)

// PeakWindow 描述一个时段倍率窗口：窗口内的单价乘以 Multiplier。
//
// 时间语义：Start/End 与 Days 都按**服务器本地时区**判断（部署里是 Asia/Shanghai）。
// 这里特意写清楚：定价自动同步还在的时候，这些字段存的是 UTC（DeepSeek 官方口径），
// 同步删掉后规则由用户手填，用户看到的 9:00 就是他手表上的 9:00。
type PeakWindow struct {
	Days       []int   `json:"days"`       // ISO 星期，1=周一 .. 7=周日；空表示每天
	Start      string  `json:"start"`      // HH:MM，本地时间
	End        string  `json:"end"`        // HH:MM，本地时间；早于 Start 表示跨午夜
	Multiplier float64 `json:"multiplier"` // 倍率：2 = 双倍，0.5 = 五折
	Label      string  `json:"label"`
}

// MaxMultiplier 是倍率上限，挡住「把单价填错小数位」之外的量级错误。
const MaxMultiplier = 100.0

// MatchPeak 判断给定时刻是否落在某个时段窗口内，返回倍率、标签与是否命中。
//
// 多条规则同时命中时**以列表里最后一条为准**（后面的覆盖前面的），
// 而不是取最大倍率：倍率可以是折扣（0.5），取最大的话打折规则永远不生效，
// 用户只会看到「设了没反应」。要固定优先级就把规则按顺序排好。
func MatchPeak(rules model.JSONList, at time.Time) (float64, string, bool) {
	for i := len(rules) - 1; i >= 0; i-- {
		w, ok := toWindow(rules[i])
		if !ok {
			continue
		}
		if !inWindow(w, at) {
			continue
		}
		mult := w.Multiplier
		if mult <= 0 {
			mult = 1
		}
		return mult, w.Label, true
	}
	return 1, "", false
}

// NormalizeRules 校验并规整时段规则。
//
// 为什么必须校验而不是「存进去再说」：窗口字段写错（比如 end 填成 "18"）
// 不会报错，只会永远不命中 —— 用户以为已经配好了双倍计费，实际一直按原价。
// 所以把错误在保存时抛出来，并指明是第几条。
func NormalizeRules(rules model.JSONList) (model.JSONList, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	out := make(model.JSONList, 0, len(rules))
	for i, raw := range rules {
		where := fmt.Sprintf("第 %d 条时段规则", i+1)
		start := strings.TrimSpace(asStringValue(raw["start"]))
		end := strings.TrimSpace(asStringValue(raw["end"]))
		if start == "" || end == "" {
			return nil, fmt.Errorf("%s：开始与结束时间都要填", where)
		}
		if _, err := time.Parse("15:04", start); err != nil {
			return nil, fmt.Errorf("%s：开始时间 %q 不是 HH:MM 格式", where, start)
		}
		if _, err := time.Parse("15:04", end); err != nil {
			return nil, fmt.Errorf("%s：结束时间 %q 不是 HH:MM 格式", where, end)
		}
		// 起止相同（且不是跨午夜 —— 相同值构不成跨午夜）意味着零长度窗口，
		// 永远不会命中。前端 helpers 注释里一直声称会拦，这里补上实现
		if start == end {
			return nil, fmt.Errorf("%s：开始与结束时间相同，这条规则永远不会生效", where)
		}
		mult, ok := toFloat(raw["multiplier"])
		if !ok || mult <= 0 {
			return nil, fmt.Errorf("%s：倍率必须是大于 0 的数字", where)
		}
		if mult > MaxMultiplier {
			return nil, fmt.Errorf("%s：倍率不能超过 %g", where, MaxMultiplier)
		}
		days := make([]int, 0, 7)
		if list, ok := raw["days"].([]any); ok {
			for _, item := range list {
				n, ok := toFloat(item)
				if !ok {
					return nil, fmt.Errorf("%s：星期取值必须是 1-7", where)
				}
				d := int(n)
				if d < 1 || d > 7 {
					return nil, fmt.Errorf("%s：星期取值必须在 1-7 之间（1=周一）", where)
				}
				if !containsDay(days, d) {
					days = append(days, d)
				}
			}
		}
		label := strings.TrimSpace(asStringValue(raw["label"]))
		if len([]rune(label)) > 32 {
			return nil, fmt.Errorf("%s：备注不要超过 32 个字", where)
		}
		out = append(out, model.JSONMap{
			"days":       days,
			"start":      start,
			"end":        end,
			"multiplier": mult,
			"label":      label,
		})
	}
	return out, nil
}

// asStringValue 安全取字符串（jsonb 里的值可能是任意类型）。
func asStringValue(v any) string {
	s, _ := v.(string)
	return s
}

func inWindow(w PeakWindow, at time.Time) bool {
	weekday := int(at.Weekday())
	if weekday == 0 {
		weekday = 7
	}

	cur := at.Format("15:04")
	start := strings.TrimSpace(w.Start)
	end := strings.TrimSpace(w.End)
	if start == "" || end == "" {
		return false
	}
	if start <= end {
		// 常规窗口：归属日就是发生时刻的当天
		return cur >= start && cur <= end && dayMatch(w.Days, weekday)
	}
	// 跨午夜窗口，例如 22:30 - 00:30：午夜前段归属起始日，尾段归属前一天。
	//
	// 「周五 22:00–02:00」配了 days=[5] 时，周六 00:30 也必须命中 ——
	// 窗口的星期是**起始日**的星期，不是发生时刻的星期。修复前先按发生
	// 时刻的星期过滤，尾段永远不生效，配了双倍却「看起来没生效」。
	if cur >= start {
		return dayMatch(w.Days, weekday)
	}
	if cur <= end {
		return dayMatch(w.Days, prevWeekday(weekday))
	}
	return false
}

// dayMatch 判断星期是否在窗口的星期列表里；列表为空表示不限定（每天都算）。
func dayMatch(days []int, weekday int) bool {
	if len(days) == 0 {
		return true
	}
	return containsDay(days, weekday)
}

// prevWeekday 取前一天的星期（1-7 编码，周一的前一天是周日 7）。
func prevWeekday(weekday int) int {
	if weekday == 1 {
		return 7
	}
	return weekday - 1
}

// toFloat 同时接受 JSON 反序列化得到的 float64 与 Go 内直接构造的 int/int64。
// 只用 float64 断言会让代码构造的规则静默失效（曾导致周末被误判为峰时）。
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func containsDay(days []int, target int) bool {
	for _, d := range days {
		if d == target {
			return true
		}
	}
	return false
}

// toWindow 把 jsonb 里的松散结构转成强类型窗口。
func toWindow(raw map[string]any) (PeakWindow, bool) {
	var w PeakWindow
	if v, ok := raw["start"].(string); ok {
		w.Start = v
	}
	if v, ok := raw["end"].(string); ok {
		w.End = v
	}
	if v, ok := raw["label"].(string); ok {
		w.Label = v
	}
	if v, ok := toFloat(raw["multiplier"]); ok {
		w.Multiplier = v
	}
	if list, ok := raw["days"].([]any); ok {
		for _, item := range list {
			if n, ok := toFloat(item); ok {
				w.Days = append(w.Days, int(n))
			}
		}
	}
	if w.Start == "" || w.End == "" {
		return w, false
	}
	return w, true
}
