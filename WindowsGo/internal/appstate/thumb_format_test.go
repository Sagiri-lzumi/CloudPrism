package appstate

import (
	"encoding/base64"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/thumb"
)

// 缩略图解码器注册守卫。
//
// 【它防的是哪一类缺陷】
// pkg/thumb/decode.go 的注释写明支持「PNG/JPEG/GIF/BMP/WebP」，但此前生产
// 代码只 import 了 image/jpeg（直接引用）、x/image/bmp、x/image/webp，
// **漏了 image/png 与 image/gif 的注册**。Go 的 image.Decode 走的是
// 「按内容匹配已注册格式」，未注册即返回 `image: unknown format`。
// 后果：发布出去的 exe 里 PNG（截图最常用格式）与 GIF 缩略图一律生成失败，
// 前端静默回退占位图标——网格视图看起来只是"没有预览图"，不报错、不崩溃。
//
// 【为什么这条守卫不能放在 pkg/thumb 里】
// pkg/thumb 的 thumb_test.go 自己 import 了 image/png 与 image/gif 去**编码**
// 测试夹具；同一目录的测试文件编译进同一个测试二进制，于是那两行 import 顺带
// 把解码器也注册了。结果是 `go test ./pkg/thumb` 永远全绿，而真实二进制是坏的
// —— 测试用自己的 import 掩盖了被测代码的缺失。这是「测试污染被测假设」的
// 典型案例，不是覆盖率问题。
//
// 因此本文件刻意：
//  1. 放在 internal/appstate（持有 ThumbURL 的装配层，测试二进制不 import 任何
//     image/* 子包），断言的是"生产装配面下的真实解码能力"；
//  2. 夹具用 base64 硬编码字节，**绝不** import image/png|gif 现场编码。
//
// 若哪天有人把本文件"顺手搬回 pkg/thumb"，这条守卫会立刻失效而依旧全绿——
// 请勿这么做。
//
// 夹具出自 Go 自身编码器（image/png、gif.Encode 2 色调色板）产出的 1×1 图。
const (
	pngFixture1x1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAAEUlEQVR4nAAEAPv/Av8AAAMAAwkBAvk/Y+MAAAAASUVORK5CYII="
	gifFixture1x1 = "R0lGODlhAQABAIAAAP8AAAAAACH5BAEAAAEALAAAAAABAAEAAAICRAEAOw=="
)

// TestThumbnailDecodesPNGAndGIF 钉住 PNG/GIF 的解码器注册。
func TestThumbnailDecodesPNGAndGIF(t *testing.T) {
	for _, tc := range []struct {
		name string
		b64  string
	}{
		{"PNG", pngFixture1x1},
		{"GIF", gifFixture1x1},
	} {
		raw, err := base64.StdEncoding.DecodeString(tc.b64)
		if err != nil {
			t.Fatalf("%s 夹具 base64 解码失败: %v", tc.name, err)
		}
		if _, err := thumb.MakeThumbnail(raw); err != nil {
			t.Errorf("%s 缩略图生成失败: %v —— 生产代码是否漏注册该格式的解码器（pkg/thumb/decode.go）？",
				tc.name, err)
		}
	}
}
