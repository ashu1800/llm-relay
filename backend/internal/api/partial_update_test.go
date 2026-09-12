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
// 原来这条用的是 templateUpdatePayload；模板管理下线后改用 groupUpdatePayload，
// 同样是「所有字段可选」的载荷（渠道维度的同类断言见 groups_test.go）。

// 密钥白名单同理：空数组是有效值（清空白名单），不能与「没传」混为一谈。
func TestKeyPayloadWhitelistOmittedVsEmpty(t *testing.T) {
	var omitted keyPayload
	if err := json.Unmarshal([]byte(`{"name":"k"}`), &omitted); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if omitted.AllowedModels != nil || omitted.AllowedGroups != nil {
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
