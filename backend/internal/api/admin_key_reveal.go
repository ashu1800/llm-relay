package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/model"
)

// revealKey 取出某把密钥的明文，供界面上「悬浮查看 / 点击复制」使用。
//
// 永远返回 200 + 一个说明，而不是用 4xx 表达「这把密钥看不到明文」：
// 对界面来说那是**预期内的状态**（升级前创建的密钥只存过哈希），
// 用错误码表达会让前端只能弹一句「请求失败」。
func (s *Server) revealKey(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var k model.APIKey
	if err := s.deps.Store.DB().First(&k, id).Error; err != nil {
		writeUpstreamError(c, http.StatusNotFound, "密钥不存在", "not_found_error")
		return
	}
	if k.KeyEnc == "" {
		c.JSON(http.StatusOK, gin.H{
			"available": false,
			"reason":    "这把密钥创建于加密存储之前，只保留了哈希，明文无法找回；如需完整密钥请新建一把",
		})
		return
	}
	if s.deps.Cipher == nil {
		c.JSON(http.StatusOK, gin.H{"available": false, "reason": "服务未配置主密钥，无法解密"})
		return
	}
	plain, err := s.deps.Cipher.Decrypt(k.KeyEnc)
	if err != nil {
		// 换过 RELAY_SECRET 就会走到这里：说清楚原因，别让人以为密钥坏了
		c.JSON(http.StatusOK, gin.H{
			"available": false,
			"reason":    "密钥解密失败（主密钥是否变过？）；这把密钥本身仍然可用，只是看不到明文了",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"available": true, "key": plain})
}
