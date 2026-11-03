package api

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

// noisyRGBA 生成一张不可压缩的图：纯噪声在 PNG 里几乎没有压缩空间，
// 这样 512×512 的素材才会稳稳超过 64KB，测试的不是「刚好塞得下」的边界。
func noisyRGBA(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(12345)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(seed >> 24),
				G: uint8(seed >> 16),
				B: uint8(seed >> 8),
				A: 255,
			})
		}
	}
	return img
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码测试 PNG 失败：%v", err)
	}
	return buf.Bytes()
}

// testICO 手工拼一个 32bpp BMP 的 ICO（与 deepseek.com/favicon.ico 同一形态）。
func testICO(t *testing.T, img *image.RGBA) []byte {
	t.Helper()
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	stride := ((w*32 + 31) / 32) * 4
	pixels := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		srcY := img.Bounds().Min.Y + h - 1 - y // BMP 行序自下而上
		for x := 0; x < w; x++ {
			c := img.RGBAAt(x, srcY)
			o := y*stride + x*4
			pixels[o], pixels[o+1], pixels[o+2], pixels[o+3] = c.B, c.G, c.R, c.A
		}
	}
	dib := make([]byte, 40)
	binary.LittleEndian.PutUint32(dib[0:], 40)
	binary.LittleEndian.PutUint32(dib[4:], uint32(w))
	binary.LittleEndian.PutUint32(dib[8:], uint32(h*2)) // ICO 的 height 含 AND 掩码
	binary.LittleEndian.PutUint16(dib[12:], 1)
	binary.LittleEndian.PutUint16(dib[14:], 32)
	payload := append(dib, pixels...)
	maskStride := ((w + 31) / 32) * 4
	payload = append(payload, make([]byte, maskStride*h)...)

	out := make([]byte, 6+16)
	out[2], out[3] = 1, 0
	binary.LittleEndian.PutUint16(out[4:], 1)
	out[6], out[7] = byte(w), byte(h)
	binary.LittleEndian.PutUint16(out[10:], 1)
	binary.LittleEndian.PutUint16(out[12:], 32)
	binary.LittleEndian.PutUint32(out[14:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(out[18:], 22)
	return append(out, payload...)
}

func TestCompressOversizedPNG(t *testing.T) {
	raw := pngBytes(t, noisyRGBA(512, 512))
	if len(raw) <= maxIconBytes {
		t.Fatalf("测试素材应当超过 %dKB，实际 %d 字节", maxIconBytes>>10, len(raw))
	}
	out, err := compressIcon(raw, "image/png")
	if err != nil {
		t.Fatalf("压缩失败：%v", err)
	}
	if len(out) > maxIconBytes {
		t.Errorf("压缩后仍超过 %dKB：%d 字节", maxIconBytes>>10, len(out))
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("压缩产物不是合法 PNG：%v", err)
	}
	if img.Bounds().Dx() > iconCompressDimension || img.Bounds().Dy() > iconCompressDimension {
		t.Errorf("压缩后尺寸 %v 超过上限 %dpx", img.Bounds().Size(), iconCompressDimension)
	}
}

func TestCompressOversizedICOBMP(t *testing.T) {
	raw := testICO(t, noisyRGBA(256, 256))
	if len(raw) <= maxIconBytes {
		t.Fatalf("测试 ICO 应当超过 %dKB，实际 %d 字节", maxIconBytes>>10, len(raw))
	}
	out, err := compressIcon(raw, "image/x-icon")
	if err != nil {
		t.Fatalf("ICO 压缩失败：%v", err)
	}
	if len(out) > maxIconBytes {
		t.Errorf("压缩后仍超过 %dKB：%d 字节", maxIconBytes>>10, len(out))
	}
	if _, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("压缩产物不是合法 PNG：%v", err)
	}
}

func TestDecodeICOKeepsPixels(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	src.SetRGBA(1, 0, color.RGBA{G: 255, A: 255})
	src.SetRGBA(0, 1, color.RGBA{B: 255, A: 255})
	src.SetRGBA(1, 1, color.RGBA{R: 255, G: 255, A: 255})

	img, err := decodeICO(testICO(t, src))
	if err != nil {
		t.Fatalf("解析 ICO 失败：%v", err)
	}
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
		t.Fatalf("尺寸不对：%v", img.Bounds())
	}
	// ICO 是 BGRA 存储，验证通道没有被写反（红在 0,0，绿在 1,0，蓝在 0,1）
	cases := []struct {
		x, y int
		want color.NRGBA
	}{
		{0, 0, color.NRGBA{R: 255, A: 255}},
		{1, 0, color.NRGBA{G: 255, A: 255}},
		{0, 1, color.NRGBA{B: 255, A: 255}},
	}
	for _, c := range cases {
		got := color.NRGBAModel.Convert(img.At(c.x, c.y)).(color.NRGBA)
		if got != c.want {
			t.Errorf("像素(%d,%d) = %+v，期望 %+v", c.x, c.y, got, c.want)
		}
	}
}

func TestCompressIconDataURI(t *testing.T) {
	small := pngBytes(t, noisyRGBA(16, 16))
	smallURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(small)
	if got, err := compressIconDataURI(smallURI); err != nil || got != smallURI {
		t.Errorf("未超限的 data URI 应原样返回，实际 err=%v changed=%v", err, got != smallURI)
	}

	bigURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes(t, noisyRGBA(512, 512)))
	got, err := compressIconDataURI(bigURI)
	if err != nil {
		t.Fatalf("压缩 data URI 失败：%v", err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("压缩后应为 PNG data URI，实际 %.40s", got)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("产物 base64 解码失败：%v", err)
	}
	if len(raw) > maxIconBytes {
		t.Errorf("压缩后仍超过 %dKB：%d 字节", maxIconBytes>>10, len(raw))
	}
}

func TestReadIconResponseCompressesOversized(t *testing.T) {
	raw := pngBytes(t, noisyRGBA(512, 512))
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"image/png"}},
		Body:       io.NopCloser(bytes.NewReader(raw)),
	}
	got, err := readIconResponse(resp, "https://a.com/big.png")
	if err != nil {
		t.Fatalf("大图应当被压缩而不是拒绝：%v", err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("应返回 PNG data URI，实际 %.40s", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("产物 base64 解码失败：%v", err)
	}
	if len(decoded) > maxIconBytes {
		t.Errorf("压缩后仍超过 %dKB：%d 字节", maxIconBytes>>10, len(decoded))
	}
}
