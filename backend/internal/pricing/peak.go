package pricing

import (
	"strings"
	"time"

	"llm-relay/internal/model"
)

// PeakWindow 描述一个峰时窗口。窗口内的单价按 Multiplier 放大。
// 基准价始终存「谷时价」，与 DeepSeek 官方的 Off-peak 口径一致。
type PeakWindow struct {
	Days       []int   `json:"days"`       // ISO 星期，1=周一 .. 7=周日；空表示每天
	Start      string  `json:"start"`      // HH:MM，UTC
	End        string  `json:"end"`        // HH:MM，UTC；早于 Start 表示跨午夜
	Multiplier float64 `json:"multiplier"` // 倍率，DeepSeek 峰时为 2
	Label      string  `json:"label"`
}

// MatchPeak 判断给定时刻是否落在某个峰时窗口内，返回倍率与标签。
// 多个窗口叠加时取最大倍率，避免重复相乘导致价格失真。
func MatchPeak(rules model.JSONList, at time.Time) (float64, string) {
	if len(rules) == 0 {
		return 1, ""
	}
	best := 1.0
	label := ""
	for _, raw := range rules {
		w, ok := toWindow(raw)
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
		if mult > best {
			best = mult
			label = w.Label
		}
	}
	return best, label
}

func inWindow(w PeakWindow, at time.Time) bool {
	weekday := int(at.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	if len(w.Days) > 0 && !containsDay(w.Days, weekday) {
		return false
	}

	cur := at.Format("15:04")
	start := strings.TrimSpace(w.Start)
	end := strings.TrimSpace(w.End)
	if start == "" || end == "" {
		return false
	}
	if start <= end {
		return cur >= start && cur <= end
	}
	// 跨午夜窗口，例如 22:30 - 00:30
	return cur >= start || cur <= end
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

// DeepSeekPeakRules 返回 DeepSeek 官方公布的峰时窗口。
// 官方口径：UTC 周一至周五 01:00-04:00 与 06:00-10:00 为峰时，价格为谷时的两倍。
func DeepSeekPeakRules() model.JSONList {
	return model.JSONList{
		{"days": []any{1, 2, 3, 4, 5}, "start": "01:00", "end": "04:00", "multiplier": 2.0, "label": "峰时"},
		{"days": []any{1, 2, 3, 4, 5}, "start": "06:00", "end": "10:00", "multiplier": 2.0, "label": "峰时"},
	}
}
