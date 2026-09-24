package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 500 响应绝不能把原始数据库错误回显给客户端。
//
// 触发这条修复的真实报文（Postgres 唯一索引冲突）：
//
//	ERROR: duplicate key value violates unique constraint "idx_channel_model"
//	DETAIL: Key (channel_id, public_name)=(3, gpt-4o) already exists. (SQLSTATE 23505)
//
// 表名、列名、索引名与**被拒的实际列值**全在里面。管理接口即便有登录鉴权，
// 回显它也等于额外暴露库结构与被拒数据，没有任何收益。
func TestInternalErrorHidesDatabaseDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 造一个与真实 Postgres 报错同形的错误
	dbErr := errors.New(`ERROR: duplicate key value violates unique constraint "idx_channel_model" ` +
		`(SQLSTATE 23505); DETAIL: Key (channel_id, public_name)=(3, gpt-4o) already exists.`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/admin/channels/3/models", nil)

	// 丢掉日志输出，避免测试刷屏；同时留一个缓冲以便断言「详情确实进了日志」
	var logBuf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(prev)

	writeInternalError(c, dbErr)

	body := rec.Body.String()

	// 1. 状态码与错误类型保持不变
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码应为 500，实际 %d", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v（原文 %s）", err, body)
	}
	inner, _ := payload["error"].(map[string]any)
	if inner == nil {
		t.Fatalf("响应缺少 error 对象：%s", body)
	}
	if inner["type"] != "internal_error" {
		t.Errorf("error.type 应为 internal_error，实际 %v", inner["type"])
	}

	// 2. 内部细节一个都不能出现
	leaks := []string{
		"idx_channel_model", // 索引名
		"channel_models",    // 表名
		"channel_id",        // 列名
		"public_name",
		"SQLSTATE",
		"23505",
		"gpt-4o",        // 被拒的**数据**
		"duplicate key", // 原始英文报文
		"unique constraint",
	}
	for _, leak := range leaks {
		if strings.Contains(body, leak) {
			t.Errorf("响应体泄漏了内部信息 %q：%s", leak, body)
		}
	}

	// 3. 但完整错误必须进服务端日志，否则排障无从下手
	logged := logBuf.String()
	if !strings.Contains(logged, "idx_channel_model") {
		t.Errorf("完整错误应写进服务端日志，实际日志：%s", logged)
	}
	if !strings.Contains(logged, "/api/admin/channels/3/models") {
		t.Errorf("日志应带上请求路径便于定位，实际日志：%s", logged)
	}
}

// 回给用户的信息要能指导下一步，而不是一句无用的「服务器错误」。
func TestInternalErrorMessageIsActionable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/admin/proxies/1", nil)

	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))
	defer slog.SetDefault(prev)

	writeInternalError(c, errors.New("connection refused"))

	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	msg := payload.Error.Message
	if msg == "" {
		t.Fatal("message 不能为空")
	}
	if strings.Contains(msg, "connection refused") {
		t.Errorf("不该回显原始错误：%s", msg)
	}
	// 用户需要知道去哪看详情、以及操作有没有生效
	if !strings.Contains(msg, "日志") {
		t.Errorf("提示应引导用户查看服务端日志，实际：%s", msg)
	}
	if !strings.Contains(msg, "未") {
		t.Errorf("提示应说明操作是否已生效（避免用户不确定要不要重试），实际：%s", msg)
	}
}

// writeUpdateError 的分支行为：找不到记录仍是 404，其它才是 500。
func TestWriteUpdateErrorKeeps404ForMissingRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)

	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))
	defer slog.SetDefault(prev)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/admin/keys/99", nil)

	writeUpdateError(c, gorm.ErrRecordNotFound)
	if rec.Code != http.StatusNotFound {
		t.Errorf("记录不存在应为 404，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest(http.MethodPut, "/api/admin/keys/99", nil)
	writeUpdateError(c2, errors.New("ERROR: deadlock detected (SQLSTATE 40P01)"))
	if rec2.Code != http.StatusInternalServerError {
		t.Errorf("其它错误应为 500，实际 %d", rec2.Code)
	}
	if strings.Contains(rec2.Body.String(), "deadlock") {
		t.Errorf("不该回显原始错误：%s", rec2.Body.String())
	}
}
