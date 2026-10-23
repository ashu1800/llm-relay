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

func TestValidatePublicDeployment(t *testing.T) {
	base := Default()
	base.Server.Host = "0.0.0.0"
	if err := base.validate(); err == nil || !strings.Contains(err.Error(), "admin_key") {
		t.Fatalf("公网监听缺少管理密钥应拒绝，实际 %v", err)
	}
	base.Security.AdminKey = "0123456789abcdef-random-value"
	// public_origin 现在可以留空：内网穿透下外网地址由隧道平台分配，
	// 安装时写不死。留空时 api.sameOriginOnly 回落到「Host 必须与请求一致」，
	// 跨站防护仍在（见 api/sameorigin_test.go）。
	if err := base.validate(); err != nil {
		t.Fatalf("公网监听不设固定来源也应通过（穿透部署），实际 %v", err)
	}
	base.Server.PublicOrigin = "https://relay.example.com"
	if err := base.validate(); err != nil {
		t.Fatalf("完整公网配置应通过：%v", err)
	}
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

// 客户端映射缓存的两项配置（2026-10-22 事故修复新增）：
//
//	· 上限是「防随机 UA 撑爆」的安全边界，0/负数会让它失效；
//	· 负 TTL 会让条目一写入就过期（每次请求都落一次 DB），同样是错的配置。
//
// 默认值必须与 relay 包的 DefaultClientMapLimit / DefaultClientMapTTL 一致 ——
// config 不 import relay，两处靠注释互指，这条用例把漂移变成会红的断言。
func TestValidateClientMap(t *testing.T) {
	t.Run("默认值可用且与 relay 包一致", func(t *testing.T) {
		c := Default()
		if c.Relay.ClientMapLimit != 2048 {
			t.Errorf("默认上限 = %d，期望 2048（与 relay.DefaultClientMapLimit 一致）", c.Relay.ClientMapLimit)
		}
		if c.Relay.ClientMapTTL != 7*24*time.Hour {
			t.Errorf("默认 TTL = %s，期望 168h（与 relay.DefaultClientMapTTL 一致）", c.Relay.ClientMapTTL)
		}
		if err := c.validate(); err != nil {
			t.Fatalf("默认配置不该报错：%v", err)
		}
	})

	t.Run("非正上限拒绝启动", func(t *testing.T) {
		for _, n := range []int{0, -1} {
			c := Default()
			c.Relay.ClientMapLimit = n
			if err := c.validate(); err == nil || !strings.Contains(err.Error(), "client_map_limit") {
				t.Fatalf("上限 %d 应被拒绝，实际 %v", n, err)
			}
		}
	})

	t.Run("负 TTL 拒绝启动，0 表示不过期", func(t *testing.T) {
		c := Default()
		c.Relay.ClientMapTTL = -time.Second
		if err := c.validate(); err == nil || !strings.Contains(err.Error(), "client_map_ttl") {
			t.Fatalf("负 TTL 应被拒绝，实际 %v", err)
		}
		c = Default()
		c.Relay.ClientMapTTL = 0
		if err := c.validate(); err != nil {
			t.Fatalf("TTL=0（不过期）应被接受：%v", err)
		}
	})
}

// 环境变量入口必须真的接上：CLAUDE.md 硬约定 13 —— 新增配置项要在
// applyEnv 与 envKeys 两处都补，否则部署侧想改也改不了、且不会有提示。
func TestClientMapEnv(t *testing.T) {
	t.Setenv("RELAY_CLIENT_MAP_LIMIT", "512")
	t.Setenv("RELAY_CLIENT_MAP_TTL", "48h")
	c := Default()
	applyEnv(c)
	if c.Relay.ClientMapLimit != 512 {
		t.Errorf("上限 = %d，期望 512", c.Relay.ClientMapLimit)
	}
	if c.Relay.ClientMapTTL != 48*time.Hour {
		t.Errorf("TTL = %s，期望 48h", c.Relay.ClientMapTTL)
	}
	keys := envKeys()
	for _, field := range []string{"client_map_limit", "client_map_ttl_hours"} {
		if keys[field] == "" {
			t.Errorf("envKeys 缺少 %s —— 部署侧无法从界面看到可用的环境变量名", field)
		}
	}
}
