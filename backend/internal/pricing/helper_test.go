package pricing

import "encoding/json"

// jsonUnmarshal 抽出成变量便于测试文件集中引用编码包。
func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}
