package api

import (
	"reflect"
	"testing"

	"llm-relay/internal/model"
)

// 密钥分组白名单的归一化规则：名字换 ID、去空白、去重、解析失败即报错。
//
// 这套规则守护的是「分组改名不再打断密钥」：库里从今以后只有 ID，
// 名字条目（老客户端、手工调用）在保存前就被换掉；换不掉的当场报错，
// 而不是先收下、调用时再 403。
func TestNormalizeGroupRefList(t *testing.T) {
	// resolve 测试替身：「生产」→7、「备用」→3；「7」「3」是合法 ID，
	// 其余（包括数字形态的 9）一律解析失败
	resolve := func(item string) (uint, error) {
		switch item {
		case "生产":
			return 7, nil
		case "备用":
			return 3, nil
		case "7":
			return 7, nil
		case "3":
			return 3, nil
		}
		return 0, refNotFoundErr("分组不存在: " + item)
	}

	cases := []struct {
		name string
		in   model.StringList
		want model.StringList
	}{
		{"空表原样返回", nil, nil},
		{"名字条目换成 ID", model.StringList{"生产", "备用"}, model.StringList{"7", "3"}},
		{"名字与 ID 指向同一分组时去重", model.StringList{"生产", "7"}, model.StringList{"7"}},
		{"重复条目去重", model.StringList{"备用", "备用"}, model.StringList{"3"}},
		{"空白条目被清掉", model.StringList{"  ", "生产", ""}, model.StringList{"7"}},
		{"两侧空白去掉", model.StringList{"  生产  "}, model.StringList{"7"}},
	}
	for _, tc := range cases {
		got, err := normalizeGroupRefList(tc.in, resolve)
		if err != nil {
			t.Errorf("%s: 不应报错，实际 %v", tc.name, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: 期望 %v，实际 %v", tc.name, tc.want, got)
		}
	}

	// 解析失败必须报错：静默丢条目会让人以为整份白名单都生效了
	for _, bad := range []model.StringList{{"不存在的组"}, {"9"}, {"生产", "不存在的组"}} {
		if _, err := normalizeGroupRefList(bad, resolve); err == nil {
			t.Errorf("条目 %v 解析失败时必须报错", bad)
		}
	}
}

type refNotFoundErr string

func (e refNotFoundErr) Error() string { return string(e) }
