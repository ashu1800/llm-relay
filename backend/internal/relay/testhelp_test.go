package relay

import (
	"encoding/json"
	"testing"
	"time"

	"llm-relay/internal/model"
)

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("解析测试载荷失败: %v", err)
	}
	return m
}

func mustSlots(t *testing.T, s string) model.JSONList {
	t.Helper()
	var l model.JSONList
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		t.Fatalf("解析时段配置失败: %v", err)
	}
	return l
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("解析时间失败: %v", err)
	}
	return tm
}
