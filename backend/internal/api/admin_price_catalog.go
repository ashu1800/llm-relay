package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/pricing"
)

// 参考价库的管理接口（挂在 /admin/model-prices/catalog 下）。
//
// 定位：**建议价源**的只读查询 + 手动更新。它不参与记账 —— 记账只认
// model_prices（见 pricing.Engine.Resolve）；漏配价也不会被参考价兜底，
// 「漏配点名」的界面语义原样保留。
//
//   GET  /admin/model-prices/catalog/status   目录元信息（来源/新鲜度/条目数）
//   GET  /admin/model-prices/catalog/search   关键词搜索（定价页搜索弹窗用）
//   POST /admin/model-prices/catalog/refresh  从上游拉最新原文件 → 解析裁剪 → 落库
//
// 没有服务端「一键填充」写接口：填充发生在前端表单里（选中候选 →
// 回填编辑表单 → 用户确认后走既有的 PUT upsert 保存）。价格是账本，
// 落库前必须过一次人眼，这正是「建议价源」与「自动兜底」的分界线。

// catalogURL 是上游价格表地址。固定而不是可配：可配地址等于允许
// 管理台把「价格建议」指向任意来源，而白名单校验（validateDownloadURL）
// 的意义就在固定主机。上游换了地址时改这里重新发布即可。
const catalogURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// catalogStatus 返回当前生效目录的元信息。
func (s *Server) catalogStatus(c *gin.Context) {
	if s.deps.Catalog == nil {
		writeInternalError(c, errors.New("参考价库未装配"))
		return
	}
	st, err := s.deps.Catalog.Status(c.Request.Context())
	if st.Source == "" && err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

// catalogSearch 按关键词搜索目录。
//
// 查询参数：q（关键词）、mode（可选，上游 mode 词表过滤）、limit（默认 20）。
// 空关键词 + mode 是「浏览」语义（按模式看目录里有什么）。
func (s *Server) catalogSearch(c *gin.Context) {
	if s.deps.Catalog == nil {
		writeInternalError(c, errors.New("参考价库未装配"))
		return
	}
	cat, err := s.deps.Catalog.Get(c.Request.Context())
	if cat == nil {
		writeInternalError(c, err)
		return
	}
	limit := 20
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	hits := cat.Search(c.Query("q"), strings.TrimSpace(c.Query("mode")), limit)

	// 目录新鲜度随结果一起回：搜索弹窗要在同一次交互里告诉用户
	// 「这些候选来自哪一版目录」，单独一次 status 请求纯属浪费。
	st, stErr := s.deps.Catalog.Status(c.Request.Context())
	resp := gin.H{"items": hits}
	if stErr == nil {
		resp["builtin"] = st.Builtin
		resp["fetched_at"] = st.FetchedAt
	}
	c.JSON(http.StatusOK, resp)
}

// catalogRefresh 从上游拉最新原文件并落库。
//
// 下载走更新通道（update.Service.FetchFile）：与版本更新同一份代理优先级
// 与目标校验，管理台里为「版本更新」配的代理对这里同样生效 —— 不做第二份
// 配置的原因与 netguard 不拦勾选代理是同一条：同一类信任，配一遍就够。
func (s *Server) catalogRefresh(c *gin.Context) {
	if s.deps.Catalog == nil || s.deps.Update == nil {
		writeInternalError(c, errors.New("参考价库或更新服务未装配"))
		return
	}
	// 分钟级下载掐到 90 秒：fetcher 客户端本身是 10 分钟超时（为更新归档
	// 设置的），管理台请求不能等那么久 —— 超时后界面报「下载超时」，
	// 用户换个网络再点一次即可。
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()

	data, err := s.deps.Update.FetchFile(ctx, catalogURL, pricing.MaxCatalogRawBytes)
	if err != nil {
		if ctx.Err() != nil {
			writeUpstreamError(c, http.StatusGatewayTimeout, "下载参考价库超时（90 秒），可在「系统设置 → 版本更新」配代理后重试", "upstream_error")
			return
		}
		writeUpstreamError(c, http.StatusBadGateway, "下载参考价库失败: "+err.Error(), "upstream_error")
		return
	}

	fetchedAt := time.Now().UTC().Format(time.RFC3339)
	cat, err := s.deps.Catalog.ReplaceCatalog(c.Request.Context(), data, fetchedAt, catalogURL)
	if err != nil {
		// 解析失败说明上游内容变了或传输损坏：这类错误不值得重试，
		// 500 带原文比 502 更能引导用户来看具体原因
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"entries":    len(cat.Entries),
		"fetched_at": fetchedAt,
		"source":     catalogURL,
	})
}

// registerCatalogRoutes 把参考价库路由挂到 model-prices 组下。
// 放进 registerModelPriceRoutes 是刻意的：目录是定价页的附属功能，
// 与 model-prices 同组意味着同一个鉴权与日志中间件链。
func registerCatalogRoutes(g *gin.RouterGroup, s *Server) {
	cg := g.Group("/catalog")
	cg.GET("/status", s.catalogStatus)
	cg.GET("/search", s.catalogSearch)
	cg.POST("/refresh", s.catalogRefresh)
}
