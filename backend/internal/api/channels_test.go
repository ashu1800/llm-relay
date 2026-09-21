package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"llm-relay/internal/model"
)

func bptr(v bool) *bool { return &v }

// 布尔字段不能带 gorm 的 default 标签。
//
// 带默认值的字段值为零（false）时会被 GORM 从 INSERT 里整列省掉，转而去用列默认值，
// 于是「停用」这类操作点了保存也存不进去 —— 白名单里停用一个模型，它照样能被调用；
// 建渠道时勾掉「启用」，渠道照样是开的。这类静默失败排查成本极高（数据看着正常），
// 所以在这里逐个实体挡住，标签很容易被人「顺手补回来」。
//
// 同一类坑在数值字段上也会出现：ChannelModel.Multiplier（固定倍率）刻意不带
// gorm default，0 表示「没配」，由代码归一到 1 —— 加了列默认值的话，
// 「填了 0.5 却按 1 算」在界面上完全看不出来。
func TestBooleanFieldsHaveNoGormDefault(t *testing.T) {
	cases := []struct {
		entity any
		field  string
	}{
		{model.ChannelGroup{}, "Enabled"},
		{model.Channel{}, "Enabled"},
		{model.ChannelModel{}, "Enabled"},
		{model.APIKey{}, "Enabled"},
		// 兜底映射的标记：带默认值时「精确命中的请求」会被顶成兜底，
		// 日志上就再也分不清哪些请求其实没命中白名单
		{model.RequestLog{}, "FallbackMapped"},
		{model.RequestLog{}, "UsageEstimated"},
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

// 清零检测是「输入价莫名变 0」事故的服务端留痕（见 replaceChannelModels），
// 误报（原值就是 0）与漏报（改值没清零）都会毁掉这条排查线索。
func TestWipedPrices(t *testing.T) {
	oldRow := model.ChannelModel{
		InputPer1M:      decimal.NewFromFloat(8),
		OutputPer1M:     decimal.NewFromFloat(28),
		CacheReadPer1M:  decimal.NewFromFloat(2),
		CacheWritePer1M: decimal.Zero, // 原本就没配，合法
	}

	t.Run("被清零的字段逐个点名并带旧值", func(t *testing.T) {
		next := oldRow
		next.InputPer1M = decimal.Zero
		wiped := wipedPrices(oldRow, next)
		if len(wiped) != 1 || !strings.Contains(wiped[0], "输入单价") || !strings.Contains(wiped[0], "8") {
			t.Fatalf("应只点名「输入单价 8→0」，实际 %v", wiped)
		}
	})

	t.Run("原值就是 0 的不算清零", func(t *testing.T) {
		next := oldRow
		next.CacheWritePer1M = decimal.Zero
		if wiped := wipedPrices(oldRow, next); len(wiped) != 0 {
			t.Fatalf("缓存写价原本就是 0，不该报，实际 %v", wiped)
		}
	})

	t.Run("改值但不清零的不报", func(t *testing.T) {
		next := oldRow
		next.OutputPer1M = decimal.NewFromFloat(30)
		if wiped := wipedPrices(oldRow, next); len(wiped) != 0 {
			t.Fatalf("正常改价不该报，实际 %v", wiped)
		}
	})

	t.Run("全部归零时只点名原本有价的字段", func(t *testing.T) {
		next := model.ChannelModel{PublicName: oldRow.PublicName}
		wiped := wipedPrices(oldRow, next)
		if len(wiped) != 3 {
			t.Fatalf("应点名 3 个原本非零的字段，实际 %v", wiped)
		}
	})
}

// 默认模型映射的保存校验。
//
// 前端也会拦，但后端必须也拦：接口不是只有界面在用（脚本、备份导入都在打）。
// 拦的是两种配不上套的状态 ——
//
//  1. 开关开着但模型名空：这个组合在路由侧等同于没开（见 relay.ChannelDefaultModel），
//     保存成功等于给用户一个「配了但没生效」的假象，而表现又是那个 2ms 的 502。
//  2. 指定的模型不在该渠道白名单里：候选查询的 JOIN 匹配不到它，兜底同样不生效 ——
//     而且这条更隐蔽，因为白名单看起来是配好的，只是没配这个名字。
//
// 注意「开关关着且没填模型名」是合法状态（大多数渠道就是如此），不能拦。
func TestValidateDefaultModel(t *testing.T) {
	cases := []struct {
		name    string
		extra   map[string]any
		models  []string
		wantErr bool
	}{
		{"没配（合法）", nil, []string{"deepseek-chat"}, false},
		{"开关关着（合法）", map[string]any{"default_model_enabled": false}, []string{"deepseek-chat"}, false},
		{"开关关着且没填模型名（合法）", map[string]any{"default_model_enabled": false, "default_model": ""}, []string{"deepseek-chat"}, false},
		{"配齐且在白名单里（合法）", map[string]any{"default_model_enabled": true, "default_model": "deepseek-chat"}, []string{"deepseek-chat", "deepseek-reasoner"}, false},
		{"开关开着但没填模型名", map[string]any{"default_model_enabled": true}, []string{"deepseek-chat"}, true},
		{"开关开着但模型名为空串", map[string]any{"default_model_enabled": true, "default_model": ""}, []string{"deepseek-chat"}, true},
		{"开关开着但模型名只有空白", map[string]any{"default_model_enabled": true, "default_model": "  "}, []string{"deepseek-chat"}, true},
		{"模型名不在白名单里", map[string]any{"default_model_enabled": true, "default_model": "gpt-4o"}, []string{"deepseek-chat"}, true},
		{"白名单为空但开了开关", map[string]any{"default_model_enabled": true, "default_model": "deepseek-chat"}, nil, true},
		{"模型名两侧空白应剪掉再比", map[string]any{"default_model_enabled": true, "default_model": " deepseek-chat "}, []string{"deepseek-chat"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateDefaultModel(c.extra, c.models)
			if c.wantErr && err == nil {
				t.Fatal("应该报错")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("不该报错，实际 %v", err)
			}
		})
	}
}
