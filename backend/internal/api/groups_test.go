package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"llm-relay/internal/model"
)

// 分组的模型商必须能「显式传 0」被识别出来。
//
// 新建分组时 0 会被拒绝（必选模型商），但编辑默认分组时要能保留 0（不限）：
// 字段退回值类型的话，「没传」与「传 0」就分不开，默认分组一编辑
// 就会被迫限定一个模型商，整个中转站的路由范围跟着变。
func TestGroupUpdateProviderIDExplicitZero(t *testing.T) {
	var omitted groupUpdatePayload
	if err := json.Unmarshal([]byte(`{"name":"g1"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.ProviderID != nil {
		t.Error("没传 provider_id 时应为 nil，表示这一项不动")
	}

	var explicit groupUpdatePayload
	if err := json.Unmarshal([]byte(`{"provider_id":0}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.ProviderID == nil || *explicit.ProviderID != 0 {
		t.Fatalf("显式传的 provider_id=0 必须被识别，实际 %v", explicit.ProviderID)
	}

	var set groupUpdatePayload
	if err := json.Unmarshal([]byte(`{"provider_id":2}`), &set); err != nil {
		t.Fatal(err)
	}
	if set.ProviderID == nil || *set.ProviderID != 2 {
		t.Fatalf("provider_id=2 应被解出，实际 %v", set.ProviderID)
	}
}

// 分组的模型商字段名是前端与后端的约定，改了会让界面上的徽标静默变成「不限」。
func TestChannelGroupProviderFieldName(t *testing.T) {
	field, ok := reflect.TypeOf(model.ChannelGroup{}).FieldByName("ProviderID")
	if !ok {
		t.Fatal("model.ChannelGroup 上找不到 ProviderID")
	}
	if got := field.Tag.Get("json"); got != "provider_id" {
		t.Errorf("json 名应为 provider_id，实际 %q", got)
	}
	if field.Type.Kind().String() != "uint" {
		t.Errorf("ProviderID 应为 uint，实际 %s", field.Type.Kind())
	}
}
