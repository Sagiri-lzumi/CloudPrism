package thumb

import (
	"context"
	"fmt"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/pipeline"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// headerEstimate 加密文件头估算长度（同 transfer.HeaderEstimate=51；
// 这里只用于把容器大小换算成明文上界做分派判断，不进协议）。
const headerEstimate = 51

// MaxWholeBytes 明文总长在此阈值内的图片整文件解密后缩放。
//
// Go 标准库的 JPEG/PNG 解码器不像 QImage 支持渐进/半图解析：截断数据
// 一律解码失败。因此小/中图（覆盖绝大多数照片）必须拿全量明文；超大
// 文件（>8MiB 的长图/大分辨率图）退而求其次：只取头部 512KiB 试解码，
// 失败即回退占位图标（对齐 Python 端大图「数据不足返回 None」的语义）。
const MaxWholeBytes = 8 << 20

// MaxSourceBytes 取超大文件时最多拉取的明文上限（对照 thumbnail.py:31）。
const MaxSourceBytes = 512 * 1024

// MakeThumbnail 把任意受支持格式的图片字节解码为缩略图 JPEG。
//
// 源数据不完整（头部截断导致解码失败）时返回错误，调用方回退占位图标；
// 对照 Python 端「数据不足返回 None 由 QImage 判定」的同一语义。
func MakeThumbnail(data []byte) ([]byte, error) {
	src, err := decodeAny(data)
	if err != nil {
		return nil, fmt.Errorf("缩略图解码失败: %w", err)
	}
	thumb, err := encodeJPEG(scale192(src))
	if err != nil {
		return nil, fmt.Errorf("缩略图重编码失败: %w", err)
	}
	return thumb, nil
}

// Fetch 取远程图片的缩略图（服务端缩放 JPEG），两级缓存加速。
//
// 流程：缓存命中直接返回；未命中则解密远程容器（≤ MaxWholeBytes 整文件，
// 否则头部 ≤ MaxSourceBytes 试解——AES-CTR 随机访问无需整文件下载），
// 服务端缩放重编码后入缓存。任何一步失败返回 nil 与错误描述（前端回退
// 占位图标，不阻塞列表渲染——对齐 fetch_thumbnail 的吞异常语义）。
func Fetch(ctx context.Context, sess *session.Session, backend storage.Backend,
	remotePath string, cache *Cache) ([]byte, error) {
	if cache != nil {
		if hit := cache.Get(remotePath); hit != nil {
			return hit, nil
		}
	}

	dec := &pipeline.Decryptor{Sess: sess, Backend: backend, Workers: 1}

	// 先探明文总长决定取数策略；拿不到大小（远端缺失/网络错）直接失败
	size, err := backend.GetSize(ctx, remotePath)
	if err != nil {
		return nil, fmt.Errorf("缩略图源探测失败: %w", err)
	}
	plainTotal := size - headerEstimate
	if plainTotal < 0 {
		return nil, fmt.Errorf("缩略图源容器损坏（大小 %d < 头）", size)
	}

	var data []byte
	if plainTotal <= MaxWholeBytes {
		data, err = dec.DecryptRangeToBytes(ctx, remotePath, 0, -1) // end<=0 取到文件尾
	} else {
		data, err = dec.DecryptRangeToBytes(ctx, remotePath, 0, MaxSourceBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("缩略图源解密失败: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("缩略图源为空")
	}
	thumb, err := MakeThumbnail(data)
	if err != nil {
		return nil, fmt.Errorf("缩略图生成失败（数据不足或格式不支持）: %w", err)
	}
	if cache != nil {
		cache.Put(remotePath, thumb)
	}
	return thumb, nil
}
