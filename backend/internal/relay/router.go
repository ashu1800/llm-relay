package relay

import (
	"context"
	"fmt"
	"log/slog"
	"math"
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
	// Saturated 表示该渠道的在途请求已达自身上限。它只是被排到后面，
	// 不出局——全部渠道都饱和时仍然要能发出去。
	Saturated bool
	// GroupRPM / GroupTPM 是所属分组的每分钟额度（0 = 不限制）。
	// 路由时一并取出来，省得为每个候选再查一次分组表。
	GroupRPM int
	GroupTPM int
}

// EgressProxyID 返回这次请求该走哪个代理：模型条目上的设置优先于渠道级，
// 都是 0 表示直连。
func (c Candidate) EgressProxyID() uint {
	if c.Binding.ProxyID != 0 {
		return c.Binding.ProxyID
	}
	return c.Channel.ProxyID
}

// Router 负责按分组与模型挑选渠道，并给出故障转移顺序。
type Router struct {
	db     *gorm.DB
	cipher *secure.Cipher
	// logger 用于记录「渠道密钥解不开被剔除」这类需要人看见的异常
	logger *slog.Logger

	mu       sync.Mutex
	rrCursor map[uint]int // 分组 ID -> 轮询游标

	// state 为运行期渠道状态，可为空（未接入时限流能力自动关闭）
	state *ChannelState
}

// SetChannelState 注入渠道运行期状态，用于冷却过滤与并发饱和判定。
func (r *Router) SetChannelState(st *ChannelState) { r.state = st }

// SetLogger 覆盖默认日志器，传 nil 不生效。
func (r *Router) SetLogger(l *slog.Logger) {
	if l != nil {
		r.logger = l
	}
}

// NewRouter 构造路由器。logger 默认 slog.Default()，main 里可换成全局那一份。
func NewRouter(db *gorm.DB, cipher *secure.Cipher) *Router {
	return &Router{db: db, cipher: cipher, logger: slog.Default(), rrCursor: make(map[uint]int)}
}

// CandidateQuery 是一次候选渠道查询的条件。
type CandidateQuery struct {
	PublicModel string
	// GroupID > 0 时只在该分组内选择，分组策略也取它的
	GroupID uint
	// AllowedGroups 非空时只在这些分组内选择（来自密钥白名单）
	AllowedGroups []uint
	// Exclude 里的渠道 ID 会被跳过，用于重试时避免再次命中同一渠道
	Exclude []uint
}

// Candidates 返回按策略排好序的候选渠道。
func (r *Router) Candidates(ctx context.Context, q CandidateQuery) ([]Candidate, error) {
	type row struct {
		model.Channel
		PublicName   string
		UpstreamName string
		BindingID    uint
		// BindingProxyID 是该模型条目自己的代理（0 = 跟随渠道）
		BindingProxyID uint
		GroupRPM       int
		GroupTPM       int
	}

	var rows []row
	// 候选 = 「渠道自己的模型白名单里写了这个对外名」的启用渠道。
	// 白名单直接存在渠道上，不再经过一张独立的模型目录表 ——
	// 那种两层结构会留下「模型建好了但没绑渠道」的死状态，请求只能得到
	// 「没有可用渠道」，而界面上两处看起来都是配好的。
	query := r.db.WithContext(ctx).
		Table("channels").
		Select("channels.*, channel_models.public_name AS public_name, channel_models.upstream_name AS upstream_name, channel_models.id AS binding_id, channel_models.proxy_id AS binding_proxy_id, channel_groups.rpm AS group_rpm, channel_groups.tpm AS group_tpm").
		Joins("JOIN channel_models ON channel_models.channel_id = channels.id AND channel_models.enabled = true").
		Joins("JOIN channel_groups ON channel_groups.id = channels.group_id").
		Where("channel_models.public_name = ?", q.PublicModel).
		Where("channels.enabled = true").
		// 停用的分组连同它的渠道一起退出候选，否则「停用分组」这个开关毫无作用
		Where("channel_groups.enabled = true")

	if q.GroupID > 0 {
		query = query.Where("channels.group_id = ?", q.GroupID)
	}
	// 密钥白名单：分组之外的渠道一律不参选。
	// 空切片与 nil 都表示不限制 —— 白名单没配就不该影响路由。
	if len(q.AllowedGroups) > 0 {
		query = query.Where("channels.group_id IN ?", q.AllowedGroups)
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询候选渠道失败: %w", err)
	}

	excluded := make(map[uint]bool, len(q.Exclude))
	for _, id := range q.Exclude {
		excluded[id] = true
	}

	// 本地时区：可用时段是用户按自己钟点填的，与定价时段规则同一口径；
	// 用 UTC 判断会让窗口整体偏移（Asia/Shanghai 下偏 8 小时）。
	now := time.Now()
	cands := make([]Candidate, 0, len(rows))
	for _, rw := range rows {
		if excluded[rw.ID] {
			continue
		}
		// 冷却中的渠道直接出局：上游明确要求退避，继续打只会让限流更久
		if _, cooling := r.state.InCooldown(rw.ID, now); cooling {
			continue
		}
		maxConc := ChannelMaxConcurrency(rw.ExtraConfig)
		c := Candidate{
			Channel:   rw.Channel,
			Binding:   model.ChannelModel{ID: rw.BindingID, ChannelID: rw.ID, PublicName: rw.PublicName, UpstreamName: rw.UpstreamName, Enabled: true, ProxyID: rw.BindingProxyID},
			Available: SlotAvailable(rw.Slots, now),
			Saturated: maxConc > 0 && r.state.Inflight(rw.ID) >= maxConc,
			GroupRPM:  rw.GroupRPM,
			GroupTPM:  rw.GroupTPM,
		}
		plain, err := r.cipher.Decrypt(rw.APIKeyEnc)
		if err != nil {
			// 密文解不开（换过主密钥、数据损坏）还放行的话，请求会带着空密钥
			// 发给上游、以 401 收场 —— 用户会去查上游，而问题其实在本地。
			// 剔出本轮候选并留下日志；APIKeyEnc 为空的免密渠道不受影响。
			r.logger.Warn("渠道密钥解密失败，已从候选中剔除",
				"channel_id", rw.ID, "channel", rw.Name, "err", err)
			continue
		}
		c.APIKeyPlain = plain
		cands = append(cands, c)
	}

	// 策略归属：显式分组优先；密钥只限定了一个分组时用那个分组的策略；
	// 否则回落到默认分组。不加这一段的话，被白名单限定到某个分组的请求
	// 仍然按默认分组的策略排序，分组上配的策略形同虚设。
	strategyGroup := q.GroupID
	if strategyGroup == 0 && len(q.AllowedGroups) == 1 {
		strategyGroup = q.AllowedGroups[0]
	}
	sortCandidates(cands, r.strategyFor(ctx, strategyGroup), r)
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
	// 可用时段内且未达并发上限的渠道优先
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Available != cands[j].Available {
			return cands[i].Available
		}
		return !cands[i].Saturated && cands[j].Saturated
	})

	switch strategy {
	case model.StrategyRoundRobin:
		rotate(cands)
	case model.StrategyLeastLatency:
		// 按最近成功响应的首包延迟（EWMA，见 ChannelState.RecordLatency）升序。
		// 没有观测值的渠道排最后但不出局 —— 否则新渠道永远得不到第一次机会。
		lat := func(id uint) float64 {
			if v := r.state.Latency(id); v > 0 {
				return v
			}
			return math.Inf(1)
		}
		sort.SliceStable(cands, func(i, j int) bool {
			return lat(cands[i].Channel.ID) < lat(cands[j].Channel.ID)
		})
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
// 时刻按服务器本地时区判断（与定价时段规则同一口径）。
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
