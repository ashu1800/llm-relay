package relay

import (
	"testing"

	"llm-relay/internal/model"
)

func modelRequestLog(clientName string) model.RequestLog {
	return model.RequestLog{TraceID: "t", ModelRequested: "m", ClientName: clientName}
}

// 删除映射之后，下一次观测必须重新走登记路径。
//
// 回归的是 2026-10-19 审查发现的问题：管理接口删掉 client_aliases 行时
// 只动了数据库，而进程内缓存 —— 那个键在本进程内永远不会再登记成功，
// 注释与界面文案承诺的「下次被观测到会重新登记一行待设置」永远不会发生
// （只有重启时 backfill 才会把它插回来，连有意删除的也一起复活）。
//
// 这里只钉缓存语义，不碰数据库：newTestWriter 的库连不上，
// logClientAlias 的 Create 必然失败，失败路径本身会把键摘掉 ——
// 所以直接驱动 alias 缓存，断言 Remove 前后的 Register 判定。
func TestForgetClientAliasReArmsRegistration(t *testing.T) {
	w := newTestWriter(1)
	defer w.Close()

	// 模拟 loop 里登记的稳态：键已在缓存里
	if !w.alias.Add("dsh-harness").Register {
		t.Fatal("前置条件：首次 Add 应要求登记")
	}
	if w.alias.Add("dsh-harness").Register {
		t.Fatal("前置条件：第二次 Add 不应再要求登记")
	}

	w.ForgetClientAlias("dsh-harness")

	if !w.alias.Add("dsh-harness").Register {
		t.Fatal("删除映射后必须忘掉该键，否则本进程内永远不会重新登记")
	}

	// 空键是「没认出客户端」，不该进入缓存，也不该被当成有效操作
	w.ForgetClientAlias("")
	if got := w.alias.Stats().Entries; got != 1 {
		t.Fatalf("空键不该出现在缓存里，条目数 = %d", got)
	}
}

// 承接上面那条的端到端版本：走 logClientAlias（会尝试写 DB）时，
// 删除后再次观测也必须重新尝试登记 —— 库连不上，所以表现为「缓存里没有」。
func TestLogClientAliasRetriesAfterFailure(t *testing.T) {
	w := newTestWriter(1)
	defer w.Close()

	// 库连不上 → 写入失败 → 键被摘掉，下一次观测还要重试。
	// 旧实现的 WARN 在这里是「登记失败」，不是「已达上限」。
	w.logClientAlias(modelRequestLog("dsh-harness"))
	if !w.alias.Add("dsh-harness").Register {
		t.Fatal("写库失败后键应被摘掉，下一次观测要重新登记")
	}
}
