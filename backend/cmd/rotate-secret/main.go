// rotate-secret 把渠道密钥从一个加密主密钥迁移到另一个。
//
// 为什么需要它：渠道里的上游密钥用 RELAY_SECRET 派生出的 AES-GCM 密钥加密存储。
// 直接换掉 RELAY_SECRET 会让所有已存库的密钥解不开，表现为渠道全部认证失败——
// 而密钥原文只在上游方手里，用户未必还留着，等于把已有配置弄丢。
//
// 用法：
//
//	RELAY_SECRET=<新密钥> OLD_SECRET=<旧密钥> DB_... go run ./cmd/rotate-secret
//	# 加 -dry-run 只检查不改动
//
// 更推荐把两个密钥都从标准输入喂进来（见 -stdin）：环境变量虽然不出现在
// 进程列表里，但 docker exec -e 这种调用方式仍会把值写进 docker 客户端的 argv。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"llm-relay/internal/config"
	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

func main() {
	oldSecretFlag := flag.String("old-secret", "", "旧的主密钥。建议改用 OLD_SECRET 环境变量或 -stdin，见下")
	dryRun := flag.Bool("dry-run", false, "只检查能否解密，不写回数据库")
	readStdin := flag.Bool("stdin", false, "从标准输入按行读取新旧主密钥（第一行旧、第二行新），最安全的方式")
	flag.Parse()

	// 旧主密钥的取值优先级：标准输入 > 环境变量 > 命令行参数。
	//
	// 命令行参数的可见性比多数人以为的差得多：同一台机器上的任何用户
	// 都能通过 /proc/<pid>/cmdline 或 ps aux 读到别人进程的完整 argv，
	// 而且 shell 历史、systemd journal、CI 日志里也会留一份。
	// 对一个「用来轮换主密钥」的工具来说，把旧密钥暴露在 argv 里
	// 等于让这次轮换白做 —— 旧密钥本来就是因为怀疑泄漏才要换掉。
	//
	// -old-secret 仍然保留：手工排障时最方便，且老文档与脚本还在用。
	var oldSecret, stdinNewSecret string
	if *readStdin {
		// 第一行旧密钥、第二行新密钥。用 ReadString 而不是 Scanner：
		// Scanner 的默认缓冲上限是 64KB，对密钥来说远远够用，
		// 但它会静默截断超长行 —— 宁可在这里显式处理换行。
		r := bufio.NewReader(os.Stdin)
		first, err := r.ReadString('\n')
		if err != nil && first == "" {
			fatal("从标准输入读取旧主密钥失败: %v", err)
		}
		second, err := r.ReadString('\n')
		if err != nil && second == "" {
			fatal("从标准输入读取新主密钥失败: %v", err)
		}
		oldSecret = strings.TrimRight(first, "\r\n")
		stdinNewSecret = strings.TrimRight(second, "\r\n")
	}
	if oldSecret == "" {
		oldSecret = os.Getenv("OLD_SECRET")
	}
	if oldSecret == "" {
		oldSecret = *oldSecretFlag
	}
	if oldSecret == "" {
		fatal("必须指定旧主密钥：推荐管道输入（-stdin），也支持 OLD_SECRET 环境变量或 -old-secret")
	}

	cfg, err := config.Load(os.Getenv("CONFIG_PATH"))
	if err != nil {
		fatal("加载配置失败: %v", err)
	}
	newSecret := cfg.Security.Secret
	if *readStdin && stdinNewSecret != "" {
		// 命令行给的新密钥覆盖配置文件里的：-stdin 模式下配置文件里那个
		// 通常是旧的（.env 还没改），以管道传入的为准
		newSecret = stdinNewSecret
	}
	if newSecret == "" {
		fatal("新主密钥为空，请通过 RELAY_SECRET 环境变量或 -stdin 的第二行指定")
	}
	if newSecret == oldSecret {
		fatal("新旧主密钥相同，无需轮换")
	}

	oldCipher, err := secure.NewCipher(oldSecret)
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
