package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"llm-relay/internal/model"
)

func bptr(v bool) *bool { return &v }

// 布尔字段不能带 gorm 的 default 标签。
//
// 带默认值的字段值为零（false）时会被 GORM 从 INSERT 里整列省掉，转而去用列默认值，
// 于是「停用」这类操作点了保存也存不进去 —— 白名单里停用一个模型，它照样能被调用；
// 建渠道时勾掉「启用」，渠道照样是开的。这类静默失败排查成本极高（数据看着正常），
// 所以在这里逐个实体挡住，标签很容易被人「顺手补回来」。
func TestBooleanFieldsHaveNoGormDefault(t *testing.T) {
	cases := []struct {
		entity any
		field  string
	}{
		{model.ChannelGroup{}, "Enabled"},
		{model.Channel{}, "Enabled"},
		{model.ChannelModel{}, "Enabled"},
		{model.APIKey{}, "Enabled"},
		{model.ModelPricing{}, "Active"},
	}
	for _, c := range cases {
		field, ok := reflect.TypeOf(c.entity).FieldByName(c.field)
		if !ok {
			t.Errorf("%T 上找不到 %s", c.entity, c.field)
			continue
		}
		if tag := field.Tag.Get("gorm"); strings.Contains(tag, "default") {
			t.Errorf("%T.%s 不能有 gorm default（零值会被顶成列默认值），实际 tag 是 %q",
				c.entity, c.field, tag)
		}
	}
}

// 渠道白名单必须能区分「这次不动白名单」与「把白名单清空」。
//
// Models 退回值类型 []whitelistItem 时两者都是 len=0，于是「删掉最后一条模型」
// 会静默失效：界面提示保存成功，模型却还在。这条用例锁住指针语义。
func TestChannelModelsNilMeansUntouched(t *testing.T) {
	var omitted channelPayload
	if err := json.Unmarshal([]byte(`{"name":"c1"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.Models != nil {
		t.Error("没传 models 时应为 nil，表示这一项不动")
	}

	var cleared channelPayload
	if err := json.Unmarshal([]byte(`{"name":"c1","models":[]}`), &cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.Models == nil {
		t.Fatal("显式传空数组必须被识别为「清空白名单」，而不是「没传」")
	}
	if len(*cleared.Models) != 0 {
		t.Errorf("空数组应解出 0 条，实际 %d 条", len(*cleared.Models))
	}
}

// 白名单条目的规整规则：去空白、上游名留空则同名、enabled 默认 true。
func TestNormalizeWhitelist(t *testing.T) {
	items, err := normalizeWhitelist([]whitelistItem{
		{PublicName: "  deepseek-chat  "},
		{PublicName: "gpt-4o", UpstreamName: "openai/gpt-4o", Enabled: bptr(false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("应有 2 条，实际 %d", len(items))
	}
	if items[0].PublicName != "deepseek-chat" {
		t.Errorf("对外名两侧空白应去掉，实际 %q", items[0].PublicName)
	}
	if items[0].UpstreamName != "deepseek-chat" {
		t.Errorf("上游名留空时应与对外名相同，实际 %q", items[0].UpstreamName)
	}
	if !items[0].Enabled {
		t.Error("没传 enabled 时应默认启用")
	}
	if items[1].UpstreamName != "openai/gpt-4o" {
		t.Errorf("显式上游名应保留，实际 %q", items[1].UpstreamName)
	}
	if items[1].Enabled {
		t.Error("显式传 enabled=false 应生效")
	}
}

// 同一个渠道里对外名重复必须当场报错。
//
// 路由是按 (channel_id, public_name) 唯一索引定位的，重复会让唯一索引建不上，
// 迁移时表现为启动失败；在这里挡住比在数据库层报错好排查得多。
func TestNormalizeWhitelistRejectsDuplicates(t *testing.T) {
	if _, err := normalizeWhitelist([]whitelistItem{
		{PublicName: "m1"},
		{PublicName: "m1", UpstreamName: "other"},
	}); err == nil {
		t.Fatal("重复的对外名应该报错")
	}
}

// 空名称意味着这条白名单说不清是哪个模型，直接拒绝。
func TestNormalizeWhitelistRejectsEmptyName(t *testing.T) {
	if _, err := normalizeWhitelist([]whitelistItem{{PublicName: "   "}}); err == nil {
		t.Fatal("空白的对外名应该报错")
	}
}
