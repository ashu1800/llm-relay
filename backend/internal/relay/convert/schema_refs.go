package convert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// 本文件把出站请求里 tools / response_format 携带的 JSON Schema 引用整理成
// 「最保守」的形状：所有 $defs / definitions 提升到 schema 根级，
// 所有 $ref 一律写成 #/$defs/<名>。
//
// 为什么要做：OpenAI 兼容网关（Vercel AI Gateway 等）在把工具参数转给
// Gemini 之前要自己做一次 Google schema 转换，而它只接受「指向根级 $defs
// （或 definitions）直接子节点」的引用，报错原文是
// "Google schema conversion only supports references to direct children of
// root-level Defs or definitions"。MCP 服务器派生的工具 schema 里嵌套
// $defs、深层 #/$defs/A/properties/B 很常见，这类请求会被上游整条 400
// （AI_UnsupportedFunctionalityError），换渠道也无解 —— 只能在出站前改写。
//
// 原则：
//   - 语义保持：只挪位置、改引用，不删任何关键字。组合关键字、
//     additionalProperties 等交给上游转换器自己取舍，这里不扩大战线；
//   - 零扰动：没有引用问题的 schema 字节级原样返回，不重新序列化；
//   - 兜底：解析不了的引用（外部 URL、失效指针）换成宽松占位符 ——
//     丢一个参数的精确约束，好过整条请求被拒。
//
// 递归安全：深层引用的目标节点是「整体提升」为新定义而非就地展开，
// 引用环（A→B→A、$ref:"#") 提升后依旧靠 #/$defs 互指，不会无限展开。

// defContainerKeys 是 JSON Schema 定义容器的两种键名：
// 2020-12 草稿用 $defs，旧草稿用 definitions，语义相同。
var defContainerKeys = []string{"$defs", "definitions"}

// namedSchemaMapKeys 的值是「名字 -> schema」的映射，键是属性名/定义名而非
// schema 关键字 —— 属性完全可以叫 "$defs" 甚至 "$ref"（GitHub API 的 schema
// 就有叫 $ref 的属性）。落在这些映射里的同名键不是定义容器，绝不能当容器
// 提升，也不能被当引用改写。容器的直接子级（定义名所在层）同样适用。
var namedSchemaMapKeys = []string{
	"$defs", "definitions", "properties", "patternProperties", "dependentSchemas",
}

// schemaRefNeedles 是请求体粗筛用的子串。带引号是为了只命中 JSON 键与
// 字符串字面量，正文里出现 definitions 这类英文单词不至于触发全量解析。
var schemaRefNeedles = [][]byte{
	[]byte(`"$ref"`),
	[]byte(`"$defs"`),
	[]byte(`"definitions"`),
}

func containsAnyShard(body []byte, needles [][]byte) bool {
	for _, n := range needles {
		if bytes.Contains(body, n) {
			return true
		}
	}
	return false
}

// normalizeSchemaRefs 归一化请求体里的工具与结构化输出 schema 引用。
//
// 返回改写后的请求体与被改写的 schema 个数（0 表示字节级原样返回）。
// 入参必须是 OpenAI Chat 通用语形状（UpstreamRequest 的入口处调用）。
func normalizeSchemaRefs(body []byte) ([]byte, int) {
	if !containsAnyShard(body, schemaRefNeedles) {
		return body, 0
	}
	// 顶层用 RawMessage 浅解析：messages 等大块内容保持原字节，
	// 只有真正改写过的 tools / response_format 会重新序列化。
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return body, 0
	}
	n := 0
	if raw, ok := root["tools"]; ok {
		if out, k := normalizeToolsSchemas(raw); k > 0 {
			root["tools"] = out
			n += k
		}
	}
	if raw, ok := root["response_format"]; ok {
		if out, k := normalizeResponseFormatSchema(raw); k > 0 {
			root["response_format"] = out
			n += k
		}
	}
	if n == 0 {
		return body, 0
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body, 0
	}
	return out, n
}

// normalizeToolsSchemas 处理通用语的 tools 数组。工具是嵌套形状
// {type:function,function:{parameters}}；扁平 {parameters} 也顺手处理，
// 兜住非标准来源。非函数工具（web_search 等）没有参数，原样跳过。
func normalizeToolsSchemas(raw []byte) ([]byte, int) {
	if !containsAnyShard(raw, schemaRefNeedles) {
		return raw, 0
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return raw, 0
	}
	n := 0
	for i, tr := range list {
		if !containsAnyShard(tr, schemaRefNeedles) {
			continue
		}
		var t map[string]json.RawMessage
		if err := json.Unmarshal(tr, &t); err != nil {
			continue
		}
		k := 0
		if fnRaw, ok := t["function"]; ok {
			var fn map[string]json.RawMessage
			if err := json.Unmarshal(fnRaw, &fn); err == nil {
				if out, c := normalizeSchemaField(fn, "parameters"); c > 0 {
					fn["parameters"] = out
					if b, err := json.Marshal(fn); err == nil {
						t["function"] = b
						k = c
					}
				}
			}
		} else if out, c := normalizeSchemaField(t, "parameters"); c > 0 {
			t["parameters"] = out
			k = c
		}
		if k == 0 {
			continue
		}
		if b, err := json.Marshal(t); err == nil {
			list[i] = b
			n += k
		}
	}
	if n == 0 {
		return raw, 0
	}
	out, err := json.Marshal(list)
	if err != nil {
		return raw, 0
	}
	return out, n
}

// normalizeResponseFormatSchema 处理 Chat 形状的结构化输出：
// {"type":"json_schema","json_schema":{...,"schema":{...}}}。
func normalizeResponseFormatSchema(raw []byte) ([]byte, int) {
	if !containsAnyShard(raw, schemaRefNeedles) {
		return raw, 0
	}
	var rf map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rf); err != nil {
		return raw, 0
	}
	jsRaw, ok := rf["json_schema"]
	if !ok {
		return raw, 0
	}
	var js map[string]json.RawMessage
	if err := json.Unmarshal(jsRaw, &js); err != nil {
		return raw, 0
	}
	sRaw, ok := js["schema"]
	if !ok {
		return raw, 0
	}
	out, n := normalizeSchemaRaw(sRaw)
	if n == 0 {
		return raw, 0
	}
	js["schema"] = out
	if b, err := json.Marshal(js); err == nil {
		rf["json_schema"] = b
		if b2, err := json.Marshal(rf); err == nil {
			return b2, n
		}
	}
	return raw, 0
}

func normalizeSchemaField(m map[string]json.RawMessage, key string) ([]byte, int) {
	raw, ok := m[key]
	if !ok {
		return raw, 0
	}
	return normalizeSchemaRaw(raw)
}

// normalizeSchemaRaw 归一化单个 schema。没有引用问题的 schema 原字节返回。
func normalizeSchemaRaw(raw []byte) ([]byte, int) {
	if !containsAnyShard(raw, schemaRefNeedles) {
		return raw, 0
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw, 0
	}
	out, changed := hoistSchemaRefs(v)
	if !changed {
		return raw, 0
	}
	b, err := json.Marshal(out)
	if err != nil {
		return raw, 0
	}
	return b, 1
}

// hoistSchemaRefs 把单个 schema 里的引用整理成根级 $defs 形状。
// 返回整理后的 schema 与「是否发生了实际改写」。
func hoistSchemaRefs(schema any) (any, bool) {
	root, ok := schema.(map[string]any)
	if !ok {
		return schema, false
	}
	z := &schemaNormalizer{
		root:       root,
		defs:       map[string]any{},
		refRewrite: map[string]string{},
	}
	z.collect()
	z.plan(root, false)
	out, ok := z.apply(root, true, false).(map[string]any)
	if !ok {
		return schema, z.changed || z.renamed
	}
	// 定义节点也要走一遍：$ref:"#" 提升出的整根拷贝带着原样嵌套容器要清掉；
	// 被提升的深层节点若含解析不了的引用，占位替换要同步回定义表。
	for name, def := range z.defs {
		z.defs[name] = z.apply(def, false, false)
	}
	if len(z.defs) > 0 {
		out["$defs"] = z.defs
	}
	return out, z.changed || z.renamed
}

type schemaNormalizer struct {
	root map[string]any
	// defs 收集到的全部定义，键为（可能改名后的）定义名
	defs map[string]any
	// refRewrite 旧引用串 -> 新引用串。含恒等映射（已是根级直接子节点的引用），
	// 保证 apply 对已改写过的节点重走一遍是幂等的。
	refRewrite map[string]string
	changed    bool
	// renamed 记录定义改名（嵌套定义与根级/先注册者撞名时让位加后缀）。
	// 改名只体现在 defs 的键上，apply 感知不到，要单独记账。
	renamed bool
}

// schemaContainer 记录一个定义容器：path 是从容 schema 根到容器**含容器键**的
// 指针路径（用于拼出指向容器条目的 $ref），m 是容器本体。
type schemaContainer struct {
	path []string
	m    map[string]any
}

// collect 遍历整棵 schema，找出所有定义容器并把条目收进 defs。
// 根级容器先注册（保住原定义名），嵌套容器的同名条目让位加后缀。
func (z *schemaNormalizer) collect() {
	var cs []schemaContainer
	var walk func(node any, path []string, inNamed bool)
	walk = func(node any, path []string, inNamed bool) {
		m, ok := node.(map[string]any)
		if !ok {
			if list, ok := node.([]any); ok {
				for i, v := range list {
					walk(v, appendPath(path, strconv.Itoa(i)), false)
				}
			}
			return
		}
		if !inNamed {
			for _, key := range defContainerKeys {
				if c, ok := m[key].(map[string]any); ok {
					cs = append(cs, schemaContainer{path: appendPath(path, key), m: c})
				}
			}
		}
		for k, v := range m {
			// 子层的性质只在「当前层是 schema」时按键名推导；当前层本身是
			// 命名映射（键是属性名/定义名）时，子层一律是 schema。否则定义
			// 恰好叫 "properties" 时，它内部真正的 $defs 会被漏收。
			walk(v, appendPath(path, k), !inNamed && inNamedCollection(k))
		}
	}
	walk(z.root, nil, false)

	sort.SliceStable(cs, func(i, j int) bool { return len(cs[i].path) < len(cs[j].path) })
	for _, c := range cs {
		for name, def := range c.m {
			newName := z.uniqueName(name)
			if newName != name {
				z.renamed = true
			}
			z.defs[newName] = def
			ptr := pointerFor(appendPath(c.path, name))
			newPtr := "#/$defs/" + newName
			z.refRewrite[ptr] = newPtr
			z.refRewrite[newPtr] = newPtr
		}
	}
}

// plan 给整棵树里的每个 $ref 定改写目标（对原始树解析，先于任何改动）：
// 已是收集到的根级直接子节点 → 恒等或改指向；指向深层节点 → 整体提升为
// 新定义；解析不了 → 不登记，留给 apply 换占位符。
func (z *schemaNormalizer) plan(node any, inNamed bool) {
	switch t := node.(type) {
	case map[string]any:
		if !inNamed {
			if ref, ok := t["$ref"].(string); ok {
				z.planRef(ref)
			}
		}
		for k, v := range t {
			z.plan(v, !inNamed && inNamedCollection(k))
		}
	case []any:
		for _, v := range t {
			z.plan(v, false)
		}
	}
}

func (z *schemaNormalizer) planRef(ref string) {
	if _, done := z.refRewrite[ref]; done {
		return
	}
	if ref == "#" {
		// 自引用整个 schema：把整根提升为定义，引用改指向它。
		// 必须深拷贝 —— 原根节点稍后要挂上 $defs，直接引用会造出自包含环，
		// 序列化时栈溢出。
		name := z.uniqueName("root")
		z.defs[name] = deepCopyJSON(z.root)
		ptr := "#/$defs/" + name
		z.refRewrite[ref] = ptr
		z.refRewrite[ptr] = ptr
		return
	}
	target, ok := resolveJSONPointer(z.root, ref)
	if !ok {
		return
	}
	// 深层目标不展开、整体注册为定义：内部引用保持原样、由 apply 统一改写，
	// 天然免疫引用环。
	name := z.uniqueName(pointerBaseName(ref))
	z.defs[name] = target
	ptr := "#/$defs/" + name
	z.refRewrite[ref] = ptr
	z.refRewrite[ptr] = ptr
}

// apply 就地改写：引用按 plan 的登记表换值，嵌套定义容器删除（根级的删掉后
// 由 hoistSchemaRefs 统一重建），解析不了的引用就地降级为宽松 object。
// inNamed 为真表示这是「名字 -> schema」映射（properties 等），键是用户起的
// 属性名，不得当容器删、不得当引用改。
func (z *schemaNormalizer) apply(node any, atRoot, inNamed bool) any {
	switch t := node.(type) {
	case map[string]any:
		if !inNamed {
			if ref, ok := t["$ref"].(string); ok {
				if newRef, ok := z.refRewrite[ref]; ok {
					if newRef != ref {
						z.changed = true
					}
					t["$ref"] = newRef
				} else {
					// 内部指针失效或外部 URL：解不动。就地降级成宽松 object，
					// 其余键（description 等）保留 —— 丢一个参数的精确约束，
					// 好过让整条请求被上游 400。
					z.changed = true
					delete(t, "$ref")
					if _, has := t["type"]; !has {
						t["type"] = "object"
					}
				}
			}
			for _, key := range defContainerKeys {
				if _, ok := t[key]; ok {
					if !atRoot {
						z.changed = true
					}
					delete(t, key)
				}
			}
		}
		for k, v := range t {
			t[k] = z.apply(v, false, !inNamed && inNamedCollection(k))
		}
		return t
	case []any:
		for i, v := range t {
			t[i] = z.apply(v, false, false)
		}
		return t
	default:
		return node
	}
}

// inNamedCollection 判断该键下的映射是否是「名字 -> schema」：
// 定义容器本体与属性映射都是，它们的键不是 schema 关键字。
func inNamedCollection(key string) bool {
	for _, k := range namedSchemaMapKeys {
		if k == key {
			return true
		}
	}
	return false
}

func (z *schemaNormalizer) uniqueName(name string) string {
	if _, taken := z.defs[name]; !taken {
		return name
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s_%d", name, i)
		if _, taken := z.defs[cand]; !taken {
			return cand
		}
	}
}

// pointerFor 把指针路径拼回 $ref 串：["\u0024defs","A"] -> "#/$defs/A"。
func pointerFor(path []string) string {
	var sb strings.Builder
	sb.WriteByte('#')
	for _, tok := range path {
		sb.WriteByte('/')
		sb.WriteString(escapePointerToken(tok))
	}
	return sb.String()
}

// escapePointerToken 按 RFC 6901 转义指针段：~ -> ~0，/ -> ~1。
func escapePointerToken(tok string) string {
	tok = strings.ReplaceAll(tok, "~", "~0")
	tok = strings.ReplaceAll(tok, "/", "~1")
	return tok
}

// resolveJSONPointer 按 RFC 6901 在原始树上解引用。只认 "#..." 形式的
// 站内指针；外部 URL 一律解析失败。
func resolveJSONPointer(root any, ref string) (any, bool) {
	if !strings.HasPrefix(ref, "#") {
		return nil, false
	}
	rest := ref[1:]
	if rest == "" {
		return root, true
	}
	if !strings.HasPrefix(rest, "/") {
		return nil, false
	}
	cur := root
	for _, tok := range strings.Split(rest[1:], "/") {
		// 先解 ~1 再解 ~0：~0 编码的是 ~ 本身，顺序反了会把 ~01 解错
		tok = strings.ReplaceAll(tok, "~1", "/")
		tok = strings.ReplaceAll(tok, "~0", "~")
		switch t := cur.(type) {
		case map[string]any:
			v, ok := t[tok]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(t) {
				return nil, false
			}
			cur = t[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// pointerBaseName 从引用串取末段当提升定义的名字：#/$defs/A/properties/B -> B。
func pointerBaseName(ref string) string {
	rest := strings.TrimPrefix(ref, "#")
	toks := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if len(toks) == 0 {
		return "ref"
	}
	name := toks[len(toks)-1]
	name = strings.ReplaceAll(name, "~1", "/")
	name = strings.ReplaceAll(name, "~0", "~")
	return sanitizeDefName(name)
}

// sanitizeDefName 把程序生成的定义名收敛到安全字符集：
// 名字要进 $ref 指针，不能带 / 与 ~（否则指针语义就变了）。
func sanitizeDefName(name string) string {
	var sb strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	if sb.Len() == 0 {
		return "ref"
	}
	return sb.String()
}

func deepCopyJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = deepCopyJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = deepCopyJSON(val)
		}
		return out
	default:
		return t
	}
}

// appendPath 复制后再追加，避免多个兄弟节点共享底层数组互相踩路径。
func appendPath(path []string, tok string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = tok
	return out
}
