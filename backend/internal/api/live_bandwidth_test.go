package api

// 带宽实时推送的回归测试。
//
// 钉住三件事，每一件失效时界面上都不会报错、只会「安静地不对」：
//
//  1. 连接建立时补发一段历史。不补的话，打开看板后曲线要盯满一分钟才成形，
//     看起来就像坏了。
//  2. 采样循环在**没有订阅者**时也照样跑。这是速率差分的前提：只在有人看时
//     才采样，会让重连后的第一拍把整段空窗平均成一条假平线。
//  3. 帧的形状是 {"latest": {...}, "history": [...]}。前端靠它区分
//     「补历史」与「追加一个点」。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"llm-relay/internal/config"
	"llm-relay/internal/netstat"
)

func newLiveTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := &Server{
		deps: &Deps{
			Config: &config.Config{Server: config.ServerConfig{Port: 8888}},
			Live:   NewLiveHub(nil),
		},
		startedAt: time.Now(),
		keys:      newKeyCache(),
		lastTouch: map[uint]time.Time{},
	}
	r := gin.New()
	s.registerAdminRoutes(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return s, srv
}

func dialLive(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/admin/live"
	header := http.Header{}
	header.Set("Origin", srv.URL)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("WebSocket 连接失败：%v", err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

// readBandwidthFrame 读到第一帧 bandwidth，跳过其余类型（stats / health / heartbeat…）。
func readBandwidthFrame(t *testing.T, ws *websocket.Conn) netstat.Frame {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, payload, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("收帧失败（未收到 bandwidth）：%v", err)
		}
		var msg struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &msg) != nil || msg.Type != "bandwidth" {
			continue
		}
		var f netstat.Frame
		if err := json.Unmarshal(msg.Data, &f); err != nil {
			t.Fatalf("bandwidth 帧的 data 不是预期形状：%v（原文 %.200s）", err, msg.Data)
		}
		return f
	}
}

// 新连上的订阅者应当立刻拿到一段补位历史，而不是从空曲线开始。
func TestLiveSocketSendsBandwidthHistory(t *testing.T) {
	s, srv := newLiveTestServer(t)

	// 先在 hub 里塞几拍，模拟「进程已经跑了一会儿」
	for i := 0; i < 5; i++ {
		s.deps.Live.recordBandwidth(netstat.Stats{
			At: time.Now().UTC(), Interface: "eth0", RxBps: float64(i * 100), Available: true,
		})
	}

	ws := dialLive(t, srv)
	f := readBandwidthFrame(t, ws)
	if len(f.History) != 5 {
		t.Fatalf("补位历史应有 5 拍，实际 %d", len(f.History))
	}
	// latest 必须是最后一拍：前端拿它更新大号读数
	if f.Latest.RxBps != 400 {
		t.Fatalf("latest 应是最近一拍（400），实际 %.0f", f.Latest.RxBps)
	}
	if f.Latest.Interface != "eth0" {
		t.Fatalf("网卡名应透出，实际 %q", f.Latest.Interface)
	}
}

// 环形缓冲不该无限增长。
func TestBandwidthHistoryIsBounded(t *testing.T) {
	h := NewLiveHub(nil)
	for i := 0; i < bandwidthHistory+25; i++ {
		h.recordBandwidth(netstat.Stats{At: time.Now(), RxBps: float64(i), Available: true})
	}
	got := h.bandwidthSnapshot()
	if len(got) != bandwidthHistory {
		t.Fatalf("历史应被截到 %d 拍，实际 %d", bandwidthHistory, len(got))
	}
	// 截掉的必须是最旧的：最后一拍要留着
	if got[len(got)-1].RxBps != float64(bandwidthHistory+24) {
		t.Fatalf("截断后最后一拍应是 %d，实际 %.0f", bandwidthHistory+24, got[len(got)-1].RxBps)
	}
}

// 采样循环必须**在没有订阅者时也跑**：速率是差分出来的，
// 只在有人看时采样会让重连后的第一拍变成一大段时间的平均值。
func TestBandwidthLoopSamplesWithoutSubscribers(t *testing.T) {
	h := NewLiveHub(nil)
	s := &Server{deps: &Deps{Config: &config.Config{}, Live: h}}

	old := bandwidthInterval
	bandwidthInterval = 20 * time.Millisecond
	defer func() { bandwidthInterval = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.liveBandwidthLoop(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.bandwidthSnapshot()) >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("没有订阅者时采样循环也应持续采样，实际只采到 %d 拍", len(h.bandwidthSnapshot()))
}

// 订阅者为零时推送不该堆积：带宽帧是每秒一拍的常态流，
// 队列满时 liveHub 会丢最旧的一帧（已有实现），这里确认它不会阻塞采样循环。
func TestBandwidthBroadcastDoesNotBlock(t *testing.T) {
	h := NewLiveHub(nil)
	// 订阅一个永不消费的通道：队列塞满后仍应能继续推送
	h.subscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < liveQueueSize*4; i++ {
			h.recordBandwidth(netstat.Stats{At: time.Now(), RxBps: float64(i), Available: true})
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("订阅者不消费时广播被阻塞了")
	}
}
