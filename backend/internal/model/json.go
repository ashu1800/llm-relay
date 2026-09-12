package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONMap 映射到 PostgreSQL 的 jsonb 列，用于存放可变结构（额外配置、自定义映射等）。
type JSONMap map[string]any

// Value 实现 driver.Valuer。
func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// Scan 实现 sql.Scanner。
func (m *JSONMap) Scan(src any) error {
	return scanJSON(src, m)
}

// JSONList 映射到 jsonb 数组列（可用时段等）。
type JSONList []map[string]any

// Value 实现 driver.Valuer。
func (l JSONList) Value() (driver.Value, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(l)
}

// Scan 实现 sql.Scanner。
func (l *JSONList) Scan(src any) error {
	return scanJSON(src, l)
}

// StringList 映射到 jsonb 字符串数组（模型白名单等）。
type StringList []string

// Value 实现 driver.Valuer。
func (l StringList) Value() (driver.Value, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(l)
}

// Scan 实现 sql.Scanner。
func (l *StringList) Scan(src any) error {
	return scanJSON(src, l)
}

func scanJSON(src any, dst any) error {
	if src == nil {
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("无法把 %T 解析为 JSON", src)
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}
