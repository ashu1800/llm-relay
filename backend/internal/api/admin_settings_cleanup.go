package api

import (
	"context"
	"time"
)

// cleanupCounts 是一次清理的结果，手动按钮与自动循环共用。
type cleanupCounts struct {
	DeletedLogs     int64
	DeletedPayloads int64
	DeletedOrphans  int64
}

// runCleanup 删除超出保留期的日志与报文，顺带清掉孤儿报文。
// 手动按钮与自动清理必须走同一份逻辑：两边口径不一致的话，
// 「按钮清掉的」和「半夜自动清掉的」对不上账。
func (s *Server) runCleanup(days int) (cleanupCounts, error) {
	cutoff := time.Now().AddDate(0, 0, -days)
	db := s.deps.Store.DB()
	var out cleanupCounts

	// 报文表按 log_id 关联，先删报文再删日志，避免留下孤儿行
	if res := db.Exec(`DELETE FROM request_payloads WHERE log_id IN
(SELECT id FROM request_logs WHERE created_at < ?)`, cutoff); res.Error != nil {
		return out, res.Error
	} else {
		out.DeletedPayloads = res.RowsAffected
	}
	// 历史遗留的孤儿报文（对应日志已被删）一并清掉
	db.Raw("SELECT COUNT(*) FROM request_payloads WHERE log_id NOT IN (SELECT id FROM request_logs)").Scan(&out.DeletedOrphans)
	if out.DeletedOrphans > 0 {
		db.Exec("DELETE FROM request_payloads WHERE log_id NOT IN (SELECT id FROM request_logs)")
	}

	res := db.Exec("DELETE FROM request_logs WHERE created_at < ?", cutoff)
	if res.Error != nil {
		return out, res.Error
	}
	out.DeletedLogs = res.RowsAffected
	return out, nil
}

// StartRetentionLoop 自动清理超出保留期的日志。
// 没有它，保留天数只对「手动点清理按钮」生效，数据库照样无限增长 ——
// 报文留存开 all 档时，一个月就能把几个 GB 吃掉。
// 启动一分钟后先跑一次（给迁移与连接池预热让路），之后每天一次；
// 未配置保留天数（<=0）视为用户明确不要自动清理，不启动。
func (s *Server) StartRetentionLoop(ctx context.Context) {
	days := s.deps.Config.Relay.LogRetentionDays
	if days <= 0 {
		return
	}
	go func() {
		first := time.NewTimer(time.Minute)
		defer first.Stop()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
			case <-ticker.C:
			}
			out, err := s.runCleanup(days)
			if err != nil {
				s.logger().Warn("自动清理日志失败", "err", err)
				continue
			}
			if out.DeletedLogs > 0 || out.DeletedPayloads > 0 || out.DeletedOrphans > 0 {
				s.logger().Info("已自动清理超出保留期的数据",
					"days", days, "logs", out.DeletedLogs,
					"payloads", out.DeletedPayloads, "orphans", out.DeletedOrphans)
			}
		}
	}()
}
