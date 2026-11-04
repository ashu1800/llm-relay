package relay

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
)

// buildMultipart 构造一个带 model 字段与文件部件的 multipart 报文，
// 返回 (body, contentType)。语音转写请求的形状（form 字段 + 音频文件）
// 由它模拟；文件内容带特征串，改写后必须原样保留。
func buildMultipart(t *testing.T, model string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("purpose", "transcribe"); err != nil {
		t.Fatal(err)
	}
	if model != "" {
		if err := w.WriteField("model", model); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := w.CreateFormFile("file", "audio.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("FAKE-MP3-BYTES-0123456789")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// parseModelAndFile 解析改写后的报文，返回 (model 字段值, 文件内容, 出错)。
func parseModelAndFile(t *testing.T, body []byte, contentType string) (string, string) {
	t.Helper()
	// 用标准库的 FormFile 解析（生产读取走 multipartFormValue，
	// 这里用另一条路径交叉验证编码结果没坏）
	req := httptest.NewRequest("POST", "/v1/audio/transcriptions", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		t.Fatalf("改写后的报文不是合法 multipart: %v", err)
	}
	file, header, err := req.FormFile("file")
	if err != nil {
		t.Fatalf("文件部件丢失: %v", err)
	}
	defer file.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(file); err != nil {
		t.Fatal(err)
	}
	if header.Filename != "audio.mp3" {
		t.Errorf("文件名漂移: %s", header.Filename)
	}
	return req.MultipartForm.Value["model"][0], out.String()
}

func TestMultipartModelField(t *testing.T) {
	body, ct := buildMultipart(t, "whisper-1")
	if got := MultipartModelField(body, ct); got != "whisper-1" {
		t.Errorf("MultipartModelField = %q，期望 whisper-1", got)
	}

	body2, ct2 := buildMultipart(t, "")
	if got := MultipartModelField(body2, ct2); got != "" {
		t.Errorf("缺 model 字段应返回空串，得到 %q", got)
	}

	if got := MultipartModelField([]byte("not multipart"), "application/json"); got != "" {
		t.Errorf("非 multipart 报文应返回空串而不是报错，得到 %q", got)
	}
}

func TestRewriteMultipartModel(t *testing.T) {
	// 同名直通：字节与 Content-Type 都不动 —— 重编码会把整份文件复制一遍，
	// 对绝大多数部署（对外名 = 上游名）是纯浪费
	body, ct := buildMultipart(t, "whisper-1")
	out, outCT, err := RewriteMultipartModel(body, ct, "whisper-1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, body) || outCT != ct {
		t.Error("同名改写不应产生任何变化")
	}

	// 改名：model 被替换、文件内容原样保留、新 Content-Type 带新 boundary
	out2, outCT2, err := RewriteMultipartModel(body, ct, "whisper-large-v3")
	if err != nil {
		t.Fatal(err)
	}
	if outCT2 == ct {
		t.Error("重编码后的 Content-Type 应带新 boundary")
	}
	model, fileContent := parseModelAndFile(t, out2, outCT2)
	if model != "whisper-large-v3" {
		t.Errorf("改写后的 model 字段 = %q", model)
	}
	if !strings.Contains(fileContent, "FAKE-MP3-BYTES") {
		t.Errorf("文件内容损坏: %q", fileContent)
	}
	// purpose 字段也要活着 —— 只替换 model，其余部件原样
	if got := MultipartModelField(out2, outCT2); got != "" {
		// MultipartModelField 只认 model 字段，这里换个方式验证 purpose
		_ = got
	}
	v, found, err := multipartFormValue(out2, outCT2, "purpose")
	if err != nil || !found || v != "transcribe" {
		t.Errorf("其余 form 字段应原样保留: v=%q found=%v err=%v", v, found, err)
	}

	// 没有 model 字段：原样返回（交给上游报缺参数）
	body3, ct3 := buildMultipart(t, "")
	out3, outCT3, err := RewriteMultipartModel(body3, ct3, "whisper-1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out3, body3) || outCT3 != ct3 {
		t.Error("无 model 字段时不应重编码")
	}
}
