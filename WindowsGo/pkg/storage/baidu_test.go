package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// defaultCred 返回配好假服务器 token 状态的凭证（mock 初始 access-1/refresh-1）。
func defaultCred() BaiduCredData {
	return BaiduCredData{
		AppID:        "dev-1",
		AppKey:       "appkey-1",
		SecretKey:    "secret-1",
		SignKey:      "sign-1",
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
	}
}

// patternBytes 生成确定性伪随机字节（i*31+7 循环，避免大文件内存开销）。
func patternBytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*31 + 7)
	}
	return out
}

func TestBaiduListDir(t *testing.T) {
	b, m := newBaiduPair(t, defaultCred())
	ctx := context.Background()

	t.Run("空根与混合条目", func(t *testing.T) {
		m.mu.Lock()
		m.addFile("/a.cpenc", []byte("aaa"))
		m.addFile("/dir1/b.cpenc", []byte("bbbb"))
		m.addDir("/dir1")
		m.mu.Unlock()

		// 根：直接子项 a.cpenc 与 dir1（dir1 里的 b.cpenc 不该出现）
		entries, err := b.ListDir(ctx, "/")
		if err != nil {
			t.Fatalf("ListDir(/) 失败: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("根应有 2 条（a.cpenc+dir1），实得 %v", entries)
		}
		byName := map[string]Entry{}
		for _, e := range entries {
			byName[e.Name] = e
		}
		if e := byName["a.cpenc"]; e.IsDir || e.Size != 3 {
			t.Errorf("a.cpenc 应为 3 字节文件，实得 %+v", e)
		}
		if e := byName["dir1"]; !e.IsDir || e.Size != 0 {
			t.Errorf("dir1 应为目录，实得 %+v", e)
		}
	})

	t.Run("子目录列表", func(t *testing.T) {
		entries, err := b.ListDir(ctx, "/dir1")
		if err != nil {
			t.Fatalf("ListDir(/dir1) 失败: %v", err)
		}
		if len(entries) != 1 || entries[0].Name != "b.cpenc" || entries[0].Size != 4 {
			t.Errorf("dir1 应只有 b.cpenc(4B)，实得 %v", entries)
		}
	})

	t.Run("中文目录与文件名", func(t *testing.T) {
		m.mu.Lock()
		m.addDir("/中文目录")
		m.addFile("/中文目录/照片.cpenc", []byte("照片数据"))
		m.mu.Unlock()
		entries, err := b.ListDir(ctx, "/中文目录")
		if err != nil {
			t.Fatalf("中文目录列表失败: %v", err)
		}
		if len(entries) != 1 || entries[0].Name != "照片.cpenc" {
			t.Errorf("中文目录内容不符: %v", entries)
		}
	})

	t.Run("目录不存在回后端错误", func(t *testing.T) {
		_, err := b.ListDir(ctx, "/不存在的目录")
		if err == nil {
			t.Fatal("不存在目录应报错")
		}
		if !errors.Is(err, ErrBackend) {
			t.Errorf("应归 ErrBackend（errno=-9 是百度对不存在目录的表达），实得 %v", err)
		}
		var apiErr *baiduAPIError
		if !errors.As(err, &apiErr) || apiErr.Errno != -9 {
			t.Errorf("应携带 errno=-9，实得 %v", err)
		}
	})
}

func TestBaiduGetSizeHeadExists(t *testing.T) {
	b, m := newBaiduPair(t, defaultCred())
	ctx := context.Background()
	m.mu.Lock()
	m.addFile("/big.cpenc", patternBytes(100))
	m.mu.Unlock()

	t.Run("GetSize 与缓存去重", func(t *testing.T) {
		size, err := b.GetSize(ctx, "/big.cpenc")
		if err != nil || size != 100 {
			t.Fatalf("GetSize 失败: size=%d err=%v", size, err)
		}
		if m.listHits != 1 {
			t.Fatalf("首次 GetSize 应列父目录一次，实得 %d", m.listHits)
		}
		// 第二次应命中 entry 缓存，不再列父目录
		if size, err = b.GetSize(ctx, "/big.cpenc"); err != nil || size != 100 {
			t.Fatalf("第二次 GetSize 失败: %v", err)
		}
		if m.listHits != 1 {
			t.Errorf("缓存生效后不应再列父目录，实得 %d", m.listHits)
		}
	})

	t.Run("Head 与 GetSize 一致", func(t *testing.T) {
		n, err := b.Head(ctx, "/big.cpenc")
		if err != nil || n != 100 {
			t.Errorf("Head 应返回 100，实得 %d err=%v", n, err)
		}
	})

	t.Run("Exists 三态", func(t *testing.T) {
		ok, err := b.Exists(ctx, "/big.cpenc")
		if err != nil || !ok {
			t.Fatalf("存在的文件应 true，实得 ok=%v err=%v", ok, err)
		}
		// 父目录存在但目标不在：list 成功、entry miss → false
		ok, err = b.Exists(ctx, "/missing.cpenc")
		if err != nil || ok {
			t.Errorf("不存在文件应 false 且无错误，实得 ok=%v err=%v", ok, err)
		}
		// 父目录本身不存在：list 回 errno -9 → 折叠为 false
		ok, err = b.Exists(ctx, "/无父目录/x.cpenc")
		if err != nil || ok {
			t.Errorf("父目录不存在应 false 且无错误，实得 ok=%v err=%v", ok, err)
		}
	})

	t.Run("Exists 目录", func(t *testing.T) {
		m.mu.Lock()
		m.addDir("/onlydir")
		m.mu.Unlock()
		ok, err := b.Exists(ctx, "/onlydir")
		if err != nil || !ok {
			t.Errorf("目录应 true，实得 ok=%v err=%v", ok, err)
		}
	})
}

func TestBaiduDownloadRange(t *testing.T) {
	ctx := context.Background()

	t.Run("206 分片下载与缓存去重", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		payload := patternBytes(1000)
		m.mu.Lock()
		m.addFile("/movie.cpenc", payload)
		m.mu.Unlock()

		got, err := b.DownloadRange(ctx, "/movie.cpenc", 100, 299)
		if err != nil {
			t.Fatalf("DownloadRange 失败: %v", err)
		}
		if !bytes.Equal(got, payload[100:300]) {
			t.Fatal("206 返回字节应与本地切片一致")
		}
		// 首次下载: 列父目录 + filemetas 各一次
		if m.listHits != 1 || m.metasHits != 1 {
			t.Fatalf("首次下载应 list×1+filemetas×1，实得 list=%d metas=%d", m.listHits, m.metasHits)
		}
		// 第二次下载：entry 与 dlink 双命中 → 0 次 API（Python 端此处必付两次）
		if _, err := b.DownloadRange(ctx, "/movie.cpenc", 0, 49); err != nil {
			t.Fatalf("第二次下载失败: %v", err)
		}
		if m.listHits != 1 || m.metasHits != 1 {
			t.Errorf("缓存命中后不应再调 API，实得 list=%d metas=%d", m.listHits, m.metasHits)
		}
	})

	t.Run("200 整文件回退切片", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		payload := patternBytes(5000)
		m.mu.Lock()
		m.addFile("/norange.cpenc", payload)
		m.failRange = true // dlink 忽略 Range 回整文件
		m.mu.Unlock()

		got, err := b.DownloadRange(ctx, "/norange.cpenc", 2000, 2999)
		if err != nil {
			t.Fatalf("200 回退下载失败: %v", err)
		}
		if !bytes.Equal(got, payload[2000:3000]) {
			t.Fatal("200 回退应本地切出 [start,end]")
		}
	})

	t.Run("200 回退整文件不足报错", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addFile("/short.cpenc", patternBytes(50))
		m.failRange = true
		m.mu.Unlock()
		// 请求 200-499（end 越界）；整文件回退只有 50 字节 → 必须报错，
		// 否则按 [start,end] 切片错位返回给解密层
		_, err := b.DownloadRange(ctx, "/short.cpenc", 200, 499)
		if err == nil {
			t.Fatal("整文件不足必须报错（防止错位解密）")
		}
		if !errors.Is(err, ErrBackend) {
			t.Errorf("应归 ErrBackend，实得 %v", err)
		}
	})

	t.Run("非法范围", func(t *testing.T) {
		b, _ := newBaiduPair(t, defaultCred())
		_, err := b.DownloadRange(ctx, "/x", 10, 5)
		if !errors.Is(err, ErrRange) {
			t.Errorf("start>end 应归 ErrRange，实得 %v", err)
		}
	})

	t.Run("目录没有 dlink", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addDir("/folder")
		m.mu.Unlock()
		_, err := b.DownloadRange(ctx, "/folder", 0, 10)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("下载目录应 ErrNotFound（跨后端一致），实得 %v", err)
		}
	})

	t.Run("文件被删后 404 清缓存", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addFile("/gone.cpenc", patternBytes(64))
		m.mu.Unlock()
		if _, err := b.DownloadRange(ctx, "/gone.cpenc", 0, 10); err != nil {
			t.Fatalf("下载成功路径前置失败: %v", err)
		}
		m.mu.Lock()
		delete(m.entries, "/gone.cpenc") // 模拟远端被别处删除
		m.mu.Unlock()
		// 缓存已因 remove 失效会重查；直接删 dlink 引用后应 404 → ErrNotFound
		_, err := b.DownloadRange(ctx, "/gone.cpenc", 0, 10)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("dlink 404 应 ErrNotFound，实得 %v", err)
		}
	})

	t.Run("中文路径往返", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		payload := patternBytes(256)
		m.mu.Lock()
		m.addFile("/资料/报告.cpenc", payload)
		m.mu.Unlock()
		got, err := b.DownloadRange(ctx, "/资料/报告.cpenc", 10, 99)
		if err != nil || !bytes.Equal(got, payload[10:100]) {
			t.Fatalf("中文路径下载失败: %v", err)
		}
	})
}

// TestBaiduTokenRefresh 验证 errno=111 的刷新链路：并发触发只刷新一次
// （百度刷新轮换 refresh_token，Python 端多线程并发刷新会互相作废）。
func TestBaiduTokenRefresh(t *testing.T) {
	ctx := context.Background()

	t.Run("单飞刷新：并发 8 只刷 1 次", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addFile("/f.cpenc", patternBytes(10))
		m.curAccess = "access-99" // 使后端持有的 access-1 立即失效
		m.mu.Unlock()

		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = b.GetSize(ctx, "/f.cpenc")
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("并发 goroutine %d GetSize 失败: %v", i, err)
			}
		}
		if m.tokenHits != 1 {
			t.Errorf("并发触发 111 应只刷新一次（refreshMu 单飞），实得 %d", m.tokenHits)
		}
		// 最后一次请求应已带上刷新后的新 token
		last := m.accessUsed[len(m.accessUsed)-1]
		if last != m.curAccess {
			t.Errorf("重试应使用新 token，实得 %s 期望 %s", last, m.curAccess)
		}
	})

	t.Run("无 refresh_token 直接报错", func(t *testing.T) {
		d := defaultCred()
		d.RefreshToken = ""
		b, m := newBaiduPair(t, d)
		m.mu.Lock()
		m.addFile("/f.cpenc", patternBytes(10))
		m.curAccess = "access-99"
		m.mu.Unlock()
		_, err := b.GetSize(ctx, "/f.cpenc")
		if err == nil || !errors.Is(err, ErrBackend) {
			t.Fatalf("无 refresh_token 应 ErrBackend，实得 %v", err)
		}
	})

	t.Run("刷新端点失败透传", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addFile("/f.cpenc", patternBytes(10))
		m.curAccess = "access-99"
		m.denyRefresh = true
		m.mu.Unlock()
		_, err := b.GetSize(ctx, "/f.cpenc")
		if err == nil || !errors.Is(err, ErrBackend) {
			t.Fatalf("刷新失败应 ErrBackend，实得 %v", err)
		}
	})
}

// TestBaiduUploadSimple 简单上传（<=4MiB）。
func TestBaiduUploadSimple(t *testing.T) {
	b, m := newBaiduPair(t, defaultCred())
	ctx := context.Background()

	local := filepath.Join(t.TempDir(), "small.bin")
	if err := os.WriteFile(local, patternBytes(1000), 0o644); err != nil {
		t.Fatal(err)
	}
	progress := []float64{}
	progMu := sync.Mutex{}
	onProgress := func(v float64) {
		progMu.Lock()
		progress = append(progress, v)
		progMu.Unlock()
	}

	t.Run("上传新文件", func(t *testing.T) {
		if err := b.UploadChunked(ctx, local, "/remote/small.cpenc", 0, onProgress); err != nil {
			t.Fatalf("UploadChunked 失败: %v", err)
		}
		m.mu.Lock()
		got := m.entries["/remote/small.cpenc"]
		m.mu.Unlock()
		if got == nil || !bytes.Equal(got.data, patternBytes(1000)) {
			t.Fatal("远端内容应与本地一致")
		}
		if m.uploadHits != 1 {
			t.Errorf("应走一次简单上传，实得 %d", m.uploadHits)
		}
		progMu.Lock()
		if len(progress) != 1 || progress[len(progress)-1] != 1.0 {
			t.Errorf("简单上传进度应恰为 [1.0]，实得 %v", progress)
		}
		progMu.Unlock()
	})

	t.Run("同名覆盖上传", func(t *testing.T) {
		m.mu.Lock()
		m.addFile("/remote/small.cpenc", []byte("旧内容"))
		m.mu.Unlock()
		if err := b.UploadChunked(ctx, local, "/remote/small.cpenc", 0, nil); err != nil {
			t.Fatalf("覆盖上传失败: %v", err)
		}
		m.mu.Lock()
		got := m.entries["/remote/small.cpenc"]
		m.mu.Unlock()
		if len(got.data) != 1000 {
			t.Errorf("ondup=overwrite 应整体替换，实得 %d 字节", len(got.data))
		}
	})

	t.Run("远端已完整则跳过", func(t *testing.T) {
		m.mu.Lock()
		hitsBefore := m.uploadHits
		m.mu.Unlock()
		got := []float64{}
		if err := b.UploadChunked(ctx, local, "/remote/small.cpenc", 0, func(v float64) { got = append(got, v) }); err != nil {
			t.Fatalf("重复上传失败: %v", err)
		}
		m.mu.Lock()
		if m.uploadHits != hitsBefore {
			t.Error("远端已完整（大小一致）时不应再发上传请求")
		}
		m.mu.Unlock()
		if len(got) != 1 || got[0] != 1.0 {
			t.Errorf("跳过时进度应恰为 [1.0]，实得 %v", got)
		}
	})
}

// TestBaiduUploadSuperfile 分片上传（>4MiB）：并发分片 + 单片失败重试 + 合并。
func TestBaiduUploadSuperfile(t *testing.T) {
	b, m := newBaiduPair(t, defaultCred())
	ctx := context.Background()

	// 3 片（4MiB×3 + 尾巴）：第 2 片第一次上传注入失败 → 触发重试
	const size = 3*4<<20 + 12345
	local := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(local, patternBytes(size), 0o644); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.partFailSeq = 1
	m.partFailArmed = true
	m.mu.Unlock()

	progress := []float64{}
	progMu := sync.Mutex{}
	onProgress := func(v float64) {
		progMu.Lock()
		progress = append(progress, v)
		progMu.Unlock()
	}

	if err := b.UploadChunked(ctx, local, "/movie/big.cpenc", 1<<20, onProgress); err != nil {
		t.Fatalf("分片上传失败: %v", err)
	}
	m.mu.Lock()
	got := m.entries["/movie/big.cpenc"]
	muHits := m.uploadHits
	superHits := m.superHits
	m.mu.Unlock()
	if got == nil || len(got.data) != size {
		t.Fatalf("远端文件大小应为 %d，实得 %d", size, len(got.data))
	}
	expect := patternBytes(size)
	if !bytes.Equal(got.data, expect) {
		t.Fatal("分片合并内容应与本地逐字节一致")
	}
	if muHits != 0 {
		t.Errorf(">4MiB 不应走简单上传，实得 %d", muHits)
	}
	// 3×4MiB+12345 → 4 片（3 整片 + 尾片）；第 2 片注入失败重试 1 次 = 5 次
	m.mu.Lock()
	log := append([]string(nil), m.partLog...)
	m.mu.Unlock()
	if superHits != 5 {
		t.Errorf("superfile2 应命中 5 次（4 片 + 1 次重试），实得 %d，轨迹 %v", superHits, log)
	}
	progMu.Lock()
	defer progMu.Unlock()
	if len(progress) == 0 || progress[len(progress)-1] != 1.0 {
		t.Fatalf("进度末值应为 1.0，实得 %v", progress)
	}
	for i := 1; i < len(progress); i++ {
		if progress[i] < progress[i-1] {
			t.Errorf("进度应单调递增（并发完成顺序不定），实得 %v", progress)
		}
	}
	ones := 0
	for _, v := range progress {
		if v == 1.0 {
			ones++
		}
	}
	if ones != 1 {
		t.Errorf("1.0 只能出现一次（内部跳过 + 结尾统一补发），实得 %v", progress)
	}
}

func TestBaiduMkdirRenameDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("Mkdir 幂等", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addDir("/base")
		m.mu.Unlock()
		if err := b.Mkdir(ctx, "/base/sub"); err != nil {
			t.Fatalf("Mkdir 失败: %v", err)
		}
		if err := b.Mkdir(ctx, "/base/sub"); err != nil {
			t.Fatalf("重复 Mkdir 应幂等成功（errno=-8 吞掉），实得 %v", err)
		}
		m.mu.Lock()
		_, ok := m.entries["/base/sub"]
		m.mu.Unlock()
		if !ok {
			t.Fatal("目录应已创建")
		}
	})

	t.Run("同父重命名走 rename 接口", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addFile("/base/old.cpenc", patternBytes(80))
		m.mu.Unlock()
		// 先读一次让缓存填充（验证 rename 后旧缓存被移除）
		if _, err := b.GetSize(ctx, "/base/old.cpenc"); err != nil {
			t.Fatal(err)
		}
		if err := b.Rename(ctx, "/base/old.cpenc", "/base/new.cpenc"); err != nil {
			t.Fatalf("同父 Rename 失败: %v", err)
		}
		m.mu.Lock()
		_, oldGone := m.entries["/base/old.cpenc"]
		_, newHere := m.entries["/base/new.cpenc"]
		m.mu.Unlock()
		if oldGone || !newHere {
			t.Fatal("文件应已从旧路径移到新路径")
		}
		if _, err := b.GetSize(ctx, "/base/new.cpenc"); err != nil {
			t.Fatalf("新路径应可读: %v", err)
		}
		// 旧路径缓存键已被 remove，查旧路径应 miss → false
		if ok, err := b.Exists(ctx, "/base/old.cpenc"); err != nil || ok {
			t.Errorf("旧路径应不存在，实得 ok=%v err=%v", ok, err)
		}
	})

	t.Run("跨目录移动走 filemanager move", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addDir("/src")
		m.addDir("/dst")
		m.addFile("/src/move.cpenc", patternBytes(30))
		m.mu.Unlock()
		if err := b.Rename(ctx, "/src/move.cpenc", "/dst/land.cpenc"); err != nil {
			t.Fatalf("跨父移动失败: %v", err)
		}
		m.mu.Lock()
		_, srcGone := m.entries["/src/move.cpenc"]
		dst, dstHere := m.entries["/dst/land.cpenc"]
		m.mu.Unlock()
		if srcGone || !dstHere || len(dst.data) != 30 {
			t.Fatal("跨父移动后内容应在目标路径")
		}
	})

	t.Run("删除文件与目录且幂等", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addDir("/del")
		m.addFile("/del/a.cpenc", patternBytes(5))
		m.addFile("/del/sub/b.cpenc", patternBytes(6))
		m.addDir("/del/sub")
		m.mu.Unlock()

		if err := b.Delete(ctx, "/del/sub/b.cpenc"); err != nil {
			t.Fatalf("删除文件失败: %v", err)
		}
		// 删除不存在：errno=12 幂等成功
		if err := b.Delete(ctx, "/del/sub/b.cpenc"); err != nil {
			t.Fatalf("重复删除应幂等成功，实得 %v", err)
		}
		// 目录递归删除（子树 b.cpenc 已删，a.cpenc 在 /del 下保留）
		if err := b.Delete(ctx, "/del/sub"); err != nil {
			t.Fatalf("删除目录失败: %v", err)
		}
		m.mu.Lock()
		_, subGone := m.entries["/del/sub"]
		_, aHere := m.entries["/del/a.cpenc"]
		m.mu.Unlock()
		if subGone || !aHere {
			t.Fatal("目录应被删、兄弟文件应保留")
		}
	})
}

// TestBaiduBackoffAndErrors 429 退避与非 JSON 响应。
func TestBaiduBackoffAndErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("429 退避后成功", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addFile("/f.cpenc", patternBytes(8))
		m.rateLimitLeft = 2 // 前两次请求被限流
		m.mu.Unlock()
		size, err := b.GetSize(ctx, "/f.cpenc")
		if err != nil || size != 8 {
			t.Fatalf("退避重试后应成功: size=%d err=%v", size, err)
		}
		if m.listHits != 1 {
			t.Errorf("只有最后一次成功请求应计入 list，实得 %d", m.listHits)
		}
	})

	t.Run("响应非 JSON", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addFile("/f.cpenc", patternBytes(8))
		m.badJSON = true
		m.mu.Unlock()
		_, err := b.GetSize(ctx, "/f.cpenc")
		if err == nil || !errors.Is(err, ErrBackend) {
			t.Fatalf("非 JSON 响应应 ErrBackend，实得 %v", err)
		}
		if !strings.Contains(err.Error(), "HTTP 200") {
			t.Errorf("错误文案应带真实状态码，实得 %v", err)
		}
	})

	t.Run("接口失败不折叠成不存在", func(t *testing.T) {
		b, m := newBaiduPair(t, defaultCred())
		m.mu.Lock()
		m.addFile("/f.cpenc", patternBytes(8))
		m.denyRefresh = true
		m.curAccess = "access-99" // 111 刷新失败 → 错误应透传
		m.mu.Unlock()
		ok, err := b.Exists(ctx, "/f.cpenc")
		if err == nil {
			t.Fatal("刷新失败时 Exists 应返回错误而非折叠 false")
		}
		if ok {
			t.Error("错误场景 ok 应为 false")
		}
	})
}

// TestBaiduAuthURL 授权 URL 构造。
func TestBaiduAuthURL(t *testing.T) {
	u := BaiduAuthURL("my-key", "dev-123")
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("URL 应可解析: %v", err)
	}
	q := parsed.Query()
	if q.Get("response_type") != "code" || q.Get("redirect_uri") != "oob" ||
		q.Get("scope") != "basic,netdisk" || q.Get("client_id") != "my-key" ||
		q.Get("device_id") != "dev-123" {
		t.Errorf("URL 参数不符: %s", u)
	}
	// 含保留字符的参数必须被转义（Python 端 format 直插不转义，这里是有意加固）
	u2 := BaiduAuthURL("a&b=c", "")
	if !strings.Contains(u2, "client_id=a%26b%3Dc") {
		t.Errorf("app_key 应被 QueryEscape，实得 %s", u2)
	}
}

// ---------------------------------------------------------------------------
// 凭证存储（无网络依赖）
// ---------------------------------------------------------------------------

// fakeDPAPI 是 Scheme=DPAPI 的假保护器（测试前缀分支与加解密接线）。
type fakeDPAPI struct{}

func (fakeDPAPI) Protect(d []byte) ([]byte, error) {
	out := make([]byte, len(d))
	for i, c := range d {
		out[i] = c ^ 0x5A
	}
	return out, nil
}
func (fakeDPAPI) Unprotect(d []byte) ([]byte, error) { return fakeDPAPI{}.Protect(d) }
func (fakeDPAPI) Scheme() string                     { return "DPAPI" }

func TestBaiduCredStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baidu.json")

	t.Run("PLAIN 往返含全部键", func(t *testing.T) {
		s := NewBaiduCredStore(path, nil) // nil → PLAIN 兜底
		want := BaiduCredData{
			AppID: "dev", AppKey: "key", SecretKey: "sec",
			SignKey: "sign", AccessToken: "tok", RefreshToken: "ref",
			ExpiresAt: 1728000.5,
		}
		if err := s.Save(want); err != nil {
			t.Fatalf("Save 失败: %v", err)
		}
		got := s.Load()
		if got == nil {
			t.Fatal("Load 不应为 nil")
		}
		if *got != want {
			t.Errorf("往返丢失字段：%+v != %+v", *got, want)
		}
		// 磁盘格式：PLAIN: 前缀 + base64(JSON)；解码后键拼写与 Python 互读
		raw, _ := os.ReadFile(path)
		if !bytes.HasPrefix(raw, []byte("PLAIN:")) {
			t.Errorf("应写 PLAIN: 前缀，实得 %s", raw[:20])
		}
		decoded, err := base64.StdEncoding.DecodeString(string(raw[len("PLAIN:"):]))
		if err != nil {
			t.Fatalf("PLAIN 载荷应可 base64 解码: %v", err)
		}
		for _, key := range []string{"app_id", "app_key", "secret_key", "sign_key",
			"access_token", "refresh_token", "expires_at"} {
			if !bytes.Contains(decoded, []byte(`"`+key+`"`)) {
				t.Errorf("落盘 JSON 缺少键 %s：%s", key, decoded)
			}
		}
	})

	t.Run("DPAPI protector 走加密前缀", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "b.json")
		s := NewBaiduCredStore(p, fakeDPAPI{})
		if err := s.Save(BaiduCredData{AppKey: "k", AccessToken: "t"}); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(p)
		if !bytes.HasPrefix(raw, []byte("DPAPI:")) {
			t.Errorf("应写 DPAPI: 前缀，实得 %s", raw[:20])
		}
		// DPAPI 载荷与 PLAIN 同构：b64 解码后是不可读密文（Python 同款格式）
		if _, err := base64.StdEncoding.DecodeString(string(raw[len("DPAPI:"):])); err != nil {
			t.Errorf("DPAPI 载荷应可 base64 解码: %v", err)
		}
		got := s.Load()
		if got == nil || got.AccessToken != "t" {
			t.Fatalf("DPAPI 往返失败: %+v", got)
		}
	})

	t.Run("前缀与保护器不匹配解不开", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "b.json")
		dpapi := NewBaiduCredStore(p, fakeDPAPI{})
		if err := dpapi.Save(BaiduCredData{AppKey: "k"}); err != nil {
			t.Fatal(err)
		}
		// 同一个文件换 PLAIN 保护器读：DPAPI: 前缀 → 无解密能力 → nil
		if got := NewBaiduCredStore(p, nil).Load(); got != nil {
			t.Errorf("PLAIN 保护器不应解开 DPAPI 凭证，实得 %+v", got)
		}
	})

	t.Run("不存在与损坏都返回 nil", func(t *testing.T) {
		s := NewBaiduCredStore(filepath.Join(t.TempDir(), "nope.json"), nil)
		if got := s.Load(); got != nil {
			t.Errorf("文件不存在应 nil，实得 %+v", got)
		}
		bad := filepath.Join(t.TempDir(), "bad.json")
		os.WriteFile(bad, []byte("垃圾内容"), 0o644)
		if got := NewBaiduCredStore(bad, nil).Load(); got != nil {
			t.Errorf("损坏文件应 nil，实得 %+v", got)
		}
		badPrefix := filepath.Join(t.TempDir(), "bad2.json")
		os.WriteFile(badPrefix, []byte("UNKNOWN:xxxx"), 0o644)
		if got := NewBaiduCredStore(badPrefix, nil).Load(); got != nil {
			t.Errorf("未知前缀应 nil，实得 %+v", got)
		}
	})

	t.Run("Clear 与自动建目录", func(t *testing.T) {
		s := NewBaiduCredStore(filepath.Join(dir, "sub", "deep", "b.json"), nil)
		if err := s.Save(BaiduCredData{AppKey: "k"}); err != nil {
			t.Fatalf("Save 应自动建父目录: %v", err)
		}
		s.Clear()
		if _, err := os.Stat(filepath.Join(dir, "sub", "deep", "b.json")); !os.IsNotExist(err) {
			t.Errorf("Clear 后文件应删除，实得 %v", err)
		}
		s.Clear() // 重复 Clear 容忍
	})
}

// TestBaiduConcurrentDownloadSingleFlight 并发下载同路径：entry/dlink
// 回源应只发生一次（假服务器计数验证单飞生效）。
func TestBaiduConcurrentDownloadSingleFlight(t *testing.T) {
	b, m := newBaiduPair(t, defaultCred())
	ctx := context.Background()
	m.mu.Lock()
	m.addFile("/shared.cpenc", patternBytes(2048))
	m.mu.Unlock()

	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = b.DownloadRange(ctx, "/shared.cpenc", 0, 63)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d 下载失败: %v", i, err)
		}
	}
	if m.listHits != 1 {
		t.Errorf("并发同路径回源应合并为一次 list，实得 %d", m.listHits)
	}
	if m.metasHits != 1 {
		t.Errorf("并发同路径回源应合并为一次 filemetas，实得 %d", m.metasHits)
	}
}

// TestBaiduCacheRemoveRefetch remove 使缓存失效后，再次访问应重新回源
// （覆盖 rename/delete/upload 后立刻查同一路径的场景）。
func TestBaiduCacheRemoveRefetch(t *testing.T) {
	b, m := newBaiduPair(t, defaultCred())
	ctx := context.Background()
	m.mu.Lock()
	m.addFile("/race.cpenc", patternBytes(10))
	m.mu.Unlock()

	// 第一次访问填充缓存；remove 模拟 rename/delete/upload 后的失效
	if _, err := b.GetSize(ctx, "/race.cpenc"); err != nil {
		t.Fatal(err)
	}
	b.cache.remove("/race.cpenc")
	if _, err := b.GetSize(ctx, "/race.cpenc"); err != nil {
		t.Fatalf("失效后重查应重新回源成功: %v", err)
	}
	if m.listHits != 2 {
		t.Errorf("remove 后再查询应重新列父目录，实得 %d", m.listHits)
	}
}
