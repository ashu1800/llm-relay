package api

import "testing"

// 图标类型的字节嗅探。
//
// 为什么要它：一部分静态托管（对象存储、nginx 配错 mime.types）根本不发
// Content-Type，或者发成 application/octet-stream。原先只看 Content-Type
// 会把这类站点的图标全部判死，理由还只有一句「返回的不是图片」。
func TestSniffImageType(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"PNG", []byte("\x89PNG\r\n\x1a\n----"), "image/png"},
		{"JPEG", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}, "image/jpeg"},
		{"GIF87a", []byte("GIF87a----"), "image/gif"},
		{"GIF89a", []byte("GIF89a----"), "image/gif"},
		{"ICO", []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00}, "image/x-icon"},
		{"CUR", []byte{0x00, 0x00, 0x02, 0x00, 0x01, 0x00}, "image/x-icon"},
		{"WebP", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"},
		{"SVG", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), "image/svg+xml"},
		{"SVG 带 XML 声明", []byte(`<?xml version="1.0"?><svg></svg>`), "image/svg+xml"},
		{"HTML 不是图片", []byte(`<!DOCTYPE html><html></html>`), ""},
		{"空内容", []byte{}, ""},
		{"纯文本", []byte("hello world"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sniffImageType(c.data); got != c.want {
				t.Errorf("应为 %q，实际 %q", c.want, got)
			}
		})
	}
}
