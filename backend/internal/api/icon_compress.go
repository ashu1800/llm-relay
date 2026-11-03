package api

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"strings"
)

// 图标压缩：超过 maxIconBytes 的图片先解码，按比例缩到图标实际显示需要的
// 尺寸再重新编码成 PNG，任何常见位图都能变成 data URI 入库。
//
// 为什么放在服务端：图标是后端抓回来的（渠道图标从上游站点发现、客户端
// 图标从用户填的地址抓），浏览器拿不到跨域原图（canvas 会被污染），
// 也不该为一次保存多开一轮「先取原图再回传」的往返。渠道与客户端共用
// readIconResponse，所以这一份实现同时覆盖两边。
//
// 支持范围：PNG / JPEG / GIF 交给标准库；ICO 由下面手写解析（站点 favicon
// 最常见的就是 ICO，且常是 32bpp BMP —— 2026-11-03 实测
// https://www.deepseek.com/favicon.ico 就是 225×225 的 32bpp BMP，209KB）。
// SVG 不能栅格化（不引入渲染器），超过上限时只能报错。
const (
	// iconCompressDimension 是压缩后的最长边。日志列 18px、设置面板 28px，
	// 128px 已经覆盖 4 倍屏；再大只会把日志列表接口与配置备份撑肥。
	iconCompressDimension = 128
	// iconCompressFloor 是逐步降尺寸的下限。
	iconCompressFloor = 32
)

// compressIcon 把原图压缩到 maxIconBytes 以内，返回 PNG 字节。
//
// 尺寸逐档下降而不是一次定死：128px 的 PNG 对照片类素材可能仍超 64KB，
// 而图标显示只有十几像素 —— 宁肯多试两档，也不为了「一次成功」去调
// PNG 编码参数（质量不可控，透明通道还可能丢）。
func compressIcon(raw []byte, mediaType string) ([]byte, error) {
	img, err := decodeIconImage(raw, mediaType)
	if err != nil {
		return nil, err
	}
	var lastSize int
	for _, dim := range []int{iconCompressDimension, 96, 64, 48, iconCompressFloor} {
		scaled := scaleIconToFit(img, dim)
		var buf bytes.Buffer
		if err := png.Encode(&buf, scaled); err != nil {
			return nil, err
		}
		lastSize = buf.Len()
		if lastSize <= maxIconBytes {
			return buf.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("缩到 %dpx 仍有 %dKB", iconCompressFloor, lastSize>>10)
}

// decodeIconImage 按媒体类型解码；媒体类型未知时交给 image.Decode 嗅探。
func decodeIconImage(raw []byte, mediaType string) (image.Image, error) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/x-icon", "image/vnd.microsoft.icon", "image/ico":
		return decodeICO(raw)
	case "image/svg+xml":
		return nil, errors.New("SVG 图标无法缩放")
	default:
		return decodeRaster(raw)
	}
}

// decodeRaster 用标准库解码位图，并先拦下解压炸弹。
//
// 2MB 的 PNG 可以解出几十 GB 的像素（高压缩比的纯色大图），所以先读
// DecodeConfig 看尺寸、再真正解码；图标压缩只需要小图，超过 1600 万像素
// 的素材直接拒绝。
func decodeRaster(raw []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, errors.New("图片格式无法解码")
	}
	if cfg.Width*cfg.Height > 16_000_000 {
		return nil, fmt.Errorf("图片尺寸过大（%d×%d）", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("图片格式无法解码")
	}
	return img, nil
}

// scaleIconToFit 把图缩放到最长边不超过 maxDim；原图更小时原样返回。
//
// 用面积平均而不是最近邻：图标里常带小字和细线，最近邻缩小时会断线、
// 出锯齿。面积平均按目标像素覆盖的源像素求均值，代价是 O(目标像素数×
// 源像素覆盖数)，图标这个量级（≤2MB 原图）完全够用。
func scaleIconToFit(src image.Image, maxDim int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	dw, dh := w, h
	if w > maxDim || h > maxDim {
		if w >= h {
			dw, dh = maxDim, max(1, h*maxDim/w)
		} else {
			dw, dh = max(1, w*maxDim/h), maxDim
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		sy0 := b.Min.Y + y*h/dh
		sy1 := b.Min.Y + (y+1)*h/dh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < dw; x++ {
			sx0 := b.Min.X + x*w/dw
			sx1 := b.Min.X + (x+1)*w/dw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, bl, a, n uint64
			for sy := sy0; sy < sy1 && sy < b.Max.Y; sy++ {
				for sx := sx0; sx < sx1 && sx < b.Max.X; sx++ {
					// At().RGBA() 返回的是 alpha 预乘的 16 位值；直接平均再
					// 写回 image.RGBA（同样是预乘）即可，透明边缘不会发黑。
					pr, pg, pb, pa := src.At(sx, sy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					bl += uint64(pb)
					a += uint64(pa)
					n++
				}
			}
			if n == 0 {
				continue
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8((r / n) >> 8),
				G: uint8((g / n) >> 8),
				B: uint8((bl / n) >> 8),
				A: uint8((a / n) >> 8),
			})
		}
	}
	return dst
}

// decodeICO 解析 ICO/CUR 容器，取面积最大的那一张。
//
// ICO 里的图像有两种存法：Vista 之后常见的是整段 PNG（直接交给
// png.Decode），更老的是带 BITMAPINFOHEADER 的 BMP（favicon 仍是主流）。
// 两种都要认，否则「200KB 的 favicon.ico」这类最常见的大图标恰好处理不了。
func decodeICO(raw []byte) (image.Image, error) {
	if len(raw) < 6 || raw[0] != 0 || raw[1] != 0 || raw[2] != 1 || raw[3] != 0 {
		return nil, errors.New("不是有效的 ICO 文件")
	}
	count := int(binary.LittleEndian.Uint16(raw[4:6]))
	bestArea, bestOff, bestSize := 0, 0, 0
	for i := 0; i < count; i++ {
		off := 6 + i*16
		if off+16 > len(raw) {
			break
		}
		w, h := int(raw[off]), int(raw[off+1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size := int(binary.LittleEndian.Uint32(raw[off+8 : off+12]))
		imgOff := int(binary.LittleEndian.Uint32(raw[off+12 : off+16]))
		if size <= 0 || imgOff < 0 || imgOff+size > len(raw) {
			continue
		}
		if w*h > bestArea {
			bestArea, bestOff, bestSize = w*h, imgOff, size
		}
	}
	if bestArea == 0 {
		return nil, errors.New("ICO 里没有可用的图像")
	}
	payload := raw[bestOff : bestOff+bestSize]
	if len(payload) >= 8 && string(payload[:8]) == "\x89PNG\r\n\x1a\n" {
		return decodeRaster(payload)
	}
	return decodeICODIB(payload)
}

// decodeICODIB 解析未压缩的 BMP 图像（32bpp / 24bpp）。
//
// 只做图标实际会遇到的两种位深：8bpp 及以下的调色板图标本来就小，根本
// 走不到压缩这一步；压缩包里出现它们只会是特意构造的输入，直接报错更安全。
func decodeICODIB(payload []byte) (image.Image, error) {
	if len(payload) < 40 {
		return nil, errors.New("ICO 位图头不完整")
	}
	headerSize := int(binary.LittleEndian.Uint32(payload[0:4]))
	w := int(int32(binary.LittleEndian.Uint32(payload[4:8])))
	h := int(int32(binary.LittleEndian.Uint32(payload[8:12]))) / 2 // ICO 的 height 含 AND 掩码，是实际高度的两倍
	bpp := int(binary.LittleEndian.Uint16(payload[14:16]))
	compression := binary.LittleEndian.Uint32(payload[16:20])
	if headerSize < 40 || headerSize > len(payload) {
		return nil, errors.New("ICO 位图头不完整")
	}
	if compression != 0 {
		return nil, errors.New("只支持未压缩的 ICO 位图")
	}
	if w <= 0 || h <= 0 || w > 1024 || h > 1024 {
		return nil, errors.New("ICO 尺寸超出范围")
	}
	if bpp != 32 && bpp != 24 {
		return nil, fmt.Errorf("不支持 %d 位的 ICO 位图", bpp)
	}
	stride := ((w*bpp + 31) / 32) * 4
	need := headerSize + stride*h
	if need > len(payload) {
		return nil, errors.New("ICO 位图数据不完整")
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	for y := 0; y < h; y++ {
		row := payload[headerSize+(h-1-y)*stride:] // BMP 行序自下而上
		for x := 0; x < w; x++ {
			o := x * (bpp / 8)
			a := uint8(255)
			if bpp == 32 {
				a = row[o+3]
				if a != 0 {
					anyAlpha = true
				}
			}
			out.Set(x, y, color.NRGBA{R: row[o+2], G: row[o+1], B: row[o], A: a})
		}
	}
	// 一部分 32bpp ICO 的 alpha 全为 0（Windows 按 AND 掩码处理）。这种
	// 情况按不透明渲染，否则整个图标会变成一片空白 —— 比忽略掩码更接近
	// 浏览器里的实际效果。
	if bpp == 32 && !anyAlpha {
		for y := 0; y < h; y++ {
			row := payload[headerSize+(h-1-y)*stride:]
			for x := 0; x < w; x++ {
				o := x * 4
				out.Set(x, y, color.NRGBA{R: row[o+2], G: row[o+1], B: row[o], A: 255})
			}
		}
	}
	return out, nil
}

// compressIconDataURI 归一化用户粘贴的 data URI：超过 maxIconBytes 就压缩。
//
// 小于上限的图原样返回（保留原格式与原始字节），避免每次保存都重编码一遍
// 变成「看起来没变但哈希变了」；只有确实超限的才动。
func compressIconDataURI(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return "", errors.New("不是 data URI")
	}
	comma := strings.Index(raw, ",")
	if comma < 0 {
		return "", errors.New("data URI 格式不正确")
	}
	meta := raw[len("data:"):comma]
	if !strings.Contains(strings.ToLower(meta), "base64") {
		return "", errors.New("只支持 base64 编码的图标")
	}
	payload := strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\t', ' ':
			return -1
		}
		return r
	}, raw[comma+1:])
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", errors.New("data URI 的 base64 解码失败")
	}
	if len(data) <= maxIconBytes {
		return raw, nil
	}
	mediaType := strings.TrimSpace(strings.SplitN(meta, ";", 2)[0])
	compressed, err := compressIcon(data, mediaType)
	if err != nil {
		return "", fmt.Errorf("图标过大且无法压缩：%v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(compressed), nil
}

// normalizePastedIcon 处理用户直接填进图标框的内容：data URI 超限时压缩，
// 其余形态（图片外链 / 内置文件名 / 一两个字符）原样返回。
func normalizePastedIcon(icon string) (string, error) {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(icon)), "data:") {
		return compressIconDataURI(icon)
	}
	return icon, nil
}
