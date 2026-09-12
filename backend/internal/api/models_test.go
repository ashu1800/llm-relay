package api

import (
	"encoding/json"
	"testing"
)

func uptr(v uint) *uint     { return &v }
func bptr(v bool) *bool     { return &v }
func sptr(v string) *string { return &v }

// 只改名称时不能碰到其他字段。此前的实现无条件写 enabled，
// 于是「改个名字」会把模型悄悄停用。
func TestModelUpdatesPartialDoesNotTouchOthers(t *testing.T) {
	up := modelUpdates(modelPayload{PublicName: "m1"})
	if len(up) != 1 {
		t.Fatalf("只传名称应只更新 1 个字段，实际 %d 个: %v", len(up), up)
	}
	if _, ok := up["enabled"]; ok {
		t.Error("未传 enabled 时不应更新它")
	}
	if up["public_name"] != "m1" {
		t.Errorf("public_name 应为 m1，实际 %v", up["public_name"])
	}
}

// 模型商此前被整个漏掉，导致界面上改了却不生效。
func TestModelUpdatesIncludesProvider(t *testing.T) {
	up := modelUpdates(modelPayload{ProviderID: uptr(2)})
	got, ok := up["provider_id"]
	if !ok {
		t.Fatal("provider_id 必须出现在更新字段里，否则切换模型商会静默失效")
	}
	if got != uint(2) {
		t.Errorf("provider_id 应为 2，实际 %v", got)
	}
}

// 显式传零值要能生效：停用模型、把模型商改成 0 都应该被采纳。
// 这正是用指针而不是值类型的原因。
func TestModelUpdatesExplicitZeroIsHonoured(t *testing.T) {
	up := modelUpdates(modelPayload{Enabled: bptr(false), Description: sptr("")})
	if v, ok := up["enabled"]; !ok || v != false {
		t.Errorf("显式传 enabled=false 应被采纳，实际 %v", up)
	}
	if v, ok := up["description"]; !ok || v != "" {
		t.Errorf("显式传空备注应被采纳（用于清空），实际 %v", up)
	}
}

func TestModelUpdatesEmptyPayload(t *testing.T) {
	if up := modelUpdates(modelPayload{}); len(up) != 0 {
		t.Errorf("什么都没传时不应产生更新字段，实际 %v", up)
	}
	// 只有空白的名称等同于没传
	if up := modelUpdates(modelPayload{PublicName: "   "}); len(up) != 0 {
		t.Errorf("纯空白名称应被忽略，实际 %v", up)
	}
}

// 名称两侧空白要去掉，否则会存进一个带空格的模型名。
func TestModelUpdatesTrimsName(t *testing.T) {
	up := modelUpdates(modelPayload{PublicName: "  m2  "})
	if up["public_name"] != "m2" {
		t.Errorf("名称应去掉两侧空白，实际 %q", up["public_name"])
	}
}

// 指针字段必须能与「未提供」区分开，这是整套修法的基础。
func TestModelPayloadDistinguishesOmittedFromZero(t *testing.T) {
	var omitted modelPayload
	if err := json.Unmarshal([]byte(`{"public_name":"x"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.Enabled != nil {
		t.Error("未传的 enabled 应为 nil")
	}
	if omitted.ProviderID != nil {
		t.Error("未传的 provider_id 应为 nil")
	}

	var explicit modelPayload
	if err := json.Unmarshal([]byte(`{"public_name":"x","enabled":false,"provider_id":0}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.Enabled == nil || *explicit.Enabled != false {
		t.Error("显式传的 enabled=false 应被识别")
	}
	if explicit.ProviderID == nil || *explicit.ProviderID != 0 {
		t.Error("显式传的 provider_id=0 应被识别")
	}
	if up := modelUpdates(explicit); len(up) != 3 {
		t.Errorf("应更新 3 个字段，实际 %v", up)
	}
}
