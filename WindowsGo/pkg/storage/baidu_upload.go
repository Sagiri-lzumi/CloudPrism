package storage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// uploadSuperfile 三步分片上传：precreate → superfile2 逐片 → create。
// 对照 baidu_backend.py:407-464（_upload_superfile）。
//
// 与 Python 的差异：Python 顺序两遍读盘（先读一遍算 md5_list、合并前
// create 时文件早已读回；上传阶段又是逐片串行）。Go 端顺序读一遍算 MD5
// 后 OS 页缓存即热，第二遍用 ReadAt 并发读上传，磁盘 I/O 总量不变、
// 网络成为瓶颈；单片失败重试一次（429/503 退避在 apiOnce 内核）。
//
// 并发窗口取 3：百度未文档化并发上限，保守值兼顾吞吐与限流风险。
// 进度回调按「已上传字节 / 总字节」原子累计并保证单调递增——并发完成
// 顺序不定，直接回调会出现进度倒退的视觉抖动。
func (b *Baidu) uploadSuperfile(
	ctx context.Context, f *os.File, abs string, total int64, onProgress func(float64),
) error {
	partCount := (total + baiduPartSize - 1) / baiduPartSize

	// 1) 顺序读一遍算各分片 MD5（block_list 是分片 MD5 的 JSON 数组）
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%w: 定位本地源文件失败: %v", ErrBackend, err)
	}
	buf := make([]byte, baiduPartSize)
	md5s := make([]string, 0, partCount)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			sum := md5.Sum(buf[:n])
			md5s = append(md5s, hex.EncodeToString(sum[:]))
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break // ErrUnexpectedEOF = 最后一片短读，正常结束
		}
		if err != nil {
			return fmt.Errorf("%w: 读取本地源文件失败: %v", ErrBackend, err)
		}
	}
	blockList, _ := json.Marshal(md5s)

	// 2) precreate 预创建，取 uploadid（百度据此合并分片）
	q := url.Values{}
	q.Set("method", "precreate")
	form := url.Values{}
	form.Set("path", abs)
	form.Set("size", strconv.FormatInt(total, 10))
	form.Set("isdir", "0")
	form.Set("block_list", string(blockList))
	form.Set("ondup", "newcopy")
	out, err := b.api(ctx, apiParams{endpoint: "file", query: q, form: form})
	if err != nil {
		return err
	}
	uploadid := jsonStr(out, "uploadid")
	if uploadid == "" {
		return fmt.Errorf("%w: precreate 未返回 uploadid：%s", ErrBackend, truncateJSON(out))
	}

	// 3) 并发逐片上传（窗口 3）；任一失败取消其余并保留首个错误
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		limiter  = make(chan struct{}, 3)
		errMu    sync.Mutex
		firstErr error
		uploaded atomic.Int64
		progMu   sync.Mutex
		lastProg float64
	)
	recordErr := func(err error) {
		errMu.Lock()
		defer errMu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel() // 首个错误出现即终止其余分片（它们检查 ctx）
		}
	}
	emitProgress := func() {
		v := float64(uploaded.Load()) / float64(total)
		progMu.Lock()
		defer progMu.Unlock()
		// 内部不回调 1.0：UploadChunked 成功后统一补发一次完成信号，
		// 避免「分片内已到 1.0 + 结尾补发」的重复回调
		if v >= 1 {
			return
		}
		if onProgress != nil && v > lastProg {
			lastProg = v
			onProgress(v)
		}
	}
	for seq := int64(0); seq < partCount; seq++ {
		select {
		case limiter <- struct{}{}:
		case <-ctx.Done():
			goto waitAll // 已有分片失败，不再派发新任务
		}
		wg.Add(1)
		go func(seq int64) {
			defer wg.Done()
			defer func() { <-limiter }()
			// 读本片（ReadAt 并发安全，偏移互不干扰）
			partLen := int64(baiduPartSize)
			if rem := total - seq*baiduPartSize; rem < partLen {
				partLen = rem
			}
			data := make([]byte, partLen)
			if _, err := f.ReadAt(data, seq*baiduPartSize); err != nil {
				recordErr(fmt.Errorf("%w: 读取分片 %d 失败: %v", ErrBackend, seq, err))
				return
			}
			// 单片失败重试一次；重试间短暂让路，避免故障期打满限流
			var lastErr error
			for attempt := 0; attempt < 2; attempt++ {
				if ctx.Err() != nil {
					return // 别的分片已失败，本片不必再试
				}
				lastErr = b.uploadPart(ctx, abs, uploadid, seq, data)
				if lastErr == nil {
					break
				}
				select {
				case <-time.After(300 * time.Millisecond):
				case <-ctx.Done():
					return
				}
			}
			if lastErr != nil {
				recordErr(lastErr)
				return
			}
			uploaded.Add(int64(len(data)))
			emitProgress()
		}(seq)
	}

waitAll:
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	// 4) create 合并分片为最终文件
	q2 := url.Values{}
	q2.Set("method", "create")
	form2 := url.Values{}
	form2.Set("path", abs)
	form2.Set("size", strconv.FormatInt(total, 10))
	form2.Set("isdir", "0")
	form2.Set("uploadid", uploadid)
	form2.Set("block_list", string(blockList))
	if _, err := b.api(ctx, apiParams{endpoint: "file", query: q2, form: form2}); err != nil {
		return err
	}
	return nil
}

// uploadPart 上传单片：/superfile2?method=upload&type=tmpfile&path=...
// 对照 baidu_backend.py:438-448。
func (b *Baidu) uploadPart(
	ctx context.Context, abs, uploadid string, seq int64, data []byte,
) error {
	q := url.Values{}
	q.Set("method", "upload")
	q.Set("type", "tmpfile")
	q.Set("path", abs)
	form := url.Values{}
	form.Set("partseq", strconv.FormatInt(seq, 10))
	form.Set("uploadid", uploadid)
	_, err := b.api(ctx, apiParams{
		endpoint: "superfile2", query: q, form: form,
		upload: data, uploadName: "chunk",
	})
	return err
}
