package api

import (
	"encoding/json"
	"strings"
	"testing"

	"llm-relay/internal/model"
)

// 这个测试是针对一个真实踩过的坑：
// Channel.APIKeyEnc 与 APIKey.KeyHash 都带 json:"-"（避免泄漏给管理接口），
// 直接序列化实体做备份会静默丢掉它们，恢复出来的渠道全都调不通。
// 导出必须走带显式字段的 DTO，这里守住这个行为。
func TestBackupExportKeepsSecrets(t *testing.T) {
	ch := model.Channel{
		Name: "上游A", Protocol: "openai-chat", BaseURL: "https://example.com/v1",
		APIKeyEnc: "CIPHERTEXT-BLOB", APIKeyHint: "sk-ab...yz",
	}
	k := model.APIKey{Name: "客户端", KeyPrefix: "sk-local-12", KeyHash: "HASH-BLOB", Enabled: true}

	raw, err := json.Marshal(struct {
		Channels []channelExport `json:"channels"`
		APIKeys  []apiKeyExport  `json:"api_keys"`
	}{
		Channels: []channelExport{{Channel: ch, APIKeyEnc: ch.APIKeyEnc}},
		APIKeys:  []apiKeyExport{{APIKey: k, KeyHash: k.KeyHash}},
	})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	s := string(raw)

	for _, want := range []string{
		`"api_key_enc":"CIPHERTEXT-BLOB"`,
		`"key_hash":"HASH-BLOB"`,
		`"name":"上游A"`,
		`"api_key_hint":"sk-ab...yz"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("备份里缺少 %s\n实际内容: %s", want, s)
		}
	}
}

// 直接序列化实体会丢字段——这正是必须用 DTO 的原因，写成断言避免有人改回去。
func TestRawEntityWouldDropSecrets(t *testing.T) {
	ch := model.Channel{Name: "x", APIKeyEnc: "CIPHERTEXT-BLOB"}
	raw, err := json.Marshal(ch)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "CIPHERTEXT-BLOB") {
		t.Fatal("实体的 APIKeyEnc 不应出现在 JSON 里；若此断言失败，说明 DTO 已无必要")
	}
}
