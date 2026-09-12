package convert

import (
	"encoding/json"
	"testing"
)

// 函数声明的 schema 过滤：只留 Gemini 认识的字段，且 required 不能引用被丢掉的属性 ——
// required 里写了不存在的字段，Gemini 会直接 400，整条请求失败。
func TestGeminiSchemaSanitize(t *testing.T) {
	body := []byte(`{
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {
			"name": "f",
			"parameters": {
				"type": "object",
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"additionalProperties": false,
				"properties": {
					"city": {"type": "string", "description": "城市"},
					"weird": true,
					"obj": {"type": "object", "additionalProperties": {"type": "string"}, "properties": {"inner": {"type": "string"}}}
				},
				"required": ["city", "weird", "obj"]
			}
		}}]
	}`)
	out, err := OpenAIChatToGeminiRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	decls, _ := asMap(got["tools"].([]any)[0])["functionDeclarations"].([]any)
	schema := asMap(asMap(decls[0])["parameters"])

	for _, drop := range []string{"$schema", "additionalProperties"} {
		if _, ok := schema[drop]; ok {
			t.Errorf("%s 必须过滤掉（Gemini 不认，会 400）", drop)
		}
	}
	props := asMap(schema["properties"])
	if props["city"] == nil || props["obj"] == nil {
		t.Fatalf("properties 应保留，实际 %v", props)
	}
	// 嵌套层的 additionalProperties 同样要清掉
	if _, ok := asMap(props["obj"])["additionalProperties"]; ok {
		t.Error("嵌套层的 additionalProperties 也要过滤")
	}
	// weird 的属性定义不是对象（JSON Schema 的布尔形式），会被丢掉，
	// 那么 required 里就不能再留着它
	req, _ := schema["required"].([]any)
	for _, item := range req {
		if asString(item) == "weird" {
			t.Errorf("required 里不能保留被丢掉的属性，实际 %v", req)
		}
	}
	if len(req) != 2 {
		t.Errorf("required 应只剩 city 与 obj，实际 %v", req)
	}
}
