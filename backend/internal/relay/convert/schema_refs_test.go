package convert

import (
	"encoding/json"
	"fmt"
	"testing"
)

// 出站 schema 引用归一化：把嵌套 $defs / 深层 $ref 整理成「根级 $defs 直接
// 子节点」形状。背景是 OpenAI 兼容网关代转 Google 模型时只认这种引用，
// 其余整条请求 400（AI_UnsupportedFunctionalityError），换渠道也无解。

// 把单个 parameters schema 包进 Chat 请求体跑归一化，返回改写后的请求体、
// 归一化后的 schema 与被改写的 schema 个数。
func normalizeParams(t *testing.T, params string) ([]byte, map[string]any, int) {
	t.Helper()
	body := fmt.Sprintf(`{"messages":[],"tools":[{"type":"function","function":{"name":"f","parameters":%s}}]}`, params)
	out, n := normalizeSchemaRefs([]byte(body))
	return out, toolParamsOf(t, out, 0), n
}

// 取 Chat 请求体第 i 个工具的参数 schema。
func toolParamsOf(t *testing.T, body []byte, i int) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	tools, _ := req["tools"].([]any)
	if i >= len(tools) {
		t.Fatalf("tools[%d] 不存在，实际 %d 个", i, len(tools))
	}
	fn := asMap(asMap(tools[i])["function"])
	p := asMap(fn["parameters"])
	if p == nil {
		t.Fatalf("tools[%d] 没有 parameters", i)
	}
	return p
}

func refString(t *testing.T, schema map[string]any, path ...string) string {
	t.Helper()
	cur := schema
	for i, key := range path {
		next := asMap(cur[key])
		if next == nil {
			t.Fatalf("路径 %v 在第 %d 段（%s）断掉，实际 %v", path, i, key, cur[key])
		}
		cur = next
	}
	s := asString(cur["$ref"])
	if s == "" {
		t.Fatalf("路径 %v 的 $ref 不是字符串，实际 %v", path, cur["$ref"])
	}
	return s
}

// 没有引用问题的 schema 必须字节级原样返回 —— 包括「属性名恰好叫
// definitions / $ref」的陷阱：属性键不是定义容器，绝不能误伤。
func TestNormalizeSchemaRefsPassthrough(t *testing.T) {
	params := `{
		"type": "object",
		"properties": {
			"definitions": {"type": "string", "description": "属性名恰好叫 definitions"},
			"$ref": {"type": "string", "description": "属性名恰好叫 $ref"},
			"plain": {"type": "integer"}
		}
	}`
	wrapped := fmt.Sprintf(`{"messages":[],"tools":[{"type":"function","function":{"name":"f","parameters":%s}}]}`, params)
	out, n := normalizeSchemaRefs([]byte(wrapped))
	if n != 0 {
		t.Fatalf("无害 schema 不应被改写，实际改了 %d 个", n)
	}
	if string(out) != wrapped {
		t.Errorf("无害 schema 必须字节级原样返回")
	}
	// 陷阱属性一个都不能少
	schema := toolParamsOf(t, out, 0)
	props := asMap(schema["properties"])
	if props == nil || len(props) != 3 {
		t.Fatalf("三个属性都应保留，实际 %v", props)
	}
	if asMap(props["definitions"]) == nil || asMap(props["$ref"]) == nil {
		t.Errorf("叫 definitions / $ref 的属性不能被当容器或引用处理")
	}
}

// 根级 $defs + 简单引用本来就是合规形状（Zod 生成物长这样），必须零改动。
func TestNormalizeSchemaRefsPerfectRootDefs(t *testing.T) {
	params := `{
		"type": "object",
		"$defs": {
			"A": {"type": "object", "properties": {"b": {"$ref": "#/$defs/B"}}},
			"B": {"type": "string"}
		},
		"properties": {"a": {"$ref": "#/$defs/A"}}
	}`
	wrapped := fmt.Sprintf(`{"messages":[],"tools":[{"type":"function","function":{"name":"f","parameters":%s}}]}`, params)
	out, n := normalizeSchemaRefs([]byte(wrapped))
	if n != 0 {
		t.Fatalf("合规 schema 不应被改写，实际改了 %d 个", n)
	}
	if string(out) != wrapped {
		t.Errorf("合规 schema 必须字节级原样返回")
	}
}

// 嵌套在属性里的定义容器要提升到根级，局部引用随之改写。
func TestNormalizeSchemaRefsNestedDefs(t *testing.T) {
	_, schema, n := normalizeParams(t, `{
		"type": "object",
		"$defs": {"Address": {"type": "object", "properties": {"city": {"type": "string"}}}},
		"properties": {
			"home": {"$ref": "#/$defs/Address"},
			"work": {
				"type": "object",
				"$defs": {"Phone": {"type": "string", "description": "电话"}},
				"properties": {"cell": {"$ref": "#/properties/work/$defs/Phone"}}
			}
		}
	}`)
	if n != 1 {
		t.Fatalf("应改写 1 个 schema，实际 %d", n)
	}
	defs := asMap(schema["$defs"])
	if defs == nil || asMap(defs["Address"]) == nil || asMap(defs["Phone"]) == nil {
		t.Fatalf("根级 $defs 应同时有 Address 与 Phone，实际 %v", defs)
	}
	if asString(asMap(defs["Phone"])["description"]) != "电话" {
		t.Errorf("提升的定义内容应原样保留")
	}
	work := asMap(asMap(schema["properties"])["work"])
	if _, ok := work["$defs"]; ok {
		t.Errorf("嵌套的 $defs 必须删除，实际还留着")
	}
	if got := refString(t, schema, "properties", "home"); got != "#/$defs/Address" {
		t.Errorf("根级引用应保持指向 Address，实际 %s", got)
	}
	if got := refString(t, schema, "properties", "work", "properties", "cell"); got != "#/$defs/Phone" {
		t.Errorf("深层局部引用应改写为 #/$defs/Phone，实际 %s", got)
	}
}

// 指向定义内部深处的引用（#/$defs/A/properties/B）解析不了，
// 目标节点要整体提升为新定义。
func TestNormalizeSchemaRefsDeepPointer(t *testing.T) {
	_, schema, n := normalizeParams(t, `{
		"type": "object",
		"$defs": {"A": {"type": "object", "properties": {
			"B": {"type": "string", "description": "深层目标"}
		}}},
		"properties": {"x": {"$ref": "#/$defs/A/properties/B"}}
	}`)
	if n != 1 {
		t.Fatalf("应改写 1 个 schema，实际 %d", n)
	}
	defs := asMap(schema["$defs"])
	b := asMap(defs["B"])
	if b == nil || asString(b["description"]) != "深层目标" {
		t.Fatalf("深层目标应整体提升为根级定义 B，实际 %v", defs)
	}
	if got := refString(t, schema, "properties", "x"); got != "#/$defs/B" {
		t.Errorf("深层引用应改写为 #/$defs/B，实际 %s", got)
	}
	// 原位置的内容不要求搬走（重复定义无害），但 A 本体不能被破坏
	if asMap(asMap(asMap(defs["A"])["properties"])["B"]) == nil {
		t.Errorf("定义 A 的原有内容应保留")
	}
}

// 旧草稿的 definitions 容器与引用写法同样要归一化成 $defs。
func TestNormalizeSchemaRefsDefinitionsAlias(t *testing.T) {
	_, schema, n := normalizeParams(t, `{
		"type": "object",
		"definitions": {"Id": {"type": "string"}},
		"properties": {"id": {"$ref": "#/definitions/Id"}}
	}`)
	if n != 1 {
		t.Fatalf("应改写 1 个 schema，实际 %d", n)
	}
	defs := asMap(schema["$defs"])
	if defs == nil || asMap(defs["Id"]) == nil {
		t.Fatalf("definitions 应提升为根级 $defs，实际 %v", defs)
	}
	if _, ok := schema["definitions"]; ok {
		t.Errorf("definitions 键应被 $defs 取代")
	}
	if got := refString(t, schema, "properties", "id"); got != "#/$defs/Id" {
		t.Errorf("引用应改写为 #/$defs/Id，实际 %s", got)
	}
}

// 递归不能炸：A↔B 互指是合规形状应零改动；$ref:"#" 的整根自引用
// 要把根提升为定义（深拷贝，避免自包含环）。
func TestNormalizeSchemaRefsRecursive(t *testing.T) {
	perfect := `{
		"type": "object",
		"$defs": {
			"A": {"type": "object", "properties": {"b": {"$ref": "#/$defs/B"}}},
			"B": {"type": "object", "properties": {"a": {"$ref": "#/$defs/A"}}}
		},
		"properties": {"root": {"$ref": "#/$defs/A"}}
	}`
	wrapped := fmt.Sprintf(`{"messages":[],"tools":[{"type":"function","function":{"name":"f","parameters":%s}}]}`, perfect)
	out, n := normalizeSchemaRefs([]byte(wrapped))
	if n != 0 || string(out) != wrapped {
		t.Fatalf("互指的合规递归 schema 应零改动，实际 n=%d", n)
	}

	_, schema, _ := normalizeParams(t, `{
		"type": "object",
		"properties": {"next": {"$ref": "#"}, "name": {"type": "string"}}
	}`)
	root := asMap(asMap(schema["$defs"])["root"])
	if root == nil {
		t.Fatalf("整根自引用应把根提升为定义 root，实际 %v", schema["$defs"])
	}
	if _, ok := root["$defs"]; ok {
		t.Errorf("提升出的定义内部不能还挂着 $defs（会自包含环）")
	}
	if got := refString(t, schema, "properties", "next"); got != "#/$defs/root" {
		t.Errorf("自引用应改写为 #/$defs/root，实际 %s", got)
	}
	if got := refString(t, root, "properties", "next"); got != "#/$defs/root" {
		t.Errorf("定义内部的递归引用也要改写，实际 %s", got)
	}
	if asMap(asMap(root["properties"])["name"]) == nil {
		t.Errorf("根定义应携带原有属性")
	}
}

// 解析不了的引用（外部 URL、失效指针）就地降级成宽松 object，
// 其余键保留，整条请求不能被拖累。
func TestNormalizeSchemaRefsBrokenRefs(t *testing.T) {
	_, schema, n := normalizeParams(t, `{
		"type": "object",
		"properties": {
			"x": {"$ref": "https://example.com/x.schema.json", "description": "外部引用"},
			"y": {"$ref": "#/$defs/Missing"},
			"z": {"type": "string"}
		}
	}`)
	if n != 1 {
		t.Fatalf("应改写 1 个 schema，实际 %d", n)
	}
	props := asMap(schema["properties"])
	x := asMap(props["x"])
	if x == nil || x["$ref"] != nil || asString(x["type"]) != "object" {
		t.Fatalf("外部引用应降级为 object 且去掉 $ref，实际 %v", x)
	}
	if asString(x["description"]) != "外部引用" {
		t.Errorf("降级时应保留 description，实际 %v", x)
	}
	y := asMap(props["y"])
	if y == nil || y["$ref"] != nil || asString(y["type"]) != "object" {
		t.Fatalf("失效指针应降级为 object，实际 %v", y)
	}
	if asMap(props["z"]) == nil {
		t.Errorf("无关属性不能被动到")
	}
}

// 结构化输出（response_format.json_schema.schema）同样要归一化。
func TestNormalizeSchemaRefsResponseFormat(t *testing.T) {
	body := []byte(`{
		"messages": [],
		"response_format": {"type": "json_schema", "json_schema": {
			"name": "out",
			"strict": true,
			"schema": {
				"type": "object",
				"definitions": {"Item": {"type": "object", "properties": {"id": {"type": "string"}}}},
				"properties": {"first": {"$ref": "#/definitions/Item"}}
			}
		}}
	}`)
	out, n := normalizeSchemaRefs(body)
	if n != 1 {
		t.Fatalf("应改写 1 个 schema，实际 %d", n)
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	rf := asMap(req["response_format"])
	js := asMap(rf["json_schema"])
	schema := asMap(js["schema"])
	if schema == nil {
		t.Fatal("response_format 结构不应被破坏")
	}
	if asString(js["name"]) != "out" || asBool(js["strict"]) != true {
		t.Errorf("json_schema 的 name/strict 应原样保留，实际 %v", js)
	}
	defs := asMap(schema["$defs"])
	if defs == nil || asMap(defs["Item"]) == nil {
		t.Fatalf("definitions 应提升为根级 $defs，实际 %v", defs)
	}
	if got := refString(t, schema, "properties", "first"); got != "#/$defs/Item" {
		t.Errorf("引用应改写为 #/$defs/Item，实际 %s", got)
	}
}

// 扁平形状的工具（无 function 包一层）也要兜住。
func TestNormalizeSchemaRefsFlatTool(t *testing.T) {
	body := []byte(`{
		"messages": [],
		"tools": [{"type": "function", "name": "f", "parameters": {
			"type": "object",
			"definitions": {"Tag": {"type": "string"}},
			"properties": {"tag": {"$ref": "#/definitions/Tag"}}
		}}]
	}`)
	out, n := normalizeSchemaRefs(body)
	if n != 1 {
		t.Fatalf("应改写 1 个 schema，实际 %d", n)
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	tool := asMap(req["tools"].([]any)[0])
	schema := asMap(tool["parameters"])
	if asMap(asMap(schema["$defs"])["Tag"]) == nil {
		t.Fatalf("扁平工具的定义应提升到根级，实际 %v", schema)
	}
	if got := refString(t, schema, "properties", "tag"); got != "#/$defs/Tag" {
		t.Errorf("引用应改写，实际 %s", got)
	}
}

// 全链路：UpstreamRequest 归一化通用语后再转 Responses 出站，
// 拍平后的 parameters 也必须是干净的根级 $defs 形状。
func TestUpstreamRequestNormalizesToolRefs(t *testing.T) {
	body := []byte(`{
		"model": "gemini-3.8-flash",
		"stream": false,
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "f", "description": "d", "parameters": {
			"type": "object",
			"properties": {"w": {"type": "object",
				"$defs": {"P": {"type": "string", "description": "嵌套定义"}},
				"properties": {"p": {"$ref": "#/properties/w/$defs/P"}}}
			}
		}}}]
	}`)
	_, out, err := UpstreamRequest("openai-chat", "/v1/chat/completions", body, "gemini-3.8-flash")
	if err != nil {
		t.Fatal(err)
	}
	schema := toolParamsOf(t, out, 0)
	defs := asMap(schema["$defs"])
	if defs == nil || asMap(defs["P"]) == nil {
		t.Fatalf("出站前应完成提升，实际 %v", schema)
	}
	if got := refString(t, schema, "properties", "w", "properties", "p"); got != "#/$defs/P" {
		t.Errorf("引用应改写为 #/$defs/P，实际 %s", got)
	}

	// 同一份通用语走 Responses 出站：拍平工具后依然是干净形状
	responsesBody, err := OpenAIChatToResponsesRequest(out)
	if err != nil {
		t.Fatal(err)
	}
	var rreq map[string]any
	if err := json.Unmarshal(responsesBody, &rreq); err != nil {
		t.Fatal(err)
	}
	tool := asMap(rreq["tools"].([]any)[0])
	rschema := asMap(tool["parameters"])
	if rschema == nil {
		t.Fatal("Responses 工具应携带 parameters")
	}
	if asMap(asMap(rschema["$defs"])["P"]) == nil {
		t.Fatalf("Responses 出站的 parameters 也应是根级 $defs 形状，实际 %v", rschema)
	}
	if _, ok := asMap(rschema["properties"])["w"].(map[string]any)["$defs"]; ok {
		t.Errorf("Responses 出站不应残留嵌套 $defs")
	}
}

// 非 Chat 路径（embeddings）不做任何处理。
func TestUpstreamRequestSkipsNonChatPath(t *testing.T) {
	body := []byte(`{"model": "x", "input": ["definitions"]}`)
	_, out, err := UpstreamRequest("openai-embeddings", "/v1/embeddings", body, "x")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) {
		t.Errorf("embeddings 路径应原样返回")
	}
}

// 同名定义撞车：根级容器先注册保住原名，嵌套同名条目让位加后缀，
// 两边的引用各自归位、互不串线。
func TestNormalizeSchemaRefsNameCollision(t *testing.T) {
	_, schema, n := normalizeParams(t, `{
		"type": "object",
		"$defs": {"Node": {"type": "string"}},
		"properties": {
			"tree": {
				"type": "object",
				"$defs": {"Node": {"type": "object", "properties": {"id": {"type": "string"}}}},
				"properties": {"leaf": {"$ref": "#/properties/tree/$defs/Node"}}
			},
			"root": {"$ref": "#/$defs/Node"}
		}
	}`)
	if n != 1 {
		t.Fatalf("应改写 1 个 schema，实际 %d", n)
	}
	defs := asMap(schema["$defs"])
	node := asMap(defs["Node"])
	if node == nil || asString(node["type"]) != "string" {
		t.Fatalf("根级 Node 应原样保留，实际 %v", defs["Node"])
	}
	nested := asMap(defs["Node_2"])
	if nested == nil || asMap(nested["properties"]) == nil {
		t.Fatalf("嵌套同名定义应提升为 Node_2，实际 %v", defs)
	}
	if got := refString(t, schema, "properties", "root"); got != "#/$defs/Node" {
		t.Errorf("根级引用应保持指向 Node，实际 %s", got)
	}
	if got := refString(t, schema, "properties", "tree", "properties", "leaf"); got != "#/$defs/Node_2" {
		t.Errorf("嵌套引用应改写为 #/$defs/Node_2，实际 %s", got)
	}
}

// 定义名恰好叫 properties 且内部还有真正的嵌套容器：命名映射上下文
// 不许多传一层，否则内层 $defs 漏收、内部引用漏改写（回归用）。
func TestNormalizeSchemaRefsDefNamedProperties(t *testing.T) {
	_, schema, n := normalizeParams(t, `{
		"type": "object",
		"$defs": {
			"properties": {
				"type": "object",
				"$defs": {"Z": {"type": "string"}},
				"properties": {"z": {"$ref": "#/$defs/properties/$defs/Z"}}
			}
		},
		"properties": {"a": {"$ref": "#/$defs/properties"}}
	}`)
	if n != 1 {
		t.Fatalf("应改写 1 个 schema，实际 %d", n)
	}
	defs := asMap(schema["$defs"])
	inner := asMap(defs["properties"])
	if inner == nil {
		t.Fatalf("名叫 properties 的定义应原样保留在根级，实际 %v", defs)
	}
	if _, ok := inner["$defs"]; ok {
		t.Errorf("定义内部的嵌套容器应提升删除，实际还留着")
	}
	if asMap(defs["Z"]) == nil {
		t.Errorf("内层定义 Z 应提升到根级，实际 %v", defs)
	}
	if got := refString(t, defs, "properties", "properties", "z"); got != "#/$defs/Z" {
		t.Errorf("定义内部的引用应改写为 #/$defs/Z，实际 %s", got)
	}
	if got := refString(t, schema, "properties", "a"); got != "#/$defs/properties" {
		t.Errorf("外层引用应保持指向该定义，实际 %s", got)
	}
}

// JSON Pointer 转义：属性名带 / 时引用写作 a~1b，解析要还原、
// 程序生成的定义名要消毒（不能把 / 带进新的指针里）。
func TestNormalizeSchemaRefsEscapedPointer(t *testing.T) {
	_, schema, n := normalizeParams(t, `{
		"type": "object",
		"properties": {
			"a/b": {"type": "string", "description": "斜杠属性"},
			"x": {"$ref": "#/properties/a~1b"}
		}
	}`)
	if n != 1 {
		t.Fatalf("应改写 1 个 schema，实际 %d", n)
	}
	hoisted := asMap(asMap(schema["$defs"])["a_b"])
	if hoisted == nil || asString(hoisted["description"]) != "斜杠属性" {
		t.Fatalf("转义指向的深层目标应提升为定义 a_b，实际 %v", schema["$defs"])
	}
	if asMap(asMap(schema["properties"])["a/b"]) == nil {
		t.Errorf("原属性 a/b 不能被动到")
	}
	if got := refString(t, schema, "properties", "x"); got != "#/$defs/a_b" {
		t.Errorf("引用应改写为消毒后的 #/$defs/a_b，实际 %s", got)
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}
