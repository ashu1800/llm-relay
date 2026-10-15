package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
)

// ==================== 客户端显示映射 ====================

// registerClientAliasRoutes 注册客户端显示映射（对照表）的管理接口。
//
// 行的产生路径只有「日志观测到这个客户端」（logwriter 自动登记 +
// 启动回填，见 model.ClientAlias 的注释），所以这里没有 POST ——
// 手工造一行「映射一个从未调用过的客户端」没有意义，key 也无从校验。
// 管理动作就三个：看（哪些待设置）、改（配名字/图标）、删（不想要
// 这条映射了，显示回落到内置表/快照名）。
func registerClientAliasRoutes(g *gin.RouterGroup, s *Server) {
	r := g.Group("/client-aliases")
	r.GET("", s.listClientAliases)
	r.PUT("/:id", s.updateClientAlias)
	r.DELETE("/:id", s.deleteClientAlias)
}

// listClientAliases 全量返回。这张表每行对应「一个观测过的客户端」，
// 量级是几十到几百（不是日志量），分页只会让「还有几条待设置」变难看到。
func (s *Server) listClientAliases(c *gin.Context) {
	var items []model.ClientAlias
	if err := s.deps.Store.DB().Find(&items).Error; err != nil {
		writeInternalError(c, err)
		return
	}
	// 在 Go 里排序而不是塞进 ORDER BY：待设置（name 空）排最前 —— 打开
	// 面板第一眼看到的就是「等我来配的」；其余按更新时间倒序。
	// 表小，全量读回再排的代价可以忽略，还避开了 SQL 里的方言细节。
	sort.SliceStable(items, func(i, j int) bool {
		pi, pj := items[i].Name == "", items[j].Name == ""
		if pi != pj {
			return pi // 待设置在前
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// clientAliasUpdatePayload 只允许改显示名与图标 —— Key 是日志观测出来
// 的身份，改了它等于把这行映射挂到另一个客户端头上（那是造新行的事，
// 而造行只能靠日志观测）。两个都传 nil 时报错，与密钥/分组的局部更新
// 同一约定。
type clientAliasUpdatePayload struct {
	Name *string `json:"name"`
	Icon *string `json:"icon"`
}

func (s *Server) updateClientAlias(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p clientAliasUpdatePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		writeUpstreamError(c, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	updates := map[string]any{}
	if p.Name != nil {
		// 显式传空串 = 回到「待设置」（显示层回落内置/快照），是合法操作
		name := strings.TrimSpace(*p.Name)
		if len([]rune(name)) > 64 {
			writeUpstreamError(c, http.StatusBadRequest, "显示名过长（上限 64 字符）", "invalid_request_error")
			return
		}
		updates["name"] = name
	}
	if p.Icon != nil {
		// 图标口径与渠道图标一致：data URI / 图片地址 / 一两个字符 /
		// 内置图标文件名，形状由前端按内容识别，这里只限体量
		icon := strings.TrimSpace(*p.Icon)
		if len(icon) > 128<<10 {
			writeUpstreamError(c, http.StatusBadRequest, "图标内容过长（上限 128KB）", "invalid_request_error")
			return
		}
		updates["icon"] = icon
	}
	if len(updates) == 0 {
		writeUpstreamError(c, http.StatusBadRequest, "没有需要更新的字段", "invalid_request_error")
		return
	}
	db := s.deps.Store.DB()
	if err := applyUpdates(db, &model.ClientAlias{}, id, updates); err != nil {
		writeUpdateError(c, err)
		return
	}
	// 回读整行：前端保存后直接用返回值刷新本地列表（updated_at 也变了）
	var row model.ClientAlias
	if err := db.First(&row, id).Error; err != nil {
		writeUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated": len(updates), "item": row})
}

func (s *Server) deleteClientAlias(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	// 删除只是「不想要这条映射」：这个客户端下次再被观测到会重新登记
	// 一行待设置（logwriter 的缓存只记本进程见过的键，不记得删过）。
	// 想让它不再出现，把名字配成与原样一致即可 —— 那才是「映射成原样」
	// 而不是「假装没见过」。
	deleteByID(c, s.deps.Store.DB(), &model.ClientAlias{}, id, "映射不存在")
}
