package storage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// 存储后端契约测试：本地/WebDAV/百度网盘三类后端跑同一组用例，验证
// Backend 接口语义一致、可无缝互换。上层依赖该契约，不感知后端类型。
// 对照 WindowsPy/tests/test_backend_contract.py 的 14 个用例。

// sampleBytes 256 字节测试数据（bytes(range(256)) 的 Go 版）。
func sampleBytes() []byte {
	out := make([]byte, 256)
	for i := range out {
		out[i] = byte(i)
	}
	return out
}

// contractFixture 为每类后端提供新实例（各自独立，互不串扰）。
type contractFixture struct {
	kind    string
	backend Backend
	// preplace 预置远端文件内容（断点续传用例：模拟已传的部分内容）
	preplace func(t *testing.T, path string, data []byte)
}

func newContractFixtures(t *testing.T) []contractFixture {
	t.Helper()
	ctx := context.Background()

	localRoot := filepath.Join(t.TempDir(), "backend_root")
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	localB, err := NewLocal(localRoot)
	if err != nil {
		t.Fatal(err)
	}

	webB, wm := newWebDAVPair(t, false)
	baiduB, bm := newBaiduPair(t, defaultCred())
	_ = ctx

	return []contractFixture{
		{
			kind: "local", backend: localB,
			preplace: func(t *testing.T, path string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(localRoot, path), data, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			kind: "webdav", backend: webB,
			preplace: func(t *testing.T, path string, data []byte) {
				t.Helper()
				wm.mu.Lock()
				wm.files[wm.normalize("/"+path)] = data
				wm.mu.Unlock()
			},
		},
		{
			kind: "baidu", backend: baiduB,
			preplace: func(t *testing.T, path string, data []byte) {
				t.Helper()
				bm.mu.Lock()
				bm.addFile("/"+path, data)
				bm.mu.Unlock()
			},
		},
	}
}

func TestBackendContract(t *testing.T) {
	for _, fx := range newContractFixtures(t) {
		fx := fx
		t.Run(fx.kind, func(t *testing.T) {
			runBackendContract(t, fx)
		})
	}
}

// runBackendContract 执行全部契约用例。
func runBackendContract(t *testing.T, fx contractFixture) {
	t.Helper()
	ctx := context.Background()
	b := fx.backend
	write := func(t *testing.T, local, remote string) []float64 {
		t.Helper()
		if err := os.WriteFile(local, sampleBytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		prog := []float64{}
		mu := sync.Mutex{}
		if err := b.UploadChunked(ctx, local, remote, 64, func(v float64) {
			mu.Lock()
			prog = append(prog, v)
			mu.Unlock()
		}); err != nil {
			t.Fatalf("UploadChunked 失败: %v", err)
		}
		return prog
	}

	t.Run("空根列表为空", func(t *testing.T) {
		entries, err := b.ListDir(ctx, "/")
		if err != nil {
			t.Fatalf("ListDir 失败: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("空根应 0 条，实得 %v", entries)
		}
	})

	t.Run("上传后可列出且元信息正确", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.bin")
		write(t, src, "data.cpenc")
		entries, err := b.ListDir(ctx, "/")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("根应只有 data.cpenc，实得 %v", entries)
		}
		e := entries[0]
		if e.Name != "data.cpenc" || e.IsDir || e.Size != 256 {
			t.Errorf("条目元信息不符: %+v", e)
		}
	})

	t.Run("get_size 与上传大小一致", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.bin")
		write(t, src, "f.bin")
		size, err := b.GetSize(ctx, "f.bin")
		if err != nil || size != 256 {
			t.Fatalf("GetSize 应为 256，实得 %d err=%v", size, err)
		}
	})

	t.Run("整文件下载还原", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.bin")
		write(t, src, "f.bin")
		got, err := b.DownloadRange(ctx, "f.bin", 0, 255)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, sampleBytes()) {
			t.Error("整文件下载应与源一致")
		}
	})

	t.Run("部分范围下载", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.bin")
		write(t, src, "f.bin")
		got, err := b.DownloadRange(ctx, "f.bin", 50, 99)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, sampleBytes()[50:100]) {
			t.Error("部分范围下载错位")
		}
	})

	t.Run("单字节下载", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.bin")
		write(t, src, "f.bin")
		got, err := b.DownloadRange(ctx, "f.bin", 100, 100)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, sampleBytes()[100:101]) {
			t.Error("单字节下载错位")
		}
	})

	t.Run("上传后存在性判断", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.bin")
		write(t, src, "f.bin")
		ok, err := b.Exists(ctx, "f.bin")
		if err != nil || !ok {
			t.Fatalf("存在文件应 true: ok=%v err=%v", ok, err)
		}
		ok, err = b.Exists(ctx, "nope.bin")
		if err != nil || ok {
			t.Fatalf("不存在文件应 false: ok=%v err=%v", ok, err)
		}
	})

	t.Run("head 与 get_size 一致", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.bin")
		write(t, src, "f.bin")
		a, err1 := b.Head(ctx, "f.bin")
		c, err2 := b.GetSize(ctx, "f.bin")
		if err1 != nil || err2 != nil || a != c {
			t.Errorf("head=%d size=%d 应一致（errs: %v %v）", a, c, err1, err2)
		}
	})

	t.Run("mkdir 后可列出", func(t *testing.T) {
		if err := b.Mkdir(ctx, "subdir"); err != nil {
			t.Fatal(err)
		}
		entries, err := b.ListDir(ctx, "/")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range entries {
			if e.Name == "subdir" && e.IsDir {
				found = true
			}
		}
		if !found {
			t.Errorf("subdir 应作为目录列出，实得 %v", entries)
		}
	})

	t.Run("rename 后旧名消失新名可读", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.bin")
		write(t, src, "old.bin")
		if err := b.Rename(ctx, "old.bin", "new.bin"); err != nil {
			t.Fatal(err)
		}
		ok, _ := b.Exists(ctx, "old.bin")
		if ok {
			t.Error("旧名应不存在")
		}
		ok, err := b.Exists(ctx, "new.bin")
		if err != nil || !ok {
			t.Fatalf("新名应存在: ok=%v err=%v", ok, err)
		}
		size, err := b.GetSize(ctx, "new.bin")
		if err != nil || size != 256 {
			t.Fatalf("新名大小应为 256: %d err=%v", size, err)
		}
	})

	t.Run("delete 后不再存在", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.bin")
		write(t, src, "f.bin")
		if err := b.Delete(ctx, "f.bin"); err != nil {
			t.Fatal(err)
		}
		ok, err := b.Exists(ctx, "f.bin")
		if err != nil || ok {
			t.Fatalf("删除后应不存在: ok=%v err=%v", ok, err)
		}
	})

	t.Run("上传进度单调且末值 1.0", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.bin")
		prog := write(t, src, "f.bin")
		if len(prog) == 0 || prog[len(prog)-1] != 1.0 {
			t.Fatalf("末值应为 1.0，实得 %v", prog)
		}
		for i := 1; i < len(prog); i++ {
			if prog[i] < prog[i-1]-1e-9 {
				t.Errorf("进度应单调非减，实得 %v", prog)
			}
		}
	})

	t.Run("断点续传补全", func(t *testing.T) {
		data := sampleBytes()
		fx.preplace(t, "dst.bin", data[:100])
		src := filepath.Join(t.TempDir(), "src.bin")
		if err := os.WriteFile(src, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := b.UploadChunked(ctx, src, "dst.bin", 64, nil); err != nil {
			t.Fatalf("续传上传失败: %v", err)
		}
		got, err := b.DownloadRange(ctx, "dst.bin", 0, 255)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) {
			t.Error("续传后内容应完整")
		}
	})
}

// TestBuildAndDescribe 工厂与描述工具：构造三后端 + 未知键/未授权错误。
func TestBuildAndDescribe(t *testing.T) {
	t.Run("local 构造与描述", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		b, err := Build(Params{Kind: "local", LocalDir: root})
		if err != nil {
			t.Fatalf("Build(local) 失败: %v", err)
		}
		name, addr, kind := Describe(b)
		if name != "本地文件夹" || addr != root || kind != "local" {
			t.Errorf("Describe 不符: %q %q %q", name, addr, kind)
		}
	})

	t.Run("webdav 构造与描述", func(t *testing.T) {
		// 用假服务器地址走 Build，验证参数接线与 Describe 输出
		probe, _ := newWebDAVPair(t, false)
		b, err := Build(Params{
			Kind: "webdav", WebDAVURL: probe.baseURL,
			WebDAVUser: "u", WebDAVPass: "p",
		})
		if err != nil {
			t.Fatalf("Build(webdav) 失败: %v", err)
		}
		name, addr, kind := Describe(b)
		if name != "WebDAV" || kind != "webdav" || addr == "" {
			t.Errorf("Describe 不符: %q %q %q", name, addr, kind)
		}
	})

	t.Run("baidu 未授权报错", func(t *testing.T) {
		store := NewBaiduCredStore(filepath.Join(t.TempDir(), "nope.json"), nil)
		_, err := Build(Params{Kind: "baidu", BaiduStore: store})
		if err == nil {
			t.Fatal("未授权应报错")
		}
		if !errors.Is(err, ErrBackend) {
			t.Errorf("应归 ErrBackend，实得 %v", err)
		}
		if !IsBaiduUnauthorized(err) {
			t.Errorf("应可被 IsBaiduUnauthorized 识别，实得 %v", err)
		}
	})

	t.Run("baidu 已授权构造", func(t *testing.T) {
		store := NewBaiduCredStore(filepath.Join(t.TempDir(), "b.json"), nil)
		if err := store.Save(defaultCred()); err != nil {
			t.Fatal(err)
		}
		b, err := Build(Params{Kind: "baidu", BaiduStore: store})
		if err != nil {
			t.Fatalf("Build(baidu) 失败: %v", err)
		}
		name, addr, kind := Describe(b)
		if name != "百度网盘" || kind != "baidu" || addr == "" {
			t.Errorf("Describe 不符: %q %q %q", name, addr, kind)
		}
	})

	t.Run("未知类型与缺参数", func(t *testing.T) {
		if _, err := Build(Params{Kind: "s3"}); err == nil {
			t.Error("未知类型应报错")
		}
		if _, err := Build(Params{Kind: "local"}); err == nil {
			t.Error("local 缺目录应报错")
		}
		if _, err := Build(Params{Kind: "webdav", WebDAVURL: "ftp://x"}); err == nil {
			t.Error("webdav 非法 URL 应报错")
		}
	})
}
