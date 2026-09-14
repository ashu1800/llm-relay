package api

import (
	"fmt"
	"sort"

	"gorm.io/gorm"

	"llm-relay/internal/model"
)

// 本文件集中处理「渠道在分组内的优先级序号」。
//
// 背景：weight 不再是加权随机的抽签份额，而是组内优先级序号（1..N 连续唯一，
// 越小越优先，见 model.Channel.Weight）。转发时每轮取候选链首个，候选按 weight
// 升序，于是序号就是故障转移顺序。
//
// 一致性靠三件事共同保证：
//  1. 数据库唯一索引 idx_group_weight（DDL 由 AutoMigrate 创建）
//  2. 所有会改变「组内成员或成员先后」的写路径都调用下面的函数重排
//  3. 启动时 store.normalizeChannelWeights 兜底清理历史数据
//
// 所以：**任何直接写 channels.weight 的新代码都是 bug**，请走这里。

// shiftOffset 是重排时用的临时错位量。
//
// 重排不能直接逐行赋新值：把某行从 1 改成 2 时，那个 2 往往还被别人占着，
// 唯一索引会当场拒绝。先把整组挪到一个不可能冲突的区间，再赋 1..N。
// 取一个够大的常数即可 —— 序号是人工拖出来的，不可能接近这个量级。
const shiftOffset = 1_000_000

// renumberGroupChannels 按现有 (weight, id) 顺序把某个分组的渠道重排成 1..N。
// 用于「成员减少或移出后补位」，调用方需自己保证处在事务里。
func renumberGroupChannels(tx *gorm.DB, groupID uint) error {
	var rows []model.Channel
	if err := tx.Select("id").Where("group_id = ?", groupID).
		Order("weight, id").Find(&rows).Error; err != nil {
		return fmt.Errorf("读取分组内渠道失败: %w", err)
	}
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return applyChannelOrder(tx, groupID, ids)
}

// applyChannelOrder 把某个分组的渠道**按 ids 给出的顺序**重排成 1..N。
//
// ids 必须是该分组当前全部渠道（顺序即目标优先级）；调用方需先校验，
// 这里只做写入。校验与写入分开是为了让「顺序计算」保持成可单测的纯逻辑。
func applyChannelOrder(tx *gorm.DB, groupID uint, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	// 先整体让开，避免与将要写入的序号撞唯一索引
	if err := tx.Model(&model.Channel{}).Where("group_id = ?", groupID).
		Update("weight", gorm.Expr("weight + ?", shiftOffset)).Error; err != nil {
		return fmt.Errorf("重排渠道优先级失败（错位阶段）: %w", err)
	}
	for i, id := range ids {
		// 带上 group_id 条件：传进来的 id 若不属于这个分组，宁可一行都不改
		// （漏改会让它停在错位后的巨大值上，比报错更容易被发现）
		res := tx.Model(&model.Channel{}).
			Where("id = ? AND group_id = ?", id, groupID).
			Update("weight", i+1)
		if res.Error != nil {
			return fmt.Errorf("重排渠道优先级失败（id=%d）: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("渠道 %d 不属于分组 %d", id, groupID)
		}
	}
	return nil
}

// appendChannelToGroup 把一条新渠道排到分组的末尾，返回它应得的序号。
//
// 新建渠道、以及把渠道改到另一个分组时都用它：新来的排在最后，
// 不会插队改变既有的故障转移顺序。
func appendChannelToGroup(tx *gorm.DB, groupID uint) (int, error) {
	// COALESCE 而不是让 MAX 返回 NULL：空分组下 MAX 是 NULL，
	// 扫进 int 会变成 0，看起来「最大序号是 0」，但那个 0 的含义
	// 与「真的有一条序号为 0 的渠道」区分不开
	var maxWeight int
	if err := tx.Model(&model.Channel{}).Where("group_id = ?", groupID).
		Select("COALESCE(MAX(weight), 0)").Scan(&maxWeight).Error; err != nil {
		return 0, fmt.Errorf("读取分组内最大优先级失败: %w", err)
	}
	// 历史数据理论上不会出现负序号（重排都会翻正），真出现也不该把新渠道
	// 排到它前面去，所以下界兜到 1
	if maxWeight < 1 {
		return 1, nil
	}
	return maxWeight + 1, nil
}

// normalizeStrategy 把客户端传来的策略收敛到当前支持的那几个。
//
// 后端此前不做任何校验，任意字符串都能落库 —— 一个拼错的值会静默走到
// 路由的默认分支，而界面上看不出异常。现在只认三个：
// 空值取 failover（分组默认策略），已下线的 weighted 也映射到 failover
// （老脚本、老备份里还在传它），其余一律当 failover 处理。
func normalizeStrategy(v string) string {
	switch v {
	case model.StrategyRoundRobin, model.StrategyLeastLatency:
		return v
	default:
		return model.StrategyFailover
	}
}

// orderInvalidError 标记「提交的 ids 与库里成员对不上」这类校验错误。
//
// 用类型区分而不是靠字符串匹配：处理器要据此回 400（用户输入问题，刷新列表即可）
// 还是 500（真的写失败了）。混在一起的话，界面过期会被报成服务端故障，
// 排查方向直接跑偏。
type orderInvalidError struct{ msg string }

func (e orderInvalidError) Error() string { return e.msg }

// orderValidationError 描述 order 请求与库中实际成员对不上的地方。
//
// 这种错误必须点名差在哪，否则前端只能看到一句「400」：全量提交的接口一旦
// 对不上，多半是界面上的列表过期了（别处刚加过或删过渠道），
// 而用户需要知道的是「刷新一下再来」还是「我传错了分组」。
func orderValidationError(groupChannels []model.Channel, ids []uint) error {
	inGroup := make(map[uint]bool, len(groupChannels))
	for _, ch := range groupChannels {
		inGroup[ch.ID] = true
	}
	seen := make(map[uint]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("渠道 %d 在 ids 里出现了多次", id)
		}
		seen[id] = true
		if !inGroup[id] {
			return fmt.Errorf("渠道 %d 不属于该分组，不能在这里排序", id)
		}
	}
	missing := make([]uint, 0, len(groupChannels))
	for _, ch := range groupChannels {
		if !seen[ch.ID] {
			missing = append(missing, ch.ID)
		}
	}
	if len(missing) > 0 {
		sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
		return fmt.Errorf("ids 必须是该分组的全部渠道，缺少 %v（列表可能已过期，刷新后重试）", missing)
	}
	return nil
}
