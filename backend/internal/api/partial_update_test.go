package api

import (
	"encoding/json"
	"testing"
)

// 局部更新的前提是能区分「没传」与「显式传了空值」。
//
// 这两条一旦退化成值类型，表现就是「只改名字把别的字段清空」——
// 而且因为 JSONMap 的 Valuer 把 nil map 写成 "{}" 而不是 NULL，
// 事后从数据上看不出被清过。
//
// 密钥的模型白名单同理：nil 是「这次不动」，显式空数组是「清空」
// （清空会被 normalizeKeyRoutes 拦下，但那是校验层的职责，
// 解析层必须先能区分这两种意图）。
func TestKeyPayloadWhitelistOmittedVsEmpty(t *testing.T) {
	var omitted keyPayload
	if err := json.Unmarshal([]byte(`{"name":"k"}`), &omitted); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if omitted.AllowedModels != nil || omitted.ModelChannels != nil {
		t.Error("没传白名单时必须是 nil，否则只改名字会把白名单清掉")
	}

	var explicit keyPayload
	if err := json.Unmarshal([]byte(`{"allowed_models":[]}`), &explicit); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if explicit.AllowedModels == nil {
		t.Error("显式传空数组必须是非 nil，否则清空白名单会被当成没传")
	}
	if len(explicit.AllowedModels) != 0 {
		t.Errorf("空数组应当长度为 0，实际 %v", explicit.AllowedModels)
	}
}

// model_channels 的 JSON 形状：{"模型名": [渠道ID有序]}。
// 键是模型名（不是 ID），值是有序数组 —— 顺序即优先级，jsonb 直接保留数组序。
func TestKeyPayloadModelChannelsShape(t *testing.T) {
	var p keyPayload
	if err := json.Unmarshal([]byte(
		`{"name":"k","allowed_models":["glm-5.3-flash"],"model_channels":{"glm-5.3-flash":[3,1,7]}}`,
	), &p); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(p.ModelChannels) != 1 {
		t.Fatalf("应解析出 1 个模型的路由，实际 %v", p.ModelChannels)
	}
	ids := p.ModelChannels["glm-5.3-flash"]
	if len(ids) != 3 || ids[0] != 3 || ids[1] != 1 || ids[2] != 7 {
		t.Errorf("渠道 ID 列表应保持提交顺序 [3 1 7]，实际 %v", ids)
	}
}
