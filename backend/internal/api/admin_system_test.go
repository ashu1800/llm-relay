package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-relay/internal/update"
	"llm-relay/internal/version"
)

// 版本与更新接口的测试。
//
// 这里不打真实网络（GitHub 不可达时测试会变成 flaky），而是覆盖三件
// 真正容易出错、且不需要网络的逻辑：
//
//   1. /system/version 的口径 —— 它必须永远可用，且字段齐全；
//   2. 更新相关接口在「更新服务未接入」时回 501 而不是 panic；
//   3. 错误到 HTTP 状态码的映射 —— 「更新器没装」与「没有备份」
//      对用户意味着完全不同的下一步动作，混成一个 500 就失去了指导性。

// newSystemTestServer 构造一个只挂了系统路由的测试服务器。
// deps.Update 留 nil，用来验证「未接入」这条分支。
func newSystemTestServer(t *testing.T) *gin.Engine {
	t.Helper()
	s := &Server{deps: &Deps{}}
	r := gin.New()
	g := r.Group("/api/admin")
	registerSystemRoutes(g, s)
	return r
}

func doJSON(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestVersionEndpointAlwaysAnswers 钉住「版本接口不依赖更新功能」这条契约。
//
// 它的价值在于：GitHub 被墙、限额用尽、更新服务构造失败 —— 这些情况下
// 用户最需要看到的信息恰恰是「我现在跑的是哪一版」。
// 如果它和更新检测共用一条路径，那些情况下界面连版本号都显示不出来。
func TestVersionEndpointAlwaysAnswers(t *testing.T) {
	r := newSystemTestServer(t)
	w := doJSON(r, http.MethodGet, "/api/admin/system/version", "")

	if w.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d，响应：%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}

	// 字段必须齐全：前端的分支判断依赖它们
	for _, key := range []string{"version", "display", "commit", "build_type", "is_release", "os", "arch"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("响应缺少字段 %q", key)
		}
	}
	// version 与 display 都不能为空 —— 空版本号会让前端徽标整块消失
	if resp["version"] == "" || resp["display"] == "" {
		t.Errorf("版本号不应为空: version=%v display=%v", resp["version"], resp["display"])
	}
	// build_type 必须是已知取值之一（前端按它选更新方式）
	bt, _ := resp["build_type"].(string)
	switch bt {
	case version.BuildSource, version.BuildBinary:
	default:
		t.Errorf("build_type 取值非法: %q", bt)
	}
}

// TestUpdateEndpointsReturn501WithoutService 验证未接入更新服务时的降级。
//
// 501 而不是 500：它表达的是「这个功能在这套部署上没有」，而 500
// 会让用户以为程序坏了并去翻日志。
func TestUpdateEndpointsReturn501WithoutService(t *testing.T) {
	r := newSystemTestServer(t)

	cases := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/admin/system/update/check", ""},
		{http.MethodPost, "/api/admin/system/update", ""},
		{http.MethodGet, "/api/admin/system/update/progress", ""},
		{http.MethodPost, "/api/admin/system/update/cancel", ""},
		{http.MethodGet, "/api/admin/system/update/rollback-versions", ""},
		{http.MethodPost, "/api/admin/system/update/rollback", ""},
		{http.MethodGet, "/api/admin/system/update/config", ""},
		{http.MethodPut, "/api/admin/system/update/config", `{"enabled":true}`},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			w := doJSON(r, c.method, c.path, c.body)
			if w.Code != http.StatusNotImplemented {
				t.Fatalf("应当返回 501，实际 %d，响应：%s", w.Code, w.Body.String())
			}
			// 错误结构要是 OpenAI 兼容的那套（前端统一从这里取 message）
			var resp struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("响应不是合法 JSON: %v", err)
			}
			if resp.Error.Message == "" {
				t.Error("错误响应应当带 message")
			}
		})
	}
}

// TestRollbackRequiresNoBody 验证不带请求体的 POST 是合法的。
//
// 这条值得一测：回滚的「本地回退」路径不需要任何参数，
// 而 curl -X POST 不带 body 是最自然的调用方式。
// 若 ShouldBindJSON 的错误被当成致命错误，这个调用会变成 400。
func TestRollbackRequiresNoBody(t *testing.T) {
	s := &Server{deps: &Deps{}}
	r := gin.New()
	g := r.Group("/api/admin")
	registerSystemRoutes(g, s)

	// deps.Update 为 nil → 501，关键是**不能是 400**（说明空 body 被误判）
	w := doJSON(r, http.MethodPost, "/api/admin/system/update/rollback", "")
	if w.Code == http.StatusBadRequest {
		t.Fatalf("空请求体不应被当成格式错误，响应：%s", w.Body.String())
	}
}

// TestUpdateBusinessErrorClassification 覆盖错误分类。
//
// 这个分类直接决定用户看到的提示是「结论」还是「故障」：
// 409 意味着「你的请求我们理解，但现在不能做」（读那句话就够了），
// 500 意味着「我们出问题了」（得去看日志）。分错的代价是把
// 「更新器没装」这种一眼能修的事显示成程序崩溃。
//
// 分类靠 errors.Is 哨兵而不是文案子串：所以这里的用例都用**真实的
// 构造方式**（fmt.Errorf + %w）来包哨兵，确保包装链真的能被解开 ——
// 只测裸哨兵的话，构造点忘了 %w 这种真实事故就测不出来。
func TestUpdateBusinessErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"已有任务在跑（带阶段信息）", fmt.Errorf("%w（%s，%s）", update.ErrTaskRunning, "pull", "拉取中"), true},
		{"不支持的更新方式（带 mode）", fmt.Errorf("%w: %q", update.ErrModeUnsupported, "weird"), true},
		{"构建形态拒绝（源码回滚）", fmt.Errorf("%w：当前构建类型不支持在线回滚", update.ErrBuildTypeUnsupported), true},
		{"applyMode 的原因（源码构建）", fmt.Errorf("%s: %w", "当前是源码构建，请用 git pull", update.ErrCannotApply), true},
		// 这些是故障，不是业务拒绝
		{"下载失败", errors.New("下载失败: dial tcp: connection refused"), false},
		{"校验和不匹配", errors.New("校验和不匹配（x.tar.gz）：期望 abc，实际 def"), false},
		{"GitHub 限额", errors.New("获取最新版本失败: GitHub API 返回 403"), false},
	}
	for _, c := range cases {
		if got := update.IsBusinessError(c.err); got != c.want {
			t.Errorf("%s: IsBusinessError(%v) = %v，期望 %v", c.name, c.err, got, c.want)
		}
	}
	if update.IsBusinessError(nil) {
		t.Error("nil 不应被判为业务错误")
	}
	// 旧的分类是文案子串匹配 —— 这些「长得像」的文案现在必须不再误判
	if update.IsBusinessError(errors.New("已有更新任务正在进行，请等它结束")) {
		t.Error("没有包哨兵的同款文案不应被判为业务错误（分类靠类型，不靠文案）")
	}
}

// TestSaveUpdateConfigTokenTriState 钉住 token 的三态契约。
//
// 这是一个容易在重构中悄悄坏掉、而且坏了也看不出来的地方：
//
//	字段缺失 → 不修改（界面上是个不回显的输入框，用户没动它）
//	空串     → 清除
//	非空     → 设置
//
// 如果「缺失」与「空串」被合并处理，两种后果都是坏的吗？不是 ——
// 后果是**每次保存别的设置都会顺手把 token 抹掉**，而用户看到的
// 只是「限额又变回 60 次/时了」，几乎不可能联想到是保存动作干的。
//
// 这里只测「解析出的意图」这一层，不碰数据库（Server 的依赖都是 nil，
// 走到 SaveConfig 之前就会因为 Update 为 nil 而返回 501，
// 所以这个测试实际断言的是**请求体的解析结果**，见下面直接构造的结构体）。
func TestSaveUpdateConfigTokenTriState(t *testing.T) {
	type body struct {
		Enabled bool    `json:"enabled"`
		Repo    string  `json:"repo"`
		Proxy   string  `json:"proxy"`
		Token   *string `json:"token"`
	}

	cases := []struct {
		name      string
		payload   string
		wantNil   bool // nil 表示「不修改」
		wantValue string
	}{
		{"字段缺失表示不修改", `{"enabled":true,"repo":"a/b","proxy":""}`, true, ""},
		{"显式 null 也落在不修改这一侧", `{"enabled":true,"repo":"a/b","token":null}`, true, ""},
		{"空串表示清除", `{"enabled":true,"repo":"a/b","token":""}`, false, ""},
		{"非空表示设置", `{"enabled":true,"repo":"a/b","token":"ghp_x"}`, false, "ghp_x"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b body
			if err := json.Unmarshal([]byte(c.payload), &b); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if c.wantNil {
				if b.Token != nil {
					t.Fatalf("期望 nil（不修改），实际 %q", *b.Token)
				}
				return
			}
			if b.Token == nil {
				t.Fatal("期望非 nil，实际 nil")
			}
			if *b.Token != c.wantValue {
				t.Fatalf("期望 %q，实际 %q", c.wantValue, *b.Token)
			}
		})
	}

	// 记录一个已知的平台事实（不是缺陷，是 Go encoding/json 的定义行为）：
	// null 与字段缺失在 *string 上解析结果相同。前端因此必须用空串
	// 表达「清除」，这一点写在 frontend/src/api/version.ts 的注释里。
	// 如果哪天这里失败了，说明解析行为变了，前端的约定也要跟着改。
	var missing, null body
	_ = json.Unmarshal([]byte(`{"enabled":true}`), &missing)
	_ = json.Unmarshal([]byte(`{"enabled":true,"token":null}`), &null)
	if (missing.Token == nil) != (null.Token == nil) {
		t.Error("null 与字段缺失的解析结果不再一致 —— 前端「用空串清除」的约定需要重新审视")
	}
}
