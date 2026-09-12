package relay

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/secure"
)

// Candidate 是一个可用于本次请求的渠道。
type Candidate struct {
	Channel     model.Channel
	Binding     model.ChannelModel
	APIKeyPlain string
	// Available 表示当前时刻是否在渠道的可用时段内。
	Available bool
}

// Router 负责按分组与模型挑选渠道，并给出故障转移顺序。
type Router struct {
	db     *gorm.DB
	cipher *secure.Cipher

	mu       sync.Mutex
	rrCursor map[uint]int // 分组 ID -> 轮询游标
}

// NewRouter 构造路由器。
func NewRouter(db *gorm.DB, cipher *secure.Cipher) *Router {
	return &Router{db: db, cipher: cipher, rrCursor: make(map[uint]int)}
}

// Candidates 返回按策略排好序的候选渠道。
// exclude 中的渠道 ID 会被跳过，用于重试时避免再次命中同一渠道。
func (r *Router) Candidates(ctx context.Context, groupID uint, publicModel string, exclude []uint) ([]Candidate, error) {
	type row struct {
		model.Channel
		UpstreamName string
		BindingID    uint
	}

	var rows []row
	q := r.db.WithContext(ctx).
		Table("channels").
		Select("channels.*, channel_models.upstream_name AS upstream_name, channel_models.id AS binding_id").
		Joins("JOIN channel_models ON channel_models.channel_id = channels.id AND channel_models.enabled = true").
		Joins("JOIN models ON models.id = channel_models.model_id AND models.enabled = true").
		Where("models.public_name = ?", publicModel).
		Where("channels.enabled = true")

	if groupID > 0 {
		q = q.Where("channels.group_id = ?", groupID)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询候选渠道失败: %w", err)
	}

	excluded := make(map[uint]bool, len(exclude))
	for _, id := range exclude {
		excluded[id] = true
	}

	now := time.Now().UTC()
	cands := make([]Candidate, 0, len(rows))
	for _, rw := range rows {
		if excluded[rw.ID] {
			continue
		}
		c := Candidate{
			Channel:   rw.Channel,
			Binding:   model.ChannelModel{ID: rw.BindingID, ChannelID: rw.ID, UpstreamName: rw.UpstreamName, Enabled: true},
			Available: SlotAvailable(rw.Slots, now),
		}
		if plain, err := r.cipher.Decrypt(rw.APIKeyEnc); err == nil {
			c.APIKeyPlain = plain
		}
		cands = append(cands, c)
	}

	sortCandidates(cands, r.strategyFor(ctx, groupID), r)
	return cands, nil
}

// strategyFor 取分组的路由策略，取不到则回退到加权。
func (r *Router) strategyFor(ctx context.Context, groupID uint) string {
	if groupID == 0 {
		var g model.ChannelGroup
		if err := r.db.WithContext(ctx).Where("is_default = true").First(&g).Error; err == nil {
			return g.Strategy
		}
		return model.StrategyWeighted
	}
	var g model.ChannelGroup
	if err := r.db.WithContext(ctx).First(&g, groupID).Error; err != nil {
		return model.StrategyWeighted
	}
	return g.Strategy
}

// sortCandidates 依据策略重排候选，第一个即首选渠道。
func sortCandidates(cands []Candidate, strategy string, r *Router) {
	// 可用时段内的渠道永远优先
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Available != cands[j].Available {
			return cands[i].Available
		}
		return false
	})

	switch strategy {
	case model.StrategyRoundRobin:
		rotate(cands)
	case model.StrategyLeastLatency:
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].Channel.ID < cands[j].Channel.ID })
	case model.StrategyFailover:
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].Channel.ID < cands[j].Channel.ID })
	default: // weighted
		weightedShuffle(cands)
	}
}

// weightedShuffle 按权重把首选渠道抽到最前，其余保持稳定顺序作为故障转移链。
func weightedShuffle(cands []Candidate) {
	if len(cands) <= 1 {
		return
	}
	total := 0
	for _, c := range cands {
		w := c.Channel.Weight
		if w <= 0 {
			w = 1
		}
		total += w
	}
	if total <= 0 {
		return
	}
	pick := rand.Intn(total)
	acc := 0
	idx := 0
	for i, c := range cands {
		w := c.Channel.Weight
		if w <= 0 {
			w = 1
		}
		acc += w
		if pick < acc {
			idx = i
			break
		}
	}
	if idx > 0 {
		chosen := cands[idx]
		copy(cands[1:idx+1], cands[0:idx])
		cands[0] = chosen
	}
}

// rotate 轮询：把首个渠道挪到末尾，实现公平轮转。
func rotate(cands []Candidate) {
	if len(cands) <= 1 {
		return
	}
	first := cands[0]
	copy(cands, cands[1:])
	cands[len(cands)-1] = first
}

// SlotAvailable 判断当前时刻是否落在渠道声明的可用时段内。
// slots 为空表示全天可用。时段支持跨午夜（结束时间早于开始时间）。
func SlotAvailable(slots model.JSONList, now time.Time) bool {
	if len(slots) == 0 {
		return true
	}
	weekday := int(now.Weekday()) // 0=周日
	if weekday == 0 {
		weekday = 7 // 归一化成 1..7，与 ISO 一致
	}
	cur := now.Format("15:04")

	for _, s := range slots {
		days := toIntSlice(s["days"])
		if len(days) > 0 && !containsInt(days, weekday) {
			continue
		}
		start, _ := s["start"].(string)
		end, _ := s["end"].(string)
		start = strings.TrimSpace(start)
		end = strings.TrimSpace(end)
		if start == "" || end == "" {
			continue
		}
		if start <= end {
			if cur >= start && cur <= end {
				return true
			}
		} else { // 跨午夜
			if cur >= start || cur <= end {
				return true
			}
		}
	}
	return false
}

func toIntSlice(v any) []int {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]int, 0, len(list))
	for _, item := range list {
		if f, ok := item.(float64); ok {
			result = append(result, int(f))
		}
	}
	return result
}

func containsInt(list []int, target int) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}
