package thumb

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/pipeline"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"

	"golang.org/x/image/bmp"
)

func newSession(t *testing.T) *session.Session {
	t.Helper()
	s := session.New("thumb-test-pw")
	t.Cleanup(s.Close)
	return s
}

// encodePNG 生成 x*y 尺寸的测试图（横向渐变，RGB 非均匀便于断言）。
func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x * 255 / w),
				G: uint8(y * 255 / h),
				B: 128,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// decodeJPEGBounds 解码 JPEG 并返回尺寸。
func decodeJPEGBounds(t *testing.T, data []byte) (int, int) {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("输出不是合法 JPEG: %v", err)
	}
	b := img.Bounds()
	return b.Dx(), b.Dy()
}

// ---------------------------------------------------------------------------
// MakeThumbnail：解码 → 缩放 → 重编码
// ---------------------------------------------------------------------------

// TestMakeThumbnailScalesDown 大图等比缩到 192px 宽。
func TestMakeThumbnailScalesDown(t *testing.T) {
	out, err := MakeThumbnail(encodePNG(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	w, h := decodeJPEGBounds(t, out)
	if w != 192 || h != 144 { // 300*192/400 = 144
		t.Errorf("缩略图尺寸 %dx%d，期望 192x144", w, h)
	}
}

// TestMakeThumbnailKeepsSmall 小图不放大（保持原尺寸）。
func TestMakeThumbnailKeepsSmall(t *testing.T) {
	out, err := MakeThumbnail(encodePNG(t, 100, 50))
	if err != nil {
		t.Fatal(err)
	}
	w, h := decodeJPEGBounds(t, out)
	if w != 100 || h != 50 {
		t.Errorf("小图应保持 100x50，实得 %dx%d", w, h)
	}
}

// TestMakeThumbnailTransparent 半透明像素合成到白底（JPEG 无 alpha）。
func TestMakeThumbnailTransparent(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			img.SetRGBA(x, y, color.RGBA{255, 0, 0, 128}) // 半透明红
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	out, err := MakeThumbnail(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	// 半透明红叠白底 = 偏粉；与「黑底」的 (127,0,0) 有显著差异
	r, g, b, _ := decoded.At(32, 32).RGBA()
	if r>>8 < 200 || g>>8 < 100 {
		t.Errorf("半透明像素应叠白底（粉），实得 RGB=(%d,%d,%d)", r>>8, g>>8, b>>8)
	}
}

// TestMakeThumbnailBMPAndGIF 附加格式可解码（BMP 现场构造；GIF 标准库）。
func TestMakeThumbnailBMPAndGIF(t *testing.T) {
	// BMP：x/image/bmp 有 Encode，可现场构造
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for x := 0; x < 300; x++ {
		for y := 0; y < 200; y++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var bmpBuf bytes.Buffer
	if err := bmp.Encode(&bmpBuf, img); err != nil {
		t.Fatal(err)
	}
	out, err := MakeThumbnail(bmpBuf.Bytes())
	if err != nil {
		t.Fatalf("BMP 解码失败: %v", err)
	}
	if w, h := decodeJPEGBounds(t, out); w != 192 || h != 128 {
		t.Errorf("BMP 缩略图尺寸 %dx%d，期望 192x128", w, h)
	}

	// GIF：8 色小调色板图
	pal := color.Palette{color.White, color.Black}
	gifImg := image.NewPaletted(image.Rect(0, 0, 256, 128), pal)
	for x := 0; x < 256; x++ {
		for y := 0; y < 128; y++ {
			gifImg.SetColorIndex(x, y, uint8((x/64+y/64)%2))
		}
	}
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, gifImg, nil); err != nil {
		t.Fatal(err)
	}
	out, err = MakeThumbnail(gifBuf.Bytes())
	if err != nil {
		t.Fatalf("GIF 解码失败: %v", err)
	}
	if w, h := decodeJPEGBounds(t, out); w != 192 || h != 96 {
		t.Errorf("GIF 缩略图尺寸 %dx%d，期望 192x96", w, h)
	}
}

// TestMakeThumbnailTruncatedData 截断的图数据解码失败（回退占位图标语义）。
func TestMakeThumbnailTruncatedData(t *testing.T) {
	full := encodePNG(t, 200, 200)
	if _, err := MakeThumbnail(full[:len(full)/3]); err == nil {
		t.Error("截断数据应解码失败")
	}
}

// ---------------------------------------------------------------------------
// Cache：内存 LRU + 加密磁盘
// ---------------------------------------------------------------------------

// TestCacheRoundTrip 两级缓存读写：磁盘文件不得含明文特征。
func TestCacheRoundTrip(t *testing.T) {
	sess := newSession(t)
	dir := t.TempDir()
	c := NewCache(sess, dir)
	data := encodePNG(t, 64, 64)

	c.Put("vault/pics/photo.png", data)
	if got := c.Get("vault/pics/photo.png"); !bytes.Equal(got, data) {
		t.Error("内存命中数据不一致")
	}

	// 磁盘内容必须是密文：直接搜索 PNG 签名应无命中
	blob, err := os.ReadFile(filepath.Join(dir, KeyFor("vault/pics/photo.png")+cacheExt))
	if err != nil {
		t.Fatalf("磁盘缓存文件缺失: %v", err)
	}
	if bytes.Contains(blob, []byte("\x89PNG")) {
		t.Error("磁盘缓存含明文 PNG 签名（加密失效）")
	}

	// 新实例（同会话同目录）应能解密磁盘缓存
	c2 := NewCache(sess, dir)
	if got := c2.Get("vault/pics/photo.png"); !bytes.Equal(got, data) {
		t.Error("磁盘缓存解密数据不一致")
	}

	// 不同会话（不同主密码）无法解密 → nil（密钥不匹配即损坏）
	other := session.New("thumb-other-pw")
	t.Cleanup(other.Close)
	c3 := NewCache(other, dir)
	if got := c3.Get("vault/pics/photo.png"); got != nil {
		t.Error("异会话密钥应无法解密磁盘缓存")
	}
}

// TestCacheCorruption 磁盘缓存被篡改 → 静默 miss 而非 panic/错误。
func TestCacheCorruption(t *testing.T) {
	sess := newSession(t)
	dir := t.TempDir()
	c := NewCache(sess, dir)
	data := []byte("thumb-bytes")
	c.Put("a.png", data)

	// 篡改密文区（避开 nonce/tag 截断条件，直接改正文）
	path := filepath.Join(dir, KeyFor("a.png")+cacheExt)
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 0xFF
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := NewCache(sess, dir).Get("a.png"); got != nil {
		t.Error("篡改的缓存应 miss")
	}
}

// TestCacheLRUEviction 内存 LRU 超限驱逐最旧条目。
func TestCacheLRUEviction(t *testing.T) {
	sess := newSession(t)
	c := NewCache(sess, t.TempDir())
	for i := 0; i < defaultMaxMem+10; i++ {
		c.Put(fmt.Sprintf("file-%03d.png", i), []byte{byte(i)})
	}
	c.mu.Lock()
	n := len(c.mem)
	c.mu.Unlock()
	if n != defaultMaxMem {
		t.Errorf("内存缓存应裁剪到 %d 条，实得 %d", defaultMaxMem, n)
	}
	// 最旧的 10 条应已被驱逐；保留的是后写入的（直接查内存表——Get 会
	// 经磁盘回填旧条目，那正是两级缓存的设计行为）
	c.mu.Lock()
	_, oldEvicted := c.mem[KeyFor("file-000.png")]
	_, newestHit := c.mem[KeyFor(fmt.Sprintf("file-%03d.png", defaultMaxMem+9))]
	c.mu.Unlock()
	if oldEvicted {
		t.Error("最旧条目应已被驱逐")
	}
	if !newestHit {
		t.Error("最新条目应仍命中")
	}
}

// ---------------------------------------------------------------------------
// Fetch：端到端（加密容器 → 解密头部 → 服务端缩放）
// ---------------------------------------------------------------------------

// TestFetchEndToEnd 远程加密图片生成缩略图，且缓存命中后不再触达后端。
func TestFetchEndToEnd(t *testing.T) {
	sess := newSession(t)
	root := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}

	// 把 400x300 PNG 加密上传到本地后端
	src := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(src, encodePNG(t, 400, 300), 0o644); err != nil {
		t.Fatal(err)
	}
	dst, err := os.CreateTemp(root, "photo_*.cpenc")
	if err != nil {
		t.Fatal(err)
	}
	container := dst.Name()
	enc := &pipeline.Encryptor{Sess: sess, Workers: 2}
	if _, err := enc.EncryptFile(context.Background(), src, dst, nil); err != nil {
		t.Fatal(err)
	}
	dst.Close()
	remote := filepath.Base(container)

	dir := filepath.Join(t.TempDir(), "thumbs")
	cache := NewCache(sess, dir)
	ctx := context.Background()

	thumb, err := Fetch(ctx, sess, b, remote, cache)
	if err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	if w, h := decodeJPEGBounds(t, thumb); w != 192 || h != 144 {
		t.Errorf("缩略图尺寸 %dx%d，期望 192x144", w, h)
	}

	// 缓存命中路径：删掉后端容器后仍应返回（证明未触达后端）
	if err := os.Remove(container); err != nil {
		t.Fatal(err)
	}
	thumb2, err := Fetch(ctx, sess, b, remote, cache)
	if err != nil || !bytes.Equal(thumb2, thumb) {
		t.Errorf("缓存命中应成功且字节一致：err=%v", err)
	}

	// 无缓存实例时后端缺失应失败（区分缓存与真实拉取路径）
	if _, err := Fetch(ctx, sess, b, remote, nil); err == nil {
		t.Error("后端缺失且无缓存时应报错")
	}
}

// TestFetchHugeFileFallsBack 明文 > MaxWholeBytes 的图取头部试解，失败即
// 回退（占位图标语义；对齐 Python 大图数据不足返回 None）。
func TestFetchHugeFileFallsBack(t *testing.T) {
	sess := newSession(t)
	root := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}

	// BMP 无压缩：3000x2000x4B = 24MB 明文（远大于 MaxWholeBytes），且
	// 头部截断必解码失败（BMP 像素数据不在头部）
	img := image.NewRGBA(image.Rect(0, 0, 3000, 2000))
	seed := byte(7)
	for y := 0; y < 2000; y++ {
		for x := 0; x < 3000; x++ {
			seed = seed*31 + 17
			img.SetRGBA(x, y, color.RGBA{seed, seed * 3, seed * 5, 255})
		}
	}
	src := filepath.Join(t.TempDir(), "huge.bmp")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := bmp.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if st, _ := os.Stat(src); st.Size() <= MaxWholeBytes {
		t.Skip("测试前提不满足：BMP 明文应大于整取阈值")
	}

	cf, err := os.Create(filepath.Join(root, "huge.cpenc"))
	if err != nil {
		t.Fatal(err)
	}
	enc := &pipeline.Encryptor{Sess: sess, Workers: 2}
	if _, err := enc.EncryptFile(context.Background(), src, cf, nil); err != nil {
		t.Fatal(err)
	}
	cf.Close()

	if _, err := Fetch(context.Background(), sess, b, "huge.cpenc", nil); err == nil {
		t.Error("超大非渐进图头部截断应解码失败（回退占位）")
	}
}
