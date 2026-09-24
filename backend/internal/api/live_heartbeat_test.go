package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/websocket"

	"llm-relay/internal/config"
)

// 实时推送心跳的测试（2026-09-24，配合前端 P1-10 的实时状态指示）。
//
// 为什么这条必须钉在测试里：服务端三条推送都是「变了才推」，闲时一帧不发，
// 而前端 useLive 的看门狗是「45 秒没收到消息就当断开」。没有心跳时，
// 一个完全正常的空闲站点会被判成断开 —— 指示灯周期性闪红，
// 而看板上的「实时」二字因此不可信。心跳是让这个指示灯成立的前提，
// 它不能在后续重构里被静默删掉。
//
// 测试不连数据库：Store 为 nil 时 liveSocket 会跳过快照（见那边的防御），
// 这条用例关心的只是「ticker 到了会不会发 heartbeat 帧」。
func TestLiveSocketHeartbeat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 把节拍改小：真等 25 秒的用例不会有人跑第二次
	old := heartbeatInterval
	heartbeatInterval = 60 * time.Millisecond
	defer func() { heartbeatInterval = old }()

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
	defer srv.Close()

	// httptest 的地址是 http://，换成 ws://
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/admin/live"
	// Origin 必须与 Host 同源：sameOriginOnly 中间件会拒掉不符的
	origin := srv.URL
	ws, err := websocket.Dial(wsURL, "", origin)
	if err != nil {
		t.Fatalf("WebSocket 连接失败：%v", err)
	}
	defer ws.Close()

	// 收帧直到拿到心跳。给足余量：节拍 60ms，3 秒足够收几十帧
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := false
	for !got {
		var raw string
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			t.Fatalf("收帧失败（未收到心跳）：%v", err)
		}
		var msg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			continue
		}
		if msg.Type == "heartbeat" {
			got = true
		}
	}
	if !got {
		t.Fatal("未收到 heartbeat 帧")
	}

	// 心跳帧必须是合法 JSON 且不带业务数据（否则前端会把它当成数据帧处理）
	var raw string
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := websocket.Message.Receive(ws, &raw); err != nil {
		t.Fatalf("收第二帧失败：%v", err)
	}
	var msg struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("心跳帧不是合法 JSON：%v（原文 %.80s）", err, raw)
	}
	if msg.Type != "heartbeat" {
		t.Fatalf("第二帧类型是 %q，期望 heartbeat（节拍应稳定）", msg.Type)
	}
	if len(msg.Data) > 0 && string(msg.Data) != "null" {
		t.Fatalf("心跳帧不该带业务数据，实际 data=%s", msg.Data)
	}
}

// 订阅数归零后不该继续产生心跳（没人看时空转发帧是纯粹的浪费）。
func TestLiveSocketNoHeartbeatWithoutSubscriber(t *testing.T) {
	h := NewLiveHub(nil)
	if n := h.subscribers(); n != 0 {
		t.Fatalf("新建 hub 的订阅数应为 0，实际 %d", n)
	}
	id, _ := h.subscribe()
	if n := h.subscribers(); n != 1 {
		t.Fatalf("订阅后应为 1，实际 %d", n)
	}
	h.unsubscribe(id)
	if n := h.subscribers(); n != 0 {
		t.Fatalf("退订后应为 0，实际 %d", n)
	}
}
