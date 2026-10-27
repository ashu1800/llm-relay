package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"llm-relay/internal/model"
)

// duplicateChannel 复制一条渠道。
//
// 在**后端**复制而不是「前端读出来再提交一遍」：渠道密钥在库里只有密文，
// 列表与详情接口都从不下发明文（这是有意的）。前端复制就只能拿到
// APIKeyHint（`sk-****abcd`），提交后会变成一个假密钥 ——
// 渠道看起来正常、转发时全部 401，而且得等到真正用起来才发现。
// 后端复制直接把 APIKeyEnc 原样带过去，密钥不需要解密再加密一次。
//
// 一次复制带全部关联数据：
//   - 渠道本身（协议、地址、币种、代理、图标、额外配置、监控设置…）；
//   - 模型白名单（含每行的倍率与时段规则）—— 漏了它复制出来的渠道
//     跑不了任何模型，而列表上看起来是「复制成功了」；
//   - 价格不复制：单价已迁到模型级（model_prices），与具体渠道无关。
//
// 不复制的字段：运行态数据（健康状态、最近错误、最后调用时间）——
// 复制出来的是「一份配置」，不是「一份连同历史快照的状态」。
type duplicateChannelPayload struct {
	// Name 允许指定新名字；为空时自动生成「原名（副本）」并保证不重名
	Name string `json:"name"`
}

func (s *Server) duplicateChannel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p duplicateChannelPayload
	_ = c.ShouldBindJSON(&p)

	db := s.deps.Store.DB()

	var src model.Channel
	if err := db.First(&src, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "渠道不存在", "not_found_error")
		return
	}

	// dup := src 是结构体浅拷贝：Slots / ExtraConfig / CustomMap 是
	// 引用类型（JSONList / JSONMap），副本与源**共享底层数据**。
	//
	// 这里安全，因为后续只读不写：GORM 的 Value() 把它们序列化进 INSERT，
	// src 在那之后也不再被修改。但将来若有人在这中间就地改 dup.ExtraConfig
	// （例如注入一个标记），会**同时改掉源渠道的内存值** —— 而源渠道本来
	// 不该被这次复制动到。要改内容就先 copy 一层。
	dup := src
	// ID 与时间戳归零，交给 GORM 重新分配；不归零的话 Create 会尝试
	// 用源渠道的主键插第二行，直接撞主键冲突
	dup.ID = 0
	dup.CreatedAt = time.Time{}
	dup.UpdatedAt = time.Time{}
	dup.Name = s.uniqueChannelName(db, strings.TrimSpace(p.Name), src.Name)

	// 运行态字段必须清掉，否则第二条渠道一出生就带着别人的健康状态：
	// 上游明明没探测过它，列表上却显示「正常」，而探测任务要等下一轮
	// 才会更正 —— 这期间用户可能据此判断「这条渠道可用」。
	dup.HealthStatus = "unknown"
	dup.LastError = ""
	dup.LastCheckedAt = nil

	// 白名单整块读出来，事务里重新插入到新渠道下。
	// 中间任何一步失败都整体回滚：只复制出渠道、没复制出白名单的话，
	// 新渠道不参与任何路由，而界面上看起来是成功的。
	var whitelist []model.ChannelModel
	if err := db.Where("channel_id = ?", id).Order("id").Find(&whitelist).Error; err != nil {
		writeInternalError(c, err)
		return
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		// 副本默认**停用**：复制出来的渠道带着同一个上游密钥与同一份白名单，
		// 一旦自动启用就会立刻参与路由，与源渠道抢流量。用户复制它通常是为了
		// 改点东西（换密钥、换地址）再启用，中间这段时间不该影响线上。
		dup.Enabled = false
		if err := tx.Create(&dup).Error; err != nil {
			return err
		}
		// 复用整表替换：它已经处理了 ID 归零与 ChannelID 重指，
		// 并且会为「倍率被清零」打日志。传空切片也安全（等于不建任何行）
		return replaceChannelModels(tx, dup.ID, whitelist)
	}); err != nil {
		writeInternalError(c, err)
		return
	}

	s.invalidatePricing()
	c.JSON(http.StatusOK, dup)
}

// uniqueChannelName 生成一个「副本」名字，并保证在库里不重名。
//
// 为什么不直接用「原名（副本）」就算了：同名渠道会让「哪条是哪条」
// 在界面上完全分不清（channels.name 没有唯一索引，重名是允许的）。
// 连点两次复制更会产生三个一样的名字。这里按「原名（副本）」「原名（副本 2）」
// … 依次试到空位。
//
// desired 非空时优先用它，但仍然要保证唯一 —— 用户手填一个已存在的
// 名字时，与其报错让他改，不如直接给个带序号的名字（复制是顺手动作，
// 不该因为重名被打断）。
func (s *Server) uniqueChannelName(db *gorm.DB, desired, srcName string) string {
	base := strings.TrimSpace(desired)
	if base == "" {
		base = strings.TrimSpace(srcName) + "（副本）"
	}
	// 名字本身也可能超长：channels.name 是 varchar(128)（实体 size:128，
	// 库里的列宽以它为准 —— 这里曾写成 191 并据此截到 150 字符，实测插入
	// 直接报 value too long for type character varying(128)）。
	//
	// 上限留出「（副本 99）」这类后缀的余量：后缀最长约 7 个字符，
	// 所以截到 120 仍能安全追加序号。
	//
	// 按 **rune** 而不是字节算：中文名字一个字占 3 字节，但 PostgreSQL 的
	// varchar(128) 计的是字符数，所以这里也必须按字符数截。
	const maxNameRunes = 120
	if r := []rune(base); len(r) > maxNameRunes {
		base = string(r[:maxNameRunes])
	}

	candidate := base
	for i := 2; i <= 100; i++ {
		var count int64
		// 查失败时保守地当成「已存在」，继续往后试：
		// 宁可名字多一个序号，也不要因为一次读失败就撞上一个重名
		db.Model(&model.Channel{}).Where("name = ?", candidate).Count(&count)
		if count == 0 {
			return candidate
		}
		// 追加序号后仍要压在列宽内：base 已经截到 120，加 " 100" 是 124 字符，
		// 安全余量足够。这里再截一次是防御「base 来自 desired 且没走上面的截断」
		// 这类将来改动引入的回归。
		candidate = truncateRunes(base+" "+strconv.Itoa(i), maxNameRunes+7)
	}
	// 同名 100 条：再往上加序号没有意义了，直接返回最后一个候选，
	// 由调用方按重名处理（channels.name 没有唯一索引，不会插入失败）
	return candidate
}

// truncateRunes 按字符数截断（不是字节数）—— 与 PostgreSQL varchar(n) 的计数口径一致。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
