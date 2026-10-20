package relay

import (
	"testing"
)

// 删除映射之后，下一次观测必须重新走登记路径。
//
// 回归的是 2026-10-19 审查发现的问题：管理接口删掉 client_aliases 行时
// 只动了数据库，而 seenAlias 是**进程级**缓存 —— 那个键在本进程内永远
// 不会再 LoadOrStore 成功，注释与界面文案承诺的「下次被观测到会重新登记
// 一行待设置」永远不会发生（只有重启时 backfill 才会把它插回来，
// 连有意删除的也一起复活）。
//
// 这里只钉缓存语义，不碰数据库：newTestWriter 的库连不上，
// ensureClientAlias 的 Create 必然失败，失败路径本身会把键摘掉 ——
// 所以断言要放在「第一次调用之后、失败摘键之前」无法介入的位置，
// 改为直接读 sync.Map 的状态。
func TestForgetClientAliasReArmsRegistration(t *testing.T) {
	w := newTestWriter(1)
	defer w.Close()

	// 失败路径会把键删掉，于是「已登记」这个状态无法用 ensureClientAlias
	// 制造出来。直接模拟 loop 里登记的稳态：键已在缓存里。
	w.seenAlias.Store("dsh-harness", struct{}{})
	if _, loaded := w.seenAlias.Load("dsh-harness"); !loaded {
		t.Fatal("前置条件：键应已在缓存里")
	}

	w.ForgetClientAlias("dsh-harness")

	if _, loaded := w.seenAlias.Load("dsh-harness"); loaded {
		t.Fatal("删除映射后必须忘掉该键，否则本进程内永远不会重新登记")
	}

	// 空键是「没认出客户端」，不该进入缓存，也不该被当成有效操作
	w.ForgetClientAlias("")
	if _, loaded := w.seenAlias.Load(""); loaded {
		t.Fatal("空键不该出现在缓存里")
	}
}
