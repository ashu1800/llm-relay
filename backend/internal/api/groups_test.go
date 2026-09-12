package api

import (
	"encoding/json"
	"testing"
)

// 分组编辑的字段全部用指针接收，这里锁住三件事：
// 「没传」不动、「传 false/空串」要生效。
//
// 退回值类型时布尔字段没有非零值可判，is_default / enabled 永远进不了 updates ——
// 传了返回 200 却不落库，开关拨过去又弹回来，看起来像「保存失败但没报错」。
func TestGroupUpdatePayloadPointerSemantics(t *testing.T) {
	var omitted groupUpdatePayload
	if err := json.Unmarshal([]byte(`{"name":"g1"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.IsDefault != nil || omitted.Enabled != nil || omitted.Remark != nil {
		t.Error("没传的字段应保持 nil，表示这一项不动")
	}

	var explicit groupUpdatePayload
	if err := json.Unmarshal(
		[]byte(`{"is_default":false,"enabled":false,"remark":""}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.IsDefault == nil || *explicit.IsDefault {
		t.Error("显式传 is_default=false 必须被识别")
	}
	if explicit.Enabled == nil || *explicit.Enabled {
		t.Error("显式传 enabled=false 必须被识别")
	}
	if explicit.Remark == nil || *explicit.Remark != "" {
		t.Error("显式传空备注必须被识别（用于清空备注）")
	}
}
