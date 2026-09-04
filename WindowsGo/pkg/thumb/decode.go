// Package thumb 提供远程图片的服务端缩略图生成与两级缓存。
//
// 与 WindowsPy/src/cloudprism/core/thumbnail.py 的职责对齐但实现有意不同
// （差异清单见阶段 8 文档）：
//
//   - Python 取密文头部明文后**原样**交给 QImage 渐进解析（可能是不完整的
//     半张图，能否出图由前端判定）；Go 端在服务端把头部数据**解码为完整
//     缩略图**（等比缩放到 192px 宽 + JPEG 重编码），前端拿到的永远是
//     自包含的小 JPEG，无需支持渐进/半图语义。
//   - Python 磁盘缓存落在 %TEMP%/cloudprism_thumbs；Go 端落在
//     data/thumb-cache/（程序数据目录，便于随密库迁移与清理）。
//
// 一致的隐私约束：缩略图明文一律不落盘，磁盘缓存为 AES-GCM 加密形态
// （nonce 12 + tag 16 + 密文，布局与 Python 逐字节兼容，密钥同为会话派生）。
package thumb

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"

	// 注册附加解码格式（标准库 image.Decode 不识别 BMP/WebP）
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// ThumbWidth 缩略图目标宽度（像素）。高度按源图比例等比得出。
const ThumbWidth = 192

// JPEGQuality 重编码质量（q82 对照 Python QImage 保存的质量档）。
const JPEGQuality = 82

// decodeAny 按内容解码任意受支持格式（PNG/JPEG/GIF/BMP/WebP）。
func decodeAny(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// scale192 等比缩放到 ThumbWidth 宽（原图更窄则保持原尺寸，不放大糊图）。
//
// draw.BiLinear 双线性采样是质量与耗时的平衡点（列表小图无需 Lanczos）。
func scale192(src image.Image) image.Image {
	b := src.Bounds()
	if b.Dx() <= ThumbWidth {
		return src
	}
	// 等比缩放：高按宽的比例换算，向下取整到整数
	h := int64(b.Dy()) * ThumbWidth / int64(b.Dx())
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, ThumbWidth, int(h)))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// encodeJPEG 编码为 JPEG 字节：非不透明像素先合成到白底（JPEG 无 alpha
// 通道，透明 PNG 不做合成会得到黑底/花边）。
func encodeJPEG(src image.Image) ([]byte, error) {
	if rgba, ok := src.(*image.RGBA); !ok || !fullyOpaque(rgba) {
		src = ontoWhite(src)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fullyOpaque 判断 RGBA 是否所有像素 alpha=255。
func fullyOpaque(rgba *image.RGBA) bool {
	for i := 3; i < len(rgba.Pix); i += 4 {
		if rgba.Pix[i] != 255 {
			return false
		}
	}
	return true
}

// ontoWhite 把任意图像按 alpha 混合到同尺寸纯白画布上。
func ontoWhite(src image.Image) *image.RGBA {
	b := src.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for x := 0; x < b.Dx(); x++ {
		for y := 0; y < b.Dy(); y++ {
			canvas.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	draw.Draw(canvas, canvas.Bounds(), src, b.Min, draw.Over)
	return canvas
}
