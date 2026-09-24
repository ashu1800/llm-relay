package config

import (
	"strings"
	"testing"
	"time"
)

// 管理密钥的强度校验在启动时就地卡死：公网上的登录接口面对无限次自动化
// 尝试，弱密钥哪怕有限流也守不住 —— 所以这里选择拒绝启动而不是警告。

func TestValidateAdminKey(t *testing.T) {
	base := func(key string) *Config {
		c := Default()
		c.Security.AdminKey = key
		return c
	}

	t.Run("占位值拒绝启动", func(t *testing.T) {
		err := base("CHANGE_ME").validate()
		if err == nil || !strings.Contains(err.Error(), "CHANGE_ME") {
			t.Fatalf("占位值应被拒绝，实际 %v", err)
		}
	})

	t.Run("过短密钥拒绝启动", func(t *testing.T) {
		err := base("short-key").validate()
		if err == nil || !strings.Contains(err.Error(), "16") {
			t.Fatalf("过短密钥应被拒绝，实际 %v", err)
		}
	})

	t.Run("与加密主密钥相同拒绝启动", func(t *testing.T) {
		c := base("llm-relay-dev-secret-change-me")
		// 默认 Secret 恰好是这个值：相同即拒绝
		if err := c.validate(); err == nil || !strings.Contains(err.Error(), "相同") {
			t.Fatalf("与主密钥相同应被拒绝，实际 %v", err)
		}
	})

	t.Run("合法密钥通过", func(t *testing.T) {
		c := base("0123456789abcdef-random-value")
		if err := c.validate(); err != nil {
			t.Fatalf("合法密钥不应报错：%v", err)
		}
	})

	t.Run("未配置密钥不报错（旧部署兼容）", func(t *testing.T) {
		if err := Default().validate(); err != nil {
			t.Fatalf("未配置 AdminKey 不应报错：%v", err)
		}
	})
}

func TestValidateSessionTTL(t *testing.T) {
	t.Run("非法时长拒绝", func(t *testing.T) {
		c := Default()
		c.Security.SessionTTL = -time.Hour
		if err := c.validate(); err == nil {
			t.Fatal("负的会话时长应被拒绝")
		}
	})
	t.Run("超过上限拒绝", func(t *testing.T) {
		c := Default()
		c.Security.SessionTTL = 721 * time.Hour
		if err := c.validate(); err == nil {
			t.Fatal("超过 30 天的会话时长应被拒绝")
		}
	})
	t.Run("默认值合法", func(t *testing.T) {
		if err := Default().validate(); err != nil {
			t.Fatalf("默认 168h 不应报错：%v", err)
		}
	})
}
