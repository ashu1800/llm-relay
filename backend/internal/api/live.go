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
			// 队列满：丢掉这条而不是等待。实时数值下一条马上就到，
			// 卡住广播方则会让所有连接一起变慢
			if h.logger != nil {
				h.logger.Warn("实时推送队列已满，丢弃一帧", "subscriber", id)
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

// 推送节奏。统计每 2 秒算一次（聚合查询不便宜，而看板上的数字
// 慢一拍没人会察觉）；日志每 1 秒查一次增量（它是「实时」的主要观感）。
const liveStatsInterval = 2 * time.Second
const liveLogsInterval = time.Second

// StartLive 启动实时推送循环，直到 ctx 结束。
func (s *Server) StartLive(ctx context.Context) {
	if s.deps.Live == nil {
		return
	}
	go s.liveStatsLoop(ctx)
	go s.liveLogsLoop(ctx)
}

func (s *Server) liveStatsLoop(ctx context.Context) {
	statsTicker := time.NewTicker(liveStatsInterval)
	defer statsTicker.Stop()
	var lastSent string
	for {
		select {
		case <-ctx.Done():
			return
		case <-statsTicker.C:
		}
		// 没人订阅就不查：聚合查询要扫今天的所有日志
		if s.deps.Live.subscribers() == 0 {
			continue
		}
		start, end, _ := resolveRange("today")
		data, err := s.summarySnapshot(start, end)
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

func (s *Server) liveLogsLoop(ctx context.Context) {
	ticker := time.NewTicker(liveLogsInterval)
	defer ticker.Stop()
	db := s.deps.Store.DB()

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

		// 连上先补一份当前快照：不然要等到下一次变化才有东西显示，
		// 而「打开页面后数字是空的」看起来就像坏了
		if start, end, _ := resolveRange("today"); true {
			if data, err := s.summarySnapshot(start, end); err == nil {
				_ = websocket.Message.Send(ws, string(mustJSON(liveMessage{Type: "stats", Data: data})))
			}
		}

		// WebSocket 连接不允许并发写，所以写只在这个 goroutine 里做
		for payload := range ch {
			if err := websocket.Message.Send(ws, string(payload)); err != nil {
				if s.logger() != nil {
					s.logger().Warn("实时推送发送失败，断开该订阅", "err", err)
				}
				return
			}
		}
	}).ServeHTTP(c.Writer, c.Request)
}
