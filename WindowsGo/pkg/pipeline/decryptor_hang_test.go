package pipeline

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// headerOnlyBackend 只让文件头（偏移 0 起）可取，主体一律报错。
//
// 嵌入 storage.Backend 接口，只覆写 DownloadRange —— Decryptor 只用到
// DownloadRange 与 GetSize，不需要实现整套后端。
type headerOnlyBackend struct {
	storage.Backend
}

func (b headerOnlyBackend) DownloadRange(ctx context.Context, remote string, start, end int64) ([]byte, error) {
	if start > 0 {
		return nil, errors.New("测试：主体下载失败")
	}
	return b.Backend.DownloadRange(ctx, remote, start, end)
}

// TestDecryptShardsReturnsOnFirstError 钉死「任一分片出错必须整轮返回」。
//
// 回归的是 v1.02 起的真实挂死：jobs 是无缓冲通道，出错 worker 直接 return
// 之后，喂料侧仍阻塞在 `jobs <- s` 上再没有人接收 —— DownloadAndDecrypt
// 永不返回，传输队列的那条调度 goroutine 就此卡住（自动锁库也不再触发）。
// 5 MiB 明文 + 4 个 worker ⇒ 5 个分片，全部 worker 首轮即失败，
// 残余分片必然让旧实现永久阻塞。
func TestDecryptShardsReturnsOnFirstError(t *testing.T) {
	const payloadSize = 5 << 20 // > minParallelSize(4MiB)，保证走并行分片路径

	sess := session.New("pw-decrypt-shard-error")
	payload := make([]byte, payloadSize)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "src.cpenc"))
	if err != nil {
		t.Fatal(err)
	}
	enc := &Encryptor{Sess: sess, Workers: 2}
	if _, err := enc.EncryptFile(context.Background(), src, f, nil); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// 容器搬进本地后端（含文件头，因此 fetchHeader 仍能成功）
	root := filepath.Join(dir, "backend")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(dir, "src.cpenc"), filepath.Join(root, "big.cpenc")); err != nil {
		t.Fatal(err)
	}
	local, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}

	dec := &Decryptor{Sess: sess, Backend: headerOnlyBackend{Backend: local}, Workers: 4}
	done := make(chan error, 1)
	go func() {
		done <- dec.DownloadAndDecrypt(context.Background(), "big.cpenc", filepath.Join(dir, "out.bin"), nil)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("主体下载失败时必须返回错误，实得 nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("首片出错后 DownloadAndDecrypt 未返回：decryptShards 仍会永久阻塞")
	}
}
