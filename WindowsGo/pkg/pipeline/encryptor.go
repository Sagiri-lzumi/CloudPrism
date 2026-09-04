package pipeline

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
)

// Encryptor 把本地明文文件加密为「文件头 + 密文主体」写入目标 WriterAt。
//
// 上传由调用方（传输队列 / 同步引擎）负责：本类型只产出字节。Salt 语义
// 对齐 Python encrypt_and_upload 的 salt 参数（encryptor.py:54-69）——
// 非 nil 时复用（命中 Session 密钥缓存，避免每文件重跑 PBKDF2）；nil 时
// 随机生成。IV 非 nil 仅用于测试注入定值。
//
// 对照 WindowsPy/src/cloudprism/core/encryptor.py
type Encryptor struct {
	Sess    *session.Session
	Workers int    // 并行分片内核数；<=1 或小文件自动退化为顺序
	Salt    []byte // KDF 盐；nil = 随机（密库连接后应传元信息盐复用缓存）
	IV      []byte // CTR 初始向量；nil = 随机（仅测试注入）
}

// ErrNoSession Encryptor / Decryptor 未绑定 Session 时返回。
var ErrNoSession = errors.New("pipeline: 未绑定 Session")

// EncryptFile 加密 srcPath 文件，把「头 + 密文」写到 dst 的 0 偏移起。
//
// 进度回调 onProgress 按已完成数据量回调 0.0~1.0（可 nil）；空文件产出
// 仅含文件头的容器并回调一次 1.0。返回文件头字节。
//
// 并发安全：dst 必须支持并发 WriteAt（os.File 满足）。分片按各自偏移
// 独立落位、无需顺序收集器，峰值内存 = workers × 1MiB（读-密-写共用
// cryptox.GetLarge 池缓冲，原地 XOR）。
func (e *Encryptor) EncryptFile(ctx context.Context, srcPath string, dst io.WriterAt, onProgress func(float64)) ([]byte, error) {
	if e.Sess == nil {
		return nil, ErrNoSession
	}
	st, err := os.Stat(srcPath)
	if err != nil {
		return nil, err
	}
	size := st.Size()

	salt := e.Salt
	if salt == nil {
		salt = make([]byte, protocol.SaltLen)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
	}
	iv := e.IV
	if iv == nil {
		iv = make([]byte, protocol.IVLen)
		if _, err := rand.Read(iv); err != nil {
			return nil, err
		}
	}
	header, err := cryptox.Build(salt, iv, 0x00, protocol.Version)
	if err != nil {
		return nil, err
	}
	if _, err := dst.WriteAt(header, 0); err != nil {
		return nil, err
	}

	key, err := e.Sess.DeriveKey(salt)
	if err != nil {
		return nil, err
	}
	ctr, err := cryptox.NewCTR(key[:], iv)
	if err != nil {
		return nil, err
	}

	report := onProgress
	if report == nil {
		report = func(float64) {}
	}
	if size == 0 {
		report(1.0)
		return header, nil
	}

	hl := int64(len(header)) // 密文主体起点
	if e.Workers <= 1 || size < minParallelSize {
		err = encryptSequential(ctx, srcPath, dst, ctr, hl, size, report)
	} else {
		err = encryptParallel(ctx, srcPath, dst, ctr, hl, size, e.Workers, report)
	}
	return header, err
}

// encryptSequential 单核顺序加密：1MiB 步长读-密-写，进度按字节累计。
// 对应 Python 端 _encrypt_sequential（encryptor.py:147-176）。
func encryptSequential(ctx context.Context, srcPath string, dst io.WriterAt, ctr *cryptox.CTR, hl, size int64, report func(float64)) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	buf := cryptox.GetLarge()
	defer cryptox.PutLarge(buf)
	var done int64
	for done < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(src, *buf)
		if err == io.EOF {
			break
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return err
		}
		if n == 0 {
			break
		}
		part := (*buf)[:n]
		// 计数器块索引 = 主体内偏移 / 16（文件头不参与计数，两端一致）
		ctr.XORAt(part, part, uint64(done)/uint64(protocol.BlockSize))
		if _, err := dst.WriteAt(part, hl+done); err != nil {
			return err
		}
		done += int64(n)
		report(float64(done) / float64(size))
	}
	return nil
}

// encryptParallel 并行分片加密：限流信号量约束并发 goroutine 数，
// 每个 goroutine 独占一块 1MiB 池缓冲（读 → 原地 XOR → 写 → 归还），
// 重复使用零分配。进度按已完成字节累计（atomic），末值 1.0。
// 对应 Python 端 _parallel_encrypt（encryptor.py:178-247）。
func encryptParallel(ctx context.Context, srcPath string, dst io.WriterAt, ctr *cryptox.CTR, hl, size int64, workers int, report func(float64)) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	shards := Shards(size)
	if len(shards) == 0 {
		return nil
	}
	if workers > len(shards) {
		workers = len(shards)
	}
	sem := make(chan struct{}, workers)
	var (
		wg   sync.WaitGroup
		done atomic.Int64
		errs = make(chan error, len(shards)) // 每片至多一个错误
	)
	for i := range shards {
		if err := ctx.Err(); err != nil {
			break // 已取消：不启动新片，等待在途片结束后返回
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(s Shard) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := encryptOneShard(ctx, src, dst, ctr, hl, s, size, &done, report); err != nil {
				errs <- err
			}
		}(shards[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return err // 首错即返；其余片错误一并丢弃（同因错误居多）
	}
	return ctx.Err()
}

// encryptOneShard 加密单个分片：读源 → 原地 XOR → 写目标偏移。
func encryptOneShard(ctx context.Context, src io.ReaderAt, dst io.WriterAt, ctr *cryptox.CTR, hl int64, s Shard, size int64, done *atomic.Int64, report func(float64)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	buf := cryptox.GetLarge()
	defer cryptox.PutLarge(buf)
	n, err := src.ReadAt(*buf, s.Offset)
	if err != nil && err != io.EOF {
		return err
	}
	if n == 0 {
		return nil
	}
	part := (*buf)[:n]
	ctr.XORAt(part, part, uint64(s.Offset)/uint64(protocol.BlockSize))
	if _, err := dst.WriteAt(part, hl+s.Offset); err != nil {
		return err
	}
	if nd := done.Add(int64(n)); nd > 0 {
		report(float64(nd) / float64(size))
	}
	return nil
}
