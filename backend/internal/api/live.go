package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/websocket"

	"llm-relay/internal/model"
)

// liveHub 管理实时推送的订阅者。
//
// 为什么用「轮询 + 广播」而不是在每个写入点埋钩子：
// 日志是异步批量落库的（relay.LogWriter 带 2048 缓冲），在写入口广播会漏掉
// 重试与批量刷盘的路径；而按 id 增量查询天然不会漏、也不会重，
// 断线重连后还能自动补上断线期间的数据。
//
// 两条循环的**耦合**（2026-09-18 站主反馈后加的）：
// 日志循环查到新日志时，会通过 statsKick 立刻叫醒统计循环算一次。
// 在此之前两者各按自己的定时器跑（1s / 2s），相位无关 —— 同一条请求造成的
// 「新行插进来」与「卡片数字变了」被拆成两拍，平均差约 1 秒、最坏接近 2 秒。
// 站主看到的正是这个：「新日志入场动画结束后统计卡片里的数值才会变动」。
// 详见 liveStatsLoop 上方的说明。
type liveHub struct {
	mu   sync.Mutex
	next int
	subs map[int]chan []byte

	// 每条连接各自的心跳由写循环负责；这里只记订阅者数量用于日志
	logger *slog.Logger
}

// NewLiveHub 构造实时推送中心。
func NewLiveHub(logger *slog.Logger) *liveHub {
	return &liveHub{subs: make(map[int]chan []byte), logger: logger}
}

// 订阅队列的长度。满了就丢最旧的推送，而不是阻塞广播方 ——
// 一个卡住的浏览器标签页不该拖慢整个服务。
const liveQueueSize = 64

func (h *liveHub) subscribe() (int, <-chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	ch := make(chan []byte, liveQueueSize)
	h.subs[id] = ch
	return id, ch
}

func (h *liveHub) unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[id]; ok {
		delete(h.subs, id)
		close(ch)
	}
}

func (h *liveHub) subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *liveHub) broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.subs {
		select {
		case ch <- payload:
		default:
			// 队列满：丢掉**最旧**的一帧，再把新的放进去。
			// 直接丢弃新帧（最省事的写法）会让这个订阅者的数字永久停在旧值：
			// 统计只在变化时推，被丢掉的那一帧不会再补发。
			// 卡住广播方同样不行 —— 一个慢标签页会拖慢所有连接。
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- payload:
			default:
			}
			if h.logger != nil {
				h.logger.Warn("实时推送队列已满，丢弃最旧一帧", "subscriber", id)
			}
		}
	}
	if h.logger != nil {
		// 排查「连上了却收不到推送」时，唯一能回答「服务端到底发了没有」
		// 的就是这行日志。用 Info 而不是 Debug：推送本身是低频的
		// （统计只在变化时推），这点日志量换来的是可诊断性
		h.logger.Info("实时推送", "subscribers", len(h.subs), "bytes", len(payload))
	}
}

// liveMessage 是推给前端的一帧。
type liveMessage struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// 推送节奏。统计每 2 秒算一次（聚合查询不便宜，而这只是**兜底**节拍 ——
// 有新日志时由日志循环叫醒它立刻算，见 liveStatsLoop）；
// 日志每 1 秒查一次增量（它是「实时」的主要观感）。
const liveStatsInterval = 2 * time.Second
const liveLogsInterval = time.Second

// 单帧写入的超时。正常帧只有几百字节，10 秒足够；
// 超时说明这个客户端已经不消费数据了，断开比无限等更合适。
const liveWriteTimeout = 10 * time.Second

// 单次查询的超时。聚合查询再慢也不该拖过推送间隔，
// 否则循环会一个接一个地堆积查询。
const liveQueryTimeout = 3 * time.Second

// StartLive 启动实时推送循环，直到 ctx 结束。
func (s *Server) StartLive(ctx context.Context) {
	if s.deps.Live == nil {
		return
	}
	// 统计循环负责算，日志循环负责在「有新日志」时叫它立刻算一次。
	// 通道容量 1 + 非阻塞发送：推动方是日志循环，它一秒才推一次新日志，
	// 而接收方算一次统计要几毫秒 —— 容量 1 已经足够，多余的信号合并成一个
	// （要的只是「有新数据，再算一遍」，攒着几个没有意义）。
	statsKick := make(chan struct{}, 1)
	go s.liveStatsLoop(ctx, statsKick)
	go s.liveLogsLoop(ctx, statsKick)
	// 预算检查独立成循环：它挂在 statsKick 上（花费只随新日志变化，
	// 天然不空转），但**不看订阅者数** —— 预算是记账面告警，
	// 不是看板附属品。没人开着看板时超支提醒迟到可以接受
	//（下次连上看板就补喊），但整体静默不行。
	go s.budgetLoop(ctx, statsKick)
}

// liveStatsLoop 每 2 秒算一次今日汇总，变了才推。
//
// kick 让「有新日志入库」能立刻触发一次计算，而不是干等下一个 2 秒节拍。
//
// 为什么非要它（站主 2026-09-18 反馈「新日志入场动画结束后统计卡片里的数值
// 才会变动，正常应该是一起变动的」）：日志循环 1s 一拍、统计循环 2s 一拍，
// 两个 time.Ticker 的相位互不相干，同一条请求造成的两件事因此被拆成两拍 ——
// 实测（.shots/verify-live-sync.mjs，线上产物 8888）新行 487ms 就插进来了，
// 卡片数字到 1551ms 才动，中间空着 1064ms；两拍的相位差本身在 0..2s 之间漂移，
// 所以用户看到的是「有时候一起动、有时候差一大截」，最坏接近整整两秒。
//
// 为什么不是把统计也改成 1 秒一拍：那只是把窗口从 2s 缩到 1s，两拍仍然是两拍，
// 平均还差半秒；而且空转的聚合查询翻倍 —— 而它们绝大多数时候算出来一模一样
// （没变化就不推，白算）。用 kick 之后，闲时一次都不多算，
// 有流量时两件事落在同一个节拍里（先后差一次查询的时间，约几毫秒）。
//
// kick 只表示「有新日志」，不替代定时器：定时器负责兜住 kick 之外的变化
// （清理历史数据、手工改库、以及 kick 被合并掉的那些时刻）。
func (s *Server) liveStatsLoop(ctx context.Context, kick <-chan struct{}) {
	statsTicker := time.NewTicker(liveStatsInterval)
	defer statsTicker.Stop()
	var lastSent string
	// health 帧的上次内容：日志队列水位与丢弃计数，变了才推（与 stats 同一原则）
	var lastHealth string
	for {
		select {
		case <-ctx.Done():
			return
		case <-statsTicker.C:
		case <-kick:
		}
		// 没人订阅就不查：聚合查询要扫今天的所有日志
		if s.deps.Live.subscribers() == 0 {
			continue
		}
		// ---- 日志队列水位（health 帧）----
		//
		// 挂在统计循环的同一拍而不是另起循环：醒来时机已经够密（有新日志就
		// 会被 kick），丢弃从发生到看板喊出来最多隔一个节拍，够快；
		// 独立循环只会多一份 goroutine 与节拍器。
		// Logs 为 nil 的防御与 Live 相同：依赖缺省时这一项整体跳过，
		// 不该让 stats 也跟着不推。
		if s.deps.Logs != nil {
			st := s.deps.Logs.QueueStats()
			if b, err := json.Marshal(st); err == nil && string(b) != lastHealth {
				lastHealth = string(b)
				s.deps.Live.broadcast(mustJSON(liveMessage{Type: "health", Data: st}))
			}
		}
		start, end, _ := resolveRange("today")
		qctx, cancel := context.WithTimeout(ctx, liveQueryTimeout)
		// 零值筛选 = 全站：推送的视角固定是「今天 + 未筛选」，
		// 前端只在同样视角下才合并它（见 DashboardView 的 onLive）
		data, err := s.summarySnapshotCtx(qctx, start, end, statsFilter{})
		cancel()
		if err != nil {
			continue
		}
		// 只在真的变了才推：没变也推的话，前端每 2 秒就要跑一次数字动画，
		// 屏幕上会一直有东西在动，反而看不清哪个数字变了
		encoded, _ := json.Marshal(data)
		if string(encoded) == lastSent {
			continue
		}
		lastSent = string(encoded)
		s.deps.Live.broadcast(mustJSON(liveMessage{Type: "stats", Data: data}))
	}
}

// budgetLoop 独立跑预算检查：被 statsKick 叫醒（有新日志，花费才可能变），
// 外加 60 秒兜底节拍（兜住跨天清零与 kick 被合并掉的时刻）。
//
// 与 liveStatsLoop 的关键区别：**不看订阅者数**。统计推送没人看就是纯浪费，
// 可以跳过；但预算提醒「当天没人开看板就整体静默」是功能失效 ——
// 提醒本身经 WebSocket 广播，没人连时它自然无处可去，可去重状态在内存里，
// 有人连上看板后 liveSocket 的首帧补发机制会让他看到当前花费。
//
// 注意 statsKick 有**两个**消费者（liveStatsLoop 与本循环各在 select 里等
// 同一个通道）：一个 kick 信号只会唤醒其中之一 —— 统计与预算各自最多隔
// 一个节拍才轮到，这正是 60 秒兜底 ticker 存在的理由之一（另一个是
// 跨天清零这类不随新日志发生的变化）。
func (s *Server) budgetLoop(ctx context.Context, kick <-chan struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-kick:
		}
		start, end, _ := resolveRange("today")
		s.checkBudgets(ctx, start, end)
	}
}

// liveLogsLoop 每秒查一次增量日志并推给前端，有新行时顺带叫醒统计循环。
func (s *Server) liveLogsLoop(ctx context.Context, kick chan<- struct{}) {
	ticker := time.NewTicker(liveLogsInterval)
	defer ticker.Stop()
	// 绑定 ctx：关停时正在跑的查询会被取消，而不是让进程干等它查完
	db := s.deps.Store.DB().WithContext(ctx)

	// 起点是「当前最大 id」：不这么做的话，第一次连上来就会把历史日志
	// 当成新日志灌给前端
	var maxID uint
	if err := db.Model(&model.RequestLog{}).Select("COALESCE(MAX(id),0)").Scan(&maxID).Error; err != nil {
		s.logger().Warn("读取日志最大 id 失败，实时推送从 0 开始", "err", err)
	}
	last := maxID

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if s.deps.Live.subscribers() == 0 {
			// 没人看的时候把游标推到最新，避免重新有人连上时
			// 一次性补推这段时间积压的全部日志
			_ = db.Model(&model.RequestLog{}).Select("COALESCE(MAX(id),0)").Scan(&last).Error
			continue
		}
		var rows []model.RequestLog
		if err := db.Where("id > ?", last).Order("id").Limit(50).Find(&rows).Error; err != nil {
			continue
		}
		if len(rows) == 0 {
			continue
		}
		last = rows[len(rows)-1].ID
		s.deps.Live.broadcast(mustJSON(liveMessage{Type: "logs", Data: rows}))
		// 日志先推、再叫醒统计循环：两帧落到同一条 WebSocket 上，
		// 顺序保持「行先到、数字随后」，间隔只有一次聚合查询的时间（毫秒级），
		// 前端的入场动画与数字滚动因此是同时开始的（见 liveStatsLoop 的说明）。
		select {
		case kick <- struct{}{}:
		default:
			// 已经有一个待处理的信号：那一次计算自然会看到这几行，
			// 不必再排一个（这正是通道容量 1 想要的合并效果）
		}
	}
}

func (s *Server) logger() *slog.Logger {
	if s.deps.Logger != nil {
		return s.deps.Logger
	}
	return slog.Default()
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"type":"error","data":"序列化失败"}`)
	}
	return b
}

// liveSocket 是实时推送的 WebSocket 端点。
//
// 只推不接：这个通道上没有需要客户端上报的东西，
// 收到的消息一律忽略（留着是为了让心跳/关闭帧能被正常处理）。
func (s *Server) liveSocket(c *gin.Context) {
	if s.deps.Live == nil {
		writeUpstreamError(c, 501, "实时推送未启用", "not_implemented")
		return
	}
	websocket.Handler(func(ws *websocket.Conn) {
		id, ch := s.deps.Live.subscribe()
		defer s.deps.Live.unsubscribe(id)

		// 必须有人读：x/net/websocket 只在 Read/Receive 路径里处理
		// 控制帧（Ping → Pong、Close），只写不读的话客户端的关闭帧
		// 永远不被处理，订阅会一直留着 —— 而广播是「有变化才推」，
		// 空闲时可能几小时不推一次，靠 Send 失败来发现断线是不可靠的。
		//
		// 这个通道上没有需要客户端上报的东西，读到的内容一律丢弃。
		closed := make(chan struct{})
		go func() {
			defer close(closed)
			var discard string
			for {
				if err := websocket.Message.Receive(ws, &discard); err != nil {
					return
				}
			}
		}()

		// 连上先补一份当前快照：不然要等到下一次变化才有东西显示，
		// 而「打开页面后数字是空的」看起来就像坏了。
		// 与 liveStatsLoop 同一口径：今天 + 全站（零值筛选）
		start, end, _ := resolveRange("today")
		if data, err := s.summarySnapshot(start, end, statsFilter{}); err == nil {
			_ = websocket.Message.Send(ws, string(mustJSON(liveMessage{Type: "stats", Data: data})))
		}
		// 队列水位同样补一份：否则要等到它变化才显示（大多数时候队列是
		// 平稳的，那一等可能就是永远）
		if s.deps.Logs != nil {
			_ = websocket.Message.Send(ws, string(mustJSON(liveMessage{Type: "health", Data: s.deps.Logs.QueueStats()})))
		}

		// WebSocket 连接不允许并发写，所以写只在这个 goroutine 里做
		for {
			select {
			case <-closed:
				return
			case payload, ok := <-ch:
				if !ok {
					return
				}
				// 写超时：客户端 TCP 缓冲区满时（比如标签页被挂起）
				// 没有 deadline 的 Send 会把这条 goroutine 永久堵住
				_ = ws.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
				if err := websocket.Message.Send(ws, string(payload)); err != nil {
					s.logger().Warn("实时推送发送失败，断开该订阅", "err", err)
					return
				}
			}
		}
	}).ServeHTTP(c.Writer, c.Request)
}
