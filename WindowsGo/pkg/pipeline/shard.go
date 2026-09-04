package pipeline

import "github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"

// ShardAlign 是并行分片粒度（1 MiB）。
//
// 对比 Python 端 4MiB 最小段（encryptor.py:203）：进程池的启动成本迫使
// Python 用大段换收益；goroutine 无此成本，1MiB 粒度在带宽受限时让进度
// 更平滑、取消更及时。1MiB 是 16（AES 块大小）的整数倍，分片边界天然
// 落在密钥流块首 —— 与 cryptox.ChunkLarge 同值，缓冲可直接复用池。
const ShardAlign int64 = 1 << 20

// minParallelSize 是启用并行分片的最小数据量（4 MiB，对齐 Python 端
// encryptor.py:100 的阈值）：更小的文件走顺序路径，省去调度与并发开销。
const minParallelSize = 4 * ShardAlign

// Shard 描述密文主体（不含文件头）的一个连续分片。
type Shard struct {
	Offset int64 // 主体内偏移，恒为 ShardAlign 的整数倍（故也是 16 的倍数）
	Length int64 // 分片长度；末片吃满文件尾，可不足 ShardAlign
}

// Shards 把 size 字节的主体切成 ShardAlign 大小的连续分片。
//
// 任意片偏移都是 ShardAlign 的整数倍 → 同时是 protocol.BlockSize 的整数倍，
// 满足「每片独立从密钥流块首生成」的并行前提（16 对齐纪律，见包注释）。
// size<=0 返回空切片。
func Shards(size int64) []Shard {
	var out []Shard
	for off := int64(0); off < size; off += ShardAlign {
		ln := size - off
		if ln > ShardAlign {
			ln = ShardAlign
		}
		out = append(out, Shard{Offset: off, Length: ln})
	}
	return out
}

// BlockAligned 报告长度是否为 AES 块大小的整数倍（16 对齐断言用）。
func BlockAligned(n int64) bool { return n%int64(protocol.BlockSize) == 0 }
