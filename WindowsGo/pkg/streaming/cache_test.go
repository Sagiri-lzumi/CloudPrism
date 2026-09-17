package streaming

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cache"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// 本文件是「大文件分块读取」的端到端验收：证明视频预览的读取路径真的
// 按块落到本地缓存，并且重看时不再产生任何远端下载。
//
// 断言三条用户可见的行为：
//  1. 大于阈值的文件切成「原名-1 / 原名-2 …」并收进以原名命名的子文件夹；
//  2. 首次播放解密结果与无缓存时逐字节一致（缓存不能改变明文）；
//  3. 第二次播放远端下载次数**零增长**（读穿命中）。

// countingBackend 统计 DownloadRange 的调用次数与字节量。
type countingBackend struct {
	storage.Backend

	mu    sync.Mutex
	calls int
	bytes int64
}

func (c *countingBackend) DownloadRange(ctx context.Context, path string, start, end int64) ([]byte, error) {
	c.mu.Lock()
	c.calls++
	c.bytes += end - start + 1
	c.mu.Unlock()
	return c.Backend.DownloadRange(ctx, path, start, end)
}

func (c *countingBackend) stats() (int, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.bytes
}

// TestChunkCacheReadThrough 覆盖「首播落盘分块 + 重播零下载」全链路。
func TestChunkCacheReadThrough(t *testing.T) {
	root := t.TempDir()
	backend, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New(testPW)
	// 5000*256 = 1,280,000 字节明文，密文略大于 1MiB → 恰好切成 2 块
	plain := testPlain(5000)
	uploadPlain(t, sess, backend, plain, "media/video.cpenc")

	cb := &countingBackend{Backend: backend}
	cacheRoot := t.TempDir()
	cc, err := cache.Open(cache.Options{Root: cacheRoot, Scope: "s1", ChunkMB: 1, LimitMB: 64})
	if err != nil {
		t.Fatalf("打开缓存失败: %v", err)
	}
	defer cc.Close() //nolint:errcheck // 测试收尾

	srv := NewServer(sess, cb)
	srv.SetChunkCache(cc) // 必须在注册前注入：注册阶段要补写密文头部
	entry, err := srv.RegisterStream(context.Background(), "media/video.cpenc", "视频 测试.mp4")
	if err != nil {
		t.Fatalf("注册流端点失败: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// ---- 首轮播放：走远端并顺带把密文写进分块缓存 ----
	status, body, _ := get(t, ts.URL+entry.URLPath(), map[string]string{"Range": "bytes=0-"})
	if status != http.StatusPartialContent {
		t.Fatalf("首轮状态码应为 206，实得 %d", status)
	}
	if !bytes.Equal(body, plain) {
		t.Fatal("首轮解密结果与原始明文不一致（缓存污染了读取路径）")
	}
	callsAfterFirst, bytesAfterFirst := cb.stats()
	if callsAfterFirst <= 1 {
		t.Fatalf("首轮应产生远端范围下载，实得调用数 %d", callsAfterFirst)
	}
	// 下载量不应显著超过密文自身（防「为了填块而超额下载」回归）
	if maxWant := int64(len(plain)) + 64*1024; bytesAfterFirst > maxWant {
		t.Fatalf("首轮下载 %d 字节，明显超出文件规模 %d（疑似整块/整文件放大下载）",
			bytesAfterFirst, maxWant)
	}

	// ---- 磁盘布局：原名子文件夹 + 原名-N，且补齐的块不带 .part ----
	sub := filepath.Join(cacheRoot, "media", "s1", "视频 测试.mp4")
	fi, err := os.Stat(sub)
	if err != nil || !fi.IsDir() {
		t.Fatalf("期望出现以原名命名的子文件夹 %s，实得 err=%v", sub, err)
	}
	for _, name := range []string{"视频 测试.mp4-1", "视频 测试.mp4-2"} {
		p := filepath.Join(sub, name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("期望块文件 %s 已补齐转正：%v", p, err)
		}
		if _, err := os.Stat(p + ".part"); !os.IsNotExist(err) {
			t.Fatalf("补齐后的块不应残留 .part：%s", p)
		}
	}
	// 块文件不得散落在缓存根目录（需求：避免根目录文件过多）
	if ents, err := os.ReadDir(filepath.Join(cacheRoot, "media", "s1")); err == nil {
		for _, ent := range ents {
			if ent.Name() != ".meta" && !ent.IsDir() {
				t.Fatalf("缓存根目录不应出现散落文件：%s", ent.Name())
			}
		}
	}

	// ---- 重看：完全读穿命中，远端调用数不得增长 ----
	status2, body2, _ := get(t, ts.URL+entry.URLPath(), map[string]string{"Range": "bytes=0-"})
	if status2 != http.StatusPartialContent {
		t.Fatalf("重看状态码应为 206，实得 %d", status2)
	}
	if !bytes.Equal(body2, plain) {
		t.Fatal("重看解密结果与原始明文不一致")
	}
	if callsAfterSecond, _ := cb.stats(); callsAfterSecond != callsAfterFirst {
		t.Fatalf("重看应全部命中本地缓存：远端调用数由 %d 变为 %d",
			callsAfterFirst, callsAfterSecond)
	}

	// ---- 分段回拖：跨块区间同样零下载，且内容正确 ----
	// 明文 1040000 对应的密文偏移已越过 1MiB 分块边界，是真正的跨块读取
	callsBeforeSeek, _ := cb.stats()
	status3, body3, _ := get(t, ts.URL+entry.URLPath(), map[string]string{"Range": "bytes=1040000-1055000"})
	if status3 != http.StatusPartialContent {
		t.Fatalf("回拖状态码应为 206，实得 %d", status3)
	}
	want3 := plain[1040000:1055001]
	if !bytes.Equal(body3, want3) {
		t.Fatal("跨块回拖的明文不一致")
	}
	if callsAfterSeek, _ := cb.stats(); callsAfterSeek != callsBeforeSeek {
		t.Fatalf("回拖已缓存区间不应产生远端下载：%d → %d", callsBeforeSeek, callsAfterSeek)
	}
}

// TestChunkCacheDisabledStillServes 未注入缓存时代理必须照常工作（降级语义）。
func TestChunkCacheDisabledStillServes(t *testing.T) {
	root := t.TempDir()
	backend, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New(testPW)
	plain := testPlain(40)
	uploadPlain(t, sess, backend, plain, "media/small.cpenc")

	srv := NewServer(sess, backend) // 不注入缓存
	entry, err := srv.RegisterStream(context.Background(), "media/small.cpenc", "small.bin")
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	status, body, _ := get(t, ts.URL+entry.URLPath(), map[string]string{"Range": "bytes=0-"})
	if status != http.StatusPartialContent || !bytes.Equal(body, plain) {
		t.Fatalf("无缓存时代理应照常返回明文（status=%d）", status)
	}
}
