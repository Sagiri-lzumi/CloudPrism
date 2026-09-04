package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// Decryptor 从存储后端下载 .cpenc 容器并解密。
//
// 两个入口对应 Python Decryptor 的两个方法（decryptor.py）：整文件
// DownloadAndDecrypt（落盘，供传输队列）与 DecryptRangeToBytes（按需
// 拉取明文区间，供缩略图与流式代理），以及 Python 端缩略图/流式依赖的
// 头部随机访问语义 —— 本包只产出明文字节，不感知调用场景。
//
// 对照 WindowsPy/src/cloudprism/core/decryptor.py
type Decryptor struct {
	Sess    *session.Session
	Backend storage.Backend
	Workers int // 整文件下载的解密并发分片数；<=1 或小文件自动退化为单分片
}

// headFetchLen 是取文件头时下载的前缀长度（字节）。
//
// 默认文件头 51 字节（magic8+version4+hl4+saltlen1+salt16+ivlen1+iv16+
// flags1），64 字节足够覆盖 salt/iv 均 16 字节的头（对照 decryptor.py:48）。
const headFetchLen = 64

// fetchHeader 下载文件头并解析。
func (d *Decryptor) fetchHeader(ctx context.Context, remote string) (*cryptox.Header, error) {
	head, err := d.Backend.DownloadRange(ctx, remote, 0, headFetchLen-1)
	if err != nil {
		return nil, err
	}
	return cryptox.ParseBytes(head)
}

// DecryptRangeToBytes 解密远程文件的明文区间 [start, end) 为字节（不落盘）。
//
// end <= 0 表示取到文件末尾。start >= end（或文件无内容）返回空字节串。
// end 超过明文总长时 clamp 到文件尾 —— Python 端靠切片自然截断 + 上层
// 吞 IndexError（decryptor.py:99-134），Go 端切片越界直接 panic，故显式
// clamp，语义等价且不依赖异常。
//
// 供缩略图与流式代理使用：按需拉取密文块、内存解密、不落盘。
func (d *Decryptor) DecryptRangeToBytes(ctx context.Context, remote string, start, end int64) ([]byte, error) {
	if d.Sess == nil || d.Backend == nil {
		return nil, ErrNoSession
	}
	header, err := d.fetchHeader(ctx, remote)
	if err != nil {
		return nil, err
	}
	totalCipher, err := d.Backend.GetSize(ctx, remote)
	if err != nil {
		return nil, err
	}
	totalPlain := PlaintextTotal(int64(header.HeaderLength), totalCipher)
	if end <= 0 || end > totalPlain {
		end = totalPlain
	}
	if start >= end || totalPlain <= 0 {
		return []byte{}, nil
	}

	key, err := d.Sess.DeriveKey(header.Salt)
	if err != nil {
		return nil, err
	}
	ctr, err := cryptox.NewCTR(key[:], header.IV)
	if err != nil {
		return nil, err
	}

	r := PlaintextToCipher(int64(header.HeaderLength), start, end)
	// 密文区间终点 clamp 到文件尾（文件尾块可能不满 16 字节）
	if r.CtEnd > totalCipher {
		r.CtEnd = totalCipher
	}
	ct, err := d.Backend.DownloadRange(ctx, remote, r.CtStart, r.CtEnd-1)
	if err != nil {
		return nil, err
	}
	if len(ct) == 0 {
		return []byte{}, nil
	}

	out := make([]byte, len(ct))
	ctr.XORAt(out, ct, uint64(r.FirstBlock))
	from := start % int64(protocol.BlockSize)
	// 视图切片：明文字节在解密块中的起点（起点处 start 可不对齐 16）
	return out[from : from+(end-start)], nil
}

// DownloadAndDecrypt 整文件下载并解密到本地明文文件。
//
// 进度回调 0.0~1.0（可 nil）。空文件产出空明文并回调一次 1.0。
// 数据量达到 minParallelSize 且 Workers>1 时按 ShardAlign 分片并行：
// 每片一次 Range 请求 + 池缓冲原地解密 + WriterAt 精确落位（os.File 的
// WriteAt 并发安全），顺序由偏移保证，无需收集器；否则退化为单分片顺序。
//
// 注意：失败/取消时**不删除**半成品明文 —— 对齐 Python（decryptor.py:82），
// 由上层（传输队列）决定续传或清理。
func (d *Decryptor) DownloadAndDecrypt(ctx context.Context, remote, local string, report func(float64)) error {
	if d.Sess == nil || d.Backend == nil {
		return ErrNoSession
	}
	if report == nil {
		report = func(float64) {}
	}
	header, err := d.fetchHeader(ctx, remote)
	if err != nil {
		return err
	}
	totalCipher, err := d.Backend.GetSize(ctx, remote)
	if err != nil {
		return err
	}
	totalPlain := PlaintextTotal(int64(header.HeaderLength), totalCipher)
	if totalPlain < 0 {
		return errors.New("pipeline: 远端文件小于文件头，容器损坏")
	}

	f, err := os.Create(local)
	if err != nil {
		return err
	}
	defer f.Close()

	if totalPlain == 0 {
		report(1.0)
		return nil
	}

	key, err := d.Sess.DeriveKey(header.Salt)
	if err != nil {
		return err
	}
	ctr, err := cryptox.NewCTR(key[:], header.IV)
	if err != nil {
		return err
	}
	workers := d.Workers
	if workers < 1 || totalPlain < minParallelSize {
		workers = 1
	}
	return decryptShards(ctx, d.Backend, remote, f, ctr, int64(header.HeaderLength), totalPlain, workers, report)
}

// decryptShards 分片下载解密主循环：workers 个 goroutine 消费分片任务。
//
// workers==1 即顺序路径（同一代码，零调度差异）；每个 goroutine 独占一块
// 1MiB 池缓冲（Range 请求优先走零拷贝直写路径，见 storage.ReadRangeInto），
// 完成后归还复用。进度按已完成字节累计（atomic）。
func decryptShards(ctx context.Context, b storage.Backend, remote string, dst io.WriterAt, ctr *cryptox.CTR, hl, totalPlain int64, workers int, report func(float64)) error {
	shards := Shards(totalPlain)
	if len(shards) == 0 {
		return nil
	}
	if workers > len(shards) {
		workers = len(shards)
	}
	var (
		wg   sync.WaitGroup
		done atomic.Int64
		errs = make(chan error, len(shards))
	)
	jobs := make(chan Shard)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				if err := decryptOneShard(ctx, b, remote, dst, ctr, hl, s, totalPlain, &done, report); err != nil {
					errs <- err
					return // 首错：停止消费，让调度方退出
				}
			}
		}()
	}
feed:
	for _, s := range shards {
		if err := ctx.Err(); err != nil {
			break
		}
		select {
		case jobs <- s:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		return err
	}
	return ctx.Err()
}

// decryptOneShard 下载并解密单个分片：Range 请求（含两端）→ 原地 XOR →
// WriterAt 落位到明文偏移。返回错误时进度保持已累计值。
func decryptOneShard(ctx context.Context, b storage.Backend, remote string, dst io.WriterAt, ctr *cryptox.CTR, hl int64, s Shard, totalPlain int64, done *atomic.Int64, report func(float64)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	buf := cryptox.GetLarge()
	defer cryptox.PutLarge(buf)
	ctStart := hl + s.Offset
	ctEnd := ctStart + s.Length - 1 // Range 含两端
	n, err := storage.ReadRangeInto(ctx, b, remote, ctStart, ctEnd, *buf)
	if err != nil {
		return err
	}
	if int64(n) != s.Length {
		// 数据不足：远端是半成品或后端截断，继续只会错位写坏明文
		return errors.New("pipeline: 远端密文数据不足（容器不完整或已损坏）")
	}
	part := (*buf)[:n]
	ctr.XORAt(part, part, uint64(s.Offset)/uint64(protocol.BlockSize))
	if _, err := dst.WriteAt(part, s.Offset); err != nil {
		return err
	}
	if nd := done.Add(int64(n)); nd > 0 {
		report(float64(nd) / float64(totalPlain))
	}
	return nil
}
