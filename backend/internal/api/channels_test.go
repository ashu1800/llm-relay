package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"llm-relay/internal/model"
)

// 渠道的模型商必须能「显式改成未指定」（0）。
//
// 这条用例锁的是字段类型：ProviderID 一旦退回值类型 uint，
// 「没传这个字段」和「传了 0」就再也分不开，把渠道改回「未指定」会静默失效，
// 表现为徽标怎么都去不掉 —— 与模型那边 modelPayload 是同一个坑。
func TestChannelProviderIDExplicitZero(t *testing.T) {
	var omitted channelPayload
	if err := json.Unmarshal([]byte(`{"name":"c1"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.ProviderID != nil {
		t.Error("没传 provider_id 时应为 nil，表示「这一项不动」")
	}

	var explicit channelPayload
	if err := json.Unmarshal([]byte(`{"name":"c1","provider_id":0}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.ProviderID == nil || *explicit.ProviderID != 0 {
		t.Fatalf("显式传的 provider_id=0 必须被认成「未指定」，实际 %v", explicit.ProviderID)
	}

	var set channelPayload
	if err := json.Unmarshal([]byte(`{"provider_id":2}`), &set); err != nil {
		t.Fatal(err)
	}
	if set.ProviderID == nil || *set.ProviderID != 2 {
		t.Fatalf("provider_id=2 应被解出，实际 %v", set.ProviderID)
	}
}

// Channel.ProviderID 不能带 gorm 的 default 标签。
//
// 带默认值的字段在值为零时会被 GORM 从 INSERT 语句里省掉，转而落库成列默认值，
// 0 就永远存不进去 —— 这正是「没选过模型商的渠道全被标成 OpenAI」的根因。
// 标签很容易被人「顺手补回来」，所以在这里挡住。
func TestChannelProviderHasNoGormDefault(t *testing.T) {
	field, ok := reflect.TypeOf(model.Channel{}).FieldByName("ProviderID")
	if !ok {
		t.Fatal("model.Channel 上找不到 ProviderID")
	}
	tag := field.Tag.Get("gorm")
	if strings.Contains(tag, "default") {
		t.Errorf("ProviderID 不能有 gorm default（零值会被顶成列默认值），实际 tag 是 %q", tag)
	}
}

// 「用模板建渠道」的入参同样要能区分「没传」与「传 0」。
func TestApplyTemplateProviderIDExplicitZero(t *testing.T) {
	var p applyTemplatePayload
	if err := json.Unmarshal([]byte(`{"name":"t1","api_key":"sk-x"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.ProviderID != nil {
		t.Error("没传 provider_id 时应为 nil")
	}
	if err := json.Unmarshal([]byte(`{"provider_id":0}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.ProviderID == nil || *p.ProviderID != 0 {
		t.Fatalf("显式传的 provider_id=0 必须被认成「未指定」，实际 %v", p.ProviderID)
	}
}
