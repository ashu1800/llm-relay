package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	relaycache "llm-relay/internal/relay/cache"
)

// 响应缓存的管理接口（2026-11 P3，挂在 /api/admin/cache 下，走管理台
// 同一条鉴权链）。
//
//   GET    /api/admin/cache/stats  运行快照（条目/字节/命中率/配置）
//   DELETE /api/admin/cache        清空全部条目（命中统计保留 —— 命中率
//                                  是「这个功能有没有用」的长期证据）
//
// 没有运行期配置接口：缓存的总开关与容量走 env/yaml（与其它 Relay 配置
// 同一哲学，改后重启生效），密钥级的开关在「密钥管理」页。管理台里展示
// 的是**只读状态 + 清空动作** —— 一个能中途改缓存语义的开关，只会让
// 「为什么这次命中了上次没命中」变得更难回答。

// cacheStats 返回缓存运行快照。功能关闭时 enabled=false，其余字段为 0 值
// （cache 包的 nil 安全使然）—— 界面据此显示「未启用」而不是报错。
func (s *Server) cacheStats(c *gin.Context) {
	st := s.deps.Cache.Stats()
	st.Enabled = s.deps.Cache != nil
	c.JSON(http.StatusOK, st)
}

// cacheFlush 清空缓存条目。
func (s *Server) cacheFlush(c *gin.Context) {
	if s.deps.Cache == nil {
		writeUpstreamError(c, http.StatusNotImplemented, "响应缓存未启用", "not_implemented")
		return
	}
	n := s.deps.Cache.Flush()
	c.JSON(http.StatusOK, gin.H{"ok": true, "cleared": n})
}

// registerCacheRoutes 挂载缓存管理路由（admin 组内，与其它管理接口同一鉴权链）。
func registerCacheRoutes(admin *gin.RouterGroup, s *Server) {
	g := admin.Group("/cache")
	g.GET("/stats", s.cacheStats)
	g.DELETE("", s.cacheFlush)
}

// profileCacheable 报告该入站端点是否参与响应缓存：只有四个对话族端点
// （openai-chat / responses / anthropic / gemini）。直通端点（图像/音频）
// 的响应没有「同一请求同一答案」的语义基础（图像生成天然随机），embeddings
// 虽然确定但量小频低，都不值得进 v1。
func profileCacheable(p *inboundProfile) bool {
	return !p.Direct && p != profileEmbeddings
}

// cacheKeyOf 计算请求的缓存键。参数与 cache.CacheKey 一一对应：
// 归一化后的 body（OpenAI Chat 形状）+ 对外模型名 + 流式形态。
func cacheKeyOf(p *inboundProfile, publicModel string, body []byte, stream bool) string {
	return relaycache.CacheKey(publicModel, body, stream)
}
