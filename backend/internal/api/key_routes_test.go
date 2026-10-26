package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"llm-relay/internal/model"
)

// KeyRoutesOf 是转发热路径的解析入口：每个请求都要拿它找候选渠道。
// 规则钉在这里，防止「直接改库留下的坏形状」静默改变路由范围。
func TestKeyRoutesOf(t *testing.T) {
	parse := func(t *testing.T, raw string) *model.APIKey {
		t.Helper()
		var k model.APIKey
		if err := json.Unmarshal([]byte(raw), &k); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		return &k
	}

	t.Run("正常形状保序解析", func(t *testing.T) {
		k := parse(t, `{"model_channels":{"glm-5.3-flash":[3,1,7],"deepseek-chat":[2]}}`)
		got := KeyRoutesOf(k)
		if !reflect.DeepEqual(got["glm-5.3-flash"], []uint{3, 1, 7}) {
			t.Errorf("应保序解析出 [3 1 7]，实际 %v", got["glm-5.3-flash"])
		}
		if !reflect.DeepEqual(got["deepseek-chat"], []uint{2}) {
			t.Errorf("应解析出 [2]，实际 %v", got["deepseek-chat"])
		}
	})

	t.Run("字符串数字也认（手工改库的兜底）", func(t *testing.T) {
		k := parse(t, `{"model_channels":{"m":["3","1"]}}`)
		got := KeyRoutesOf(k)
		if !reflect.DeepEqual(got["m"], []uint{3, 1}) {
			t.Errorf("字符串数字应被识别，实际 %v", got["m"])
		}
	})

	t.Run("坏条目与零值丢弃", func(t *testing.T) {
		k := parse(t, `{"model_channels":{"m":[0,-2,"abc"],"bad":"not-an-array"}}`)
		got := KeyRoutesOf(k)
		if len(got["m"]) != 0 {
			t.Errorf("零值与非数字条目应全部丢弃，实际 %v", got["m"])
		}
		if _, ok := got["bad"]; ok {
			t.Error("非数组的条目应整体跳过")
		}
	})

	t.Run("空路由返回空 map 而不是 nil", func(t *testing.T) {
		if got := KeyRoutesOf(nil); got == nil {
			t.Error("nil 密钥应返回空 map（调用方直接读键，不该 panic）")
		}
	})
}

// KeyChannelScope 是 /v1/models 收窄候选集的判据：全部路由渠道去重。
func TestKeyChannelScope(t *testing.T) {
	var k model.APIKey
	if err := json.Unmarshal([]byte(
		`{"model_channels":{"a":[3,1],"b":[7,3]}}`), &k); err != nil {
		t.Fatal(err)
	}
	got := KeyChannelScope(KeyRoutesOf(&k))
	if len(got) != 3 {
		t.Fatalf("3 个渠道应去重为 3 项，实际 %v", got)
	}
	seen := map[uint]bool{}
	for _, id := range got {
		seen[id] = true
	}
	for _, want := range []uint{1, 3, 7} {
		if !seen[want] {
			t.Errorf("应包含渠道 %d，实际 %v", want, got)
		}
	}

	// 空路由必须返回空切片：/v1/models 依赖 len==0 判定「无渠道可服务」
	var empty model.APIKey
	if ids := KeyChannelScope(KeyRoutesOf(&empty)); len(ids) != 0 {
		t.Errorf("空路由应返回空切片，实际 %v", ids)
	}
}
