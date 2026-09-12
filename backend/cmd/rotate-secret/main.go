// rotate-secret 把渠道密钥从一个加密主密钥迁移到另一个。
//
// 为什么需要它：渠道里的上游密钥用 RELAY_SECRET 派生出的 AES-GCM 密钥加密存储。
// 直接换掉 RELAY_SECRET 会让所有已存库的密钥解不开，表现为渠道全部认证失败——
// 而密钥原文只在上游方手里，用户未必还留着，等于把已有配置弄丢。
//
// 用法：
//
//	RELAY_SECRET=<新密钥> DB_... go run ./cmd/rotate-secret -old-secret <旧密钥>
//	# 加 -dry-run 只检查不改动
package main

import (
	"flag"
	"fmt"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"llm-relay/internal/config"
	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

func main() {
	oldSecret := flag.String("old-secret", "", "旧的主密钥（当前加密时用的）")
	dryRun := flag.Bool("dry-run", false, "只检查能否解密，不写回数据库")
	flag.Parse()

	if *oldSecret == "" {
		fatal("必须通过 -old-secret 指定旧主密钥")
	}

	cfg, err := config.Load(os.Getenv("CONFIG_PATH"))
	if err != nil {
		fatal("加载配置失败: %v", err)
	}
	newSecret := cfg.Security.Secret
	if newSecret == "" {
		fatal("新主密钥为空，请通过 RELAY_SECRET 环境变量指定")
	}
	if newSecret == *oldSecret {
		fatal("新旧主密钥相同，无需轮换")
	}

	oldCipher, err := secure.NewCipher(*oldSecret)
	if err != nil {
		fatal("旧主密钥不可用: %v", err)
	}
	newCipher, err := secure.NewCipher(newSecret)
	if err != nil {
		fatal("新主密钥不可用: %v", err)
	}

	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Database.Host, cfg.Database.Port, cfg.Database.User,
		cfg.Database.Password, cfg.Database.DBName, cfg.Database.SSLMode)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		fatal("连接数据库失败: %v", err)
	}

	var channels []model.Channel
	if err := db.Order("id").Find(&channels).Error; err != nil {
		fatal("读取渠道失败: %v", err)
	}
	if len(channels) == 0 {
		fmt.Println("没有渠道需要处理")
		return
	}

	var okCount, emptyCount, failCount int
	for _, ch := range channels {
		if ch.APIKeyEnc == "" {
			fmt.Printf("  [跳过] %-20s 未设置密钥\n", ch.Name)
			emptyCount++
			continue
		}
		plain, err := oldCipher.Decrypt(ch.APIKeyEnc)
		if err != nil {
			fmt.Printf("  [失败] %-20s 旧主密钥解不开: %v\n", ch.Name, err)
			failCount++
			continue
		}
		// 用新密钥重新加密前先验证能解回来，避免写进坏数据
		reEnc, err := newCipher.Encrypt(plain)
		if err != nil {
			fmt.Printf("  [失败] %-20s 重新加密失败: %v\n", ch.Name, err)
			failCount++
			continue
		}
		if back, err := newCipher.Decrypt(reEnc); err != nil || back != plain {
			fmt.Printf("  [失败] %-20s 回读校验不一致，已跳过\n", ch.Name)
			failCount++
			continue
		}
		if *dryRun {
			fmt.Printf("  [可迁移] %-20s 密文 %d -> %d 字节\n", ch.Name, len(ch.APIKeyEnc), len(reEnc))
			okCount++
			continue
		}
		if err := db.Model(&model.Channel{}).Where("id = ?", ch.ID).
			Update("api_key_enc", reEnc).Error; err != nil {
			fmt.Printf("  [失败] %-20s 写库失败: %v\n", ch.Name, err)
			failCount++
			continue
		}
		fmt.Printf("  [已迁移] %-20s\n", ch.Name)
		okCount++
	}

	fmt.Printf("\n合计：成功 %d，未设置 %d，失败 %d\n", okCount, emptyCount, failCount)
	if *dryRun {
		fmt.Println("（dry-run，未写入任何改动）")
	}
	if failCount > 0 {
		os.Exit(1)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[错误] "+format+"\n", args...)
	os.Exit(1)
}
