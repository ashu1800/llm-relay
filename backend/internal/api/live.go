package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"llm-relay/internal/model"
	"llm-relay/internal/netstat"
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

	// bwMu 保护下面两段带宽状态。它们与订阅表是两件不相干的事，
	// 所以用独立的锁而不是复用 mu：播报带宽时不该挡住订阅/退订。
	bwMu  sync.Mutex
	bwBuf []netstat.Stats

	// 每条连接各自的心跳由写循环负责；这里只记订阅者数量用于日志
	logger *slog.Logger
}

// NewLiveHub 构造实时推送中心。
func NewLiveHub(logger *slog.Logger) *liveHub {
	return &liveHub{subs: make(map[int]chan []byte), logger: logger}
}

// bandwidthHistory 是新连上的订阅者能立刻拿到的一段历史读数。
//
// 60 拍 × 1 秒 = 过去一分钟。为什么不存更长：曲线在界面上只有几十像素宽，
// 一分钟（60 个点）已经能把「刚才有没有一波流量」显示清楚；
// 存十分钟是 600 个点，每 GB 流量对应的数据量无所谓，但下发与重绘都变贵，
// 而这段历史存在的唯一目的就是「打开页面时曲线上不是空的」。
const bandwidthHistory = 60

// recordBandwidth 记一拍带宽读数，并把它推给所有订阅者。
//
// 与另外几条推送不同，带宽**不等变化**：曲线需要的是连续的采样点，
// 「和上一拍一样」本身就是有信息量的（说明这段时间没有流量）。
//
// 没人订阅时**只记不推**：采样必须继续（速率是差分出来的，见
// liveBandwidthLoop），但推送路径的代价并不只是几十字节 ——
// broadcast 每次都会写一行 Info 日志，1 Hz × 7×24 是每天 8 万多行、
// 约 8 MB（journald 条目或轮转文件）。那行日志的理由是
// 「排查『连上了却收不到推送』」，而零订阅者时它证明不了任何事。
// 这也顺带保住了 broadcast 注释里「推送本身是低频的」那个前提 ——
// 带宽是第一条每秒无条件触发的推送。
func (h *liveHub) recordBandwidth(st netstat.Stats) {
	h.bwMu.Lock()
	if h.bwBuf == nil {
		// 按固定长度建一次：长度始终是 bandwidthHistory，前 bandwidthHistory-1
		// 个位置在填满之前是零值（At 为零时间），补帧时前端按 available=false 处理
		h.bwBuf = make([]netstat.Stats, bandwidthHistory)
	}
	copy(h.bwBuf, h.bwBuf[1:])
	h.bwBuf[bandwidthHistory-1] = st
	h.bwMu.Unlock()

	if h.subscribers() == 0 {
		return
	}
	h.broadcast(mustJSON(liveMessage{Type: "bandwidth", Data: netstat.Frame{Latest: st}}))
}

// bandwidthSnapshot 返回当前的历史读数，供连接建立时补帧。
//
// 跳过零值：缓冲是定长的，服务刚启动时前面的位置还没被填过（At 为零时间）。
// 把那些「从未采样」的槽当成读数发给前端，曲线上会出现一段贴底的假平线，
// 大号读数也会显示成一张空网卡。
func (h *liveHub) bandwidthSnapshot() []netstat.Stats {
	h.bwMu.Lock()
	defer h.bwMu.Unlock()
	out := make([]netstat.Stats, 0, len(h.bwBuf))
	for _, st := range h.bwBuf {
		if st.At.IsZero() {
			continue
		}
		out = append(out, st)
	}
	return out
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

// 带宽采样节拍。1 秒是曲线可用的下限：再密（比如 200ms）会让前端每秒重绘
// 五次，而人眼在一条几十像素高的曲线上分辨不出差别；再疏则会把短突发摊平。
const liveBandwidthInterval = time.Second

// bandwidthInterval 是实际使用的节拍。默认取上面的常量；
// 测试里会改小 —— 与 heartbeatInterval 同一个做法。
var bandwidthInterval = liveBandwidthInterval

// 单帧写入的超时。正常帧只有几百字节，10 秒足够；
// 超时说明这个客户端已经不消费数据了，断开比无限等更合适。
const liveWriteTimeout = 10 * time.Second

// 心跳间隔（2026-09-24，配合前端 P1-10 的实时状态指示）。
//
// 为什么需要它：三条推送（logs / stats / health）全都是「变了才推」——
// 闲时**一帧都不发**。而前端的 useLive 靠「45 秒没收到任何消息」判断半开连接
// （看门狗，见 useLive.armWatchdog），于是深夜没有流量时它会把一条完全正常的
// 连接判成断开：重连、再判、再重连，看板上的「实时」指示灯周期性闪红，
// 而数据其实一直是新的。加指示灯本来是为了让「实时」二字可信，
// 误报比不加还糟。
//
// 为什么不把前端的判据放宽：那会把「真的断了」也一起拖慢 ——
// 而断线被察觉的速度正是这个指示灯的用处。让服务端定期说话，
// 判据就能同时保持「闲时可信」与「断时快报」。
//
// 25 秒：留出 20 秒余量给 45 秒的看门狗（单帧延迟 + 时钟漂移都在其中），
// 同时空载流量可以忽略（约 50 字节 / 25 秒 / 每个订阅者）。
const liveHeartbeatInterval = 25 * time.Second

// heartbeatInterval 是实际使用的节拍。默认取上面的常量；
// 测试里会改小 —— 一个真等 25 秒的用例不会有人愿意跑第二次。
var heartbeatInterval = liveHeartbeatInterval

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
	go s.liveBandwidthLoop(ctx)
}

// liveBandwidthLoop 每秒采一拍网卡计数并推出去。
//
// 为什么要单独一个循环、且**不看订阅者数**：
//
//  1. 节拍与统计不同（1s vs 2s）。挂在统计循环里意味着曲线每两秒才有一个点，
//     而带宽是瞬变量 —— 两秒一采会把一个 1.5 秒的突发摊薄成一半高度，
//     曲线上看不出「刚才猛了一下」。
//  2. 速率要差分才成立，差分依赖「上一拍是什么时候」。只在有人看的时候采样，
//     会让「没人看」的那几分钟变成一个大间隔，重新有人连上时第一拍算出来的是
//     那几分钟的平均值，看起来像一条平稳的直线 —— 而事实上中间发生过什么
//     谁也不知道。持续采样才能保证每拍都是 1 秒窗口。
//  3. 代价可以忽略：一次 /proc 读取加一次广播，闲时每秒几十微秒。
func (s *Server) liveBandwidthLoop(ctx context.Context) {
	ticker := time.NewTicker(bandwidthInterval)
	defer ticker.Stop()
	var iface string
	if s.deps.Config != nil {
		iface = s.deps.Config.Server.BandwidthInterface
	}
	clock := netstat.NewClock(iface)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.deps.Live.recordBandwidth(clock.Sample())
	}
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
	// client_map 帧的上次内容（映射缓存的容量 / 条目 / 淘汰 / 过期）
	var lastClientMap string
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
			// ---- 客户端映射缓存水位（client_map 帧）----
			//
			// 单独一帧而不是并进 health：health 的消费方（RequestLogPanel）
			// 把 data 当成 LogQueueInfo 用，往里塞新字段会让它读到的形状
			// 与类型声明不一致。这一帧回答的是「映射缓存是否在淘汰 / 过期」，
			// 排查「某个客户端的映射没登记上」时不必去翻进程日志。
			cs := s.deps.Logs.ClientCacheStats()
			if b, err := json.Marshal(cs); err == nil && string(b) != lastClientMap {
				lastClientMap = string(b)
				s.deps.Live.broadcast(mustJSON(liveMessage{Type: "client_map", Data: cs}))
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

// liveLogQuery 是实时推送取增量的唯一查询实现。
//
// 抽成函数是为了让集成测试（live_logs_query_test.go）跑**同一份**查询：
// 测试里另抄一份的话，改了生产代码而忘了改测试，用例就变成了自证清白。
//
// Omit 用 Go 字段名，不手写列名：见调用处关于 2026-10-20 事故的说明。
func liveLogQuery(db *gorm.DB, lastID uint) ([]model.RequestLog, error) {
	var rows []model.RequestLog
	err := db.
		Omit("client_ua", "pricing_snapshot", "retry_trail").
		Where("id > ?", lastID).Order("id").Limit(50).Find(&rows).Error
	return rows, err
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
	// lastLogErr 是上一次查询失败的错误文本，用于「只在错误变化时告警」。
	// 查询每秒一拍，持续失败时逐条记会在一分钟内刷出 60 行。
	var lastLogErr string

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
		// 只取列表要展示的列：client_ua 是 varchar(1024) 的原始 UA，列表
		// 不展示它（只在详情抽屉可见，走 /logs/:id 单独取），每秒最多 50 行
		// 全量推出去是纯粹的带宽浪费（2026-10-19 审查）。
		// 详情抽屉需要的字段（retry_trail / pricing_snapshot / payload 等）
		// 本来也不在里面。
		//
		// 用 Omit + **Go 字段名**而不是手写列名清单：列名清单曾经照着自己的
		// 判断写成 json tag 名（inbound_protocol / upstream_protocol / stream），
		// 而真实列名是 inbound_proto / upstream_proto / is_stream —— SQL 每次
		// 都报 column does not exist，错误又被下面的 continue 吞掉，表现为
		// 「日志列表不实时更新了」且服务端一行日志都没有（2026-10-20 线上事故）。
		// Omit 让 GORM 自己做字段→列的映射，加字段时也不必再维护清单。
		// 查询本身抽在 liveLogQuery（同包另一文件），让集成测试直接跑同一份。
		rows, err := liveLogQuery(db, last)
		if err != nil {
			// 不能静默 continue：这条查询是实时推送的唯一数据来源，
			// 它一直失败就等于功能一直不工作，而现场只有「前端不更新」。
			// 每行一条 WARN 会刷屏（1s 一拍），只在错误内容变化时记。
			if msg := err.Error(); msg != lastLogErr {
				lastLogErr = msg
				s.logger().Warn("实时日志查询失败，列表将不会更新", "err", err)
			}
			continue
		}
		lastLogErr = ""
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

// liveUpgrader 把 HTTP 连接升级成 WebSocket。
//
// 2026-09-27 从 golang.org/x/net/websocket 迁到 gorilla/websocket：
// 前者已冻结维护（golang.org/x/net 仓库明说 websocket 包处于维护者缺失状态），
// 后者自 2022 年起恢复积极维护，还带来 permessage-deflate 压缩 ——
// logs 帧忙时每秒最多 50 行 × 35 字段的 JSON，压缩后线上体积降一个量级，
// 浏览器与 useLive 都不需要任何改动（握手时透明协商）。
var liveUpgrader = websocket.Upgrader{
	ReadBufferSize: 1024,
	// 写缓冲故意取小值而不是默认 4096：gorilla 的已知问题是压缩开启时
	// 每条连接的 flate 上下文占用的内存随写缓冲增长（gorilla#621），
	// 而本端点单帧要么是几十字节的心跳、要么是整帧一次性 WriteMessage ——
	// 大于缓冲的载荷会绕过缓冲直写网络，小缓冲不产生额外系统调用。
	WriteBufferSize:   128,
	EnableCompression: true,
	// Origin 校验交给链路里已有的 sameOriginOnly 中间件（握手请求与普通
	// 请求走同一条链）；gorilla 默认的 CheckOrigin 拒绝一切带跨站 Origin 的
	// 升级，与「无 Origin 的命令行客户端放行」的既有口径冲突，这里放行。
	CheckOrigin: func(*http.Request) bool { return true },
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
	ws, err := liveUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// 失败时 Upgrade 已自己写掉错误响应（握手不合法回 400 等），
		// 这里只留痕：排查「连不上 /live」时唯一的服务端线索
		s.logger().Debug("实时推送连接升级失败", "err", err)
		return
	}
	defer ws.Close()

	id, ch := s.deps.Live.subscribe()
	defer s.deps.Live.unsubscribe(id)

	// 必须有人读：gorilla 与 x/net 一样只在读路径处理控制帧
	// （Ping → Pong、Close），只写不读的话客户端的关闭帧永远不被处理，
	// 订阅会一直留着 —— 而广播是「有变化才推」，空闲时可能几小时不推一次，
	// 靠写失败来发现断线是不可靠的。
	//
	// 这个通道上没有需要客户端上报的东西，读到的内容一律丢弃。
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// 连上先补一份当前快照：不然要等到下一次变化才有东西显示，
	// 而「打开页面后数字是空的」看起来就像坏了。
	// 与 liveStatsLoop 同一口径：今天 + 全站（零值筛选）
	//
	// Store 为 nil 时整段跳过（与下面 health 的 Logs != nil 同一个防御）：
	// 没有数据库就取不出汇总，而这条连接仍然该能建立、能收心跳 ——
	// 心跳的测试正是这样跑的（不起数据库）。
	if s.deps.Store != nil {
		start, end, _ := resolveRange("today")
		if data, err := s.summarySnapshot(start, end, statsFilter{}); err == nil {
			_ = ws.WriteMessage(websocket.TextMessage, mustJSON(liveMessage{Type: "stats", Data: data}))
		}
	}
	// 队列水位同样补一份：否则要等到它变化才显示（大多数时候队列是
	// 平稳的，那一等可能就是永远）
	if s.deps.Logs != nil {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(liveMessage{Type: "health", Data: s.deps.Logs.QueueStats()}))
		// 映射缓存水位同一条理由：它平时不变，不补首帧就永远看不到
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(liveMessage{Type: "client_map", Data: s.deps.Logs.ClientCacheStats()}))
	}

	// 带宽历史同样要补：曲线在打开那一刻就该是「过去一分钟发生过什么」，
	// 只推实时帧的话用户要盯着看满一分钟才能得到一条有意义的曲线。
	// 与上面几条不同，这一帧在 Store/Logs 都缺省时仍然要发 ——
	// 带宽采集不依赖数据库。
	//
	// 缓冲在填满之前含零值（服务刚启动时），这里按「已经采样过」过滤：
	// 零值 Stats 的 At 是零时间，直接发出去会让前端画出一段假的贴底曲线，
	// 并让 latest 显示成一台根本不存在的网卡。
	if hist := s.deps.Live.bandwidthSnapshot(); len(hist) > 0 {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(liveMessage{Type: "bandwidth", Data: netstat.Frame{
			Latest:  hist[len(hist)-1],
			History: hist,
		}}))
	}

	// WebSocket 连接不允许并发写，所以写只在这个 goroutine 里做。
	//
	// 心跳也走这里而不是另起一个 goroutine：并发写会被 gorilla 判为错误
	// （与 x/net 同样的约束），心跳必须和普通帧排在同一条写队列上。
	// 心跳带 `{"type":"heartbeat"}` 而不是空帧 —— 前端 dispatch 只认已知
	// type，未知的直接忽略；写成心跳类型让它可被日志与探针识别。
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-closed:
			return
		case <-heartbeat.C:
			// 与普通帧同一条写超时：客户端 TCP 缓冲区满时（标签页被挂起）
			// 没有 deadline 的写会把这条 goroutine 永久堵住
			_ = ws.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
			if err := ws.WriteMessage(websocket.TextMessage, mustJSON(liveMessage{Type: "heartbeat"})); err != nil {
				s.logger().Warn("实时心跳发送失败，断开该订阅", "err", err)
				return
			}
		case payload, ok := <-ch:
			if !ok {
				return
			}
			// 写超时：客户端 TCP 缓冲区满时（比如标签页被挂起）
			// 没有 deadline 的写会把这条 goroutine 永久堵住
			_ = ws.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
			if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
				s.logger().Warn("实时推送发送失败，断开该订阅", "err", err)
				return
			}
		}
	}
}
