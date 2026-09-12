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
func TestTemplateUpdatePayloadDistinguishesOmittedFromEmpty(t *testing.T) {
	var omitted templateUpdatePayload
	if err := json.Unmarshal([]byte(`{"name":"改名"}`), &omitted); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if omitted.ExtraConf != nil || omitted.CustomMap != nil || omitted.GroupID != nil {
		t.Error("没传的字段必须解析成 nil，否则会被当成「显式清空」")
	}
	if omitted.Name == nil || *omitted.Name != "改名" {
		t.Error("传了的字段必须解析出来")
	}

	var explicit templateUpdatePayload
	if err := json.Unmarshal([]byte(`{"extra_config":{},"group_id":0,"custom_mapping":{}}`), &explicit); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if explicit.ExtraConf == nil {
		t.Error("显式传 {} 必须解析成非 nil，否则「清空」会被当成「没传」")
	}
	if explicit.GroupID == nil || *explicit.GroupID != 0 {
		t.Error("显式传 0 必须能与「没传」区分开")
	}
	if explicit.CustomMap == nil {
		t.Error("显式传 {} 必须解析成非 nil")
	}
}

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
