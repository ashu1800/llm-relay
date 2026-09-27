package api

import (
	"compress/gzip"
	"strings"

	"github.com/gin-gonic/gin"
)

// gzipResponses 给管理接口的响应加 gzip 压缩。
//
// 为什么现在才做：静态资源早就有一条预压缩链（前端构建时生成 .gz，
// static.go 按 Accept-Encoding 伺服），但 API 响应一直是裸传的。管理台
// 最大的三类响应——日志列表（200 行 × 35 字段全字段无投影）、CSV 导出
// （上限 2 万行）、配置备份 JSON——全是键名与结构高度重复的文本，
// gzip 后典型能压到 1/5 ~ 1/10。而管理台只有站长一个人在用，
// 每个响应现场压一次的 CPU 成本可以忽略。
//
// 为什么是中间件而不是在 handler 里各自压缩：69 处 c.JSON 调用点
// 不可能逐个改，而且 handler 侧压缩会把「协商了但客户端不要」这类
// 判断重复 69 遍。
//
// 刻意不动的地方：
//   - /v1 转发链路 —— 协议锁定 JSON/SSE，且是流式透传，压缩会破坏
//     逐块转发的首字延迟（不在本中间件的挂载组内，物理隔离）；
//   - 静态资源 —— 已有构建期预压缩，现场压反而浪费；
//   - 前端 —— fetch 对 Content-Encoding: gzip 透明解压，零改动。
func gzipResponses() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 协商失败（没带 gzip / 明确 q=0 / 只声明了别的编码）原样放行，
		// 连包装器都不套：与不压缩的行为完全一致。
		if !acceptsGzip(c.GetHeader("Accept-Encoding")) {
			c.Next()
			return
		}
		w := &gzipResponseWriter{ResponseWriter: c.Writer}
		c.Writer = w
		c.Next()
		// gzip 流必须显式收尾（写出校验和与结束块），漏了这一步
		// 客户端拿到的是无法解压的截断流
		w.close()
	}
}

// gzipResponseWriter 惰性压缩的响应包装器。
//
// 「惰性」指第一个响应字节落地前才决定压不压：那时 Content-Type 已经
// 定型，可以按类型排除（见 decide）。提前决策的问题是 gin 的 handler
// 在中间件返回之后才写响应，中间件阶段看不到 Content-Type。
//
// 通过内嵌 gin.ResponseWriter 接口，Hijack / CloseNotify / Status / Size
// 等方法全部自动委托底层——WebSocket（/live）升级时 gorilla 的 Upgrader
// 对底层连接调 Hijack，发生在任何字节写入之前，压缩从未启用，
// 升级路径与加中间件之前逐字节一致。
type gzipResponseWriter struct {
	gin.ResponseWriter
	// gz 为 nil 表示「尚未决策」或「已决定旁路」两者之一，
	// 配合 decided 区分。压缩启用后所有字节先过它。
	gz      *gzip.Writer
	decided bool
}

// decide 在第一个响应字节前做一次性决策。幂等。
func (w *gzipResponseWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true
	h := w.Header()
	// 已经带 Content-Encoding 的响应（理论上来自更内层的处理）不能再压：
	// 双重编码没有客户端解得开。管理组当前没有这类响应，防御性排除。
	if h.Get("Content-Encoding") != "" {
		return
	}
	// SSE 是流式协议，帧必须实时送达，整段缓冲进 gzip 会把
	// 「服务端推送」变成「连接关闭后一次性到达」。管理组当前没有 SSE
	// 端点（relay 的 SSE 在 /v1 组），将来出现时这里自动放行。
	if strings.HasPrefix(h.Get("Content-Type"), "text/event-stream") {
		return
	}
	h.Set("Content-Encoding", "gzip")
	// 压缩后长度变了，留着过期的 Content-Length 会让客户端按错长度截断
	h.Del("Content-Length")
	h.Add("Vary", "Accept-Encoding")
	// gzip 输出直接写给内嵌的 gin.ResponseWriter：它自己的 Write 会先
	// WriteHeaderNow 落状态头并维护 size 统计，且不会被本类型的 Write
	// 覆盖拦截（接口值持有的是 gin 的实现，不是本包装器）
	w.gz = gzip.NewWriter(w.ResponseWriter)
}

func (w *gzipResponseWriter) Write(data []byte) (int, error) {
	w.decide()
	if w.gz == nil {
		return w.ResponseWriter.Write(data)
	}
	// 状态头必须先于第一个压缩字节写出；不压缩路径由内嵌 Write 自动做
	w.ResponseWriter.WriteHeaderNow()
	return w.gz.Write(data)
}

// WriteString 必须一并重载：gin 的部分 render 路径走 WriteString 而不是
// Write，漏了它会出现「头声明 gzip、体却是明文」的腐坏响应。
// gzip.Writer 没有 WriteString，按 []byte 写。
func (w *gzipResponseWriter) WriteString(s string) (int, error) {
	w.decide()
	if w.gz == nil {
		return w.ResponseWriter.WriteString(s)
	}
	w.ResponseWriter.WriteHeaderNow()
	return w.gz.Write([]byte(s))
}

// Flush 要把 gzip 的缓冲先压出去再透传底层的 Flush：
// restart 端点依赖「先把响应刷给客户端、再退出进程」的时序，
// 只透传不压缓冲的话刷出去的是不完整的 gzip 流。
func (w *gzipResponseWriter) Flush() {
	w.decide()
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	w.ResponseWriter.Flush()
}

func (w *gzipResponseWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
		w.gz = nil
	}
}
