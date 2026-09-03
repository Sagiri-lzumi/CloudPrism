package cryptox

import "sync"

// 缓冲档位。
//
// 加解密管线与流式代理都是「取一块缓冲 → 原地异或 → 写出 → 归还」的循环。
// 若每轮都 make 一个 1MiB 切片，GB 级文件会产生上千次大对象分配，
// GC 压力足以吃掉相当一部分吞吐。
//
// 与之配套的一条硬性约束：**所有流式路径禁止用 append 增长式拼接字节**
// （bytes.Buffer / append 到 nil 切片），那会在扩容时反复复制整块数据，
// 与池化的意义正好相反。
const (
	// ChunkSmall 是流式代理与缩略图路径的分片缓冲（256 KiB）。
	// 代理的单次响应上限是 2 MiB，用 256 KiB 分片可以在首字节延迟与
	// 内存占用之间取得平衡：首块写出去时只等了一次 256 KiB 的拉取+解密。
	ChunkSmall = 256 << 10

	// ChunkLarge 是加解密管线的主缓冲（1 MiB）。
	// 1 MiB 同时是 protocol.BlockSize 的整数倍，因此按它步长分段天然满足
	// CTR 的 16 字节对齐要求 —— WindowsPy 端曾因为分段未对齐而静默损坏
	// ≥8MiB 的非对齐文件（已于阶段 0 修复），这里从常量层面就把该缺陷堵死。
	ChunkLarge = 1 << 20
)

var (
	smallPool = newBytePool(ChunkSmall)
	largePool = newBytePool(ChunkLarge)
)

func newBytePool(size int) *sync.Pool {
	return &sync.Pool{
		New: func() any {
			b := make([]byte, size)
			return &b
		},
	}
}

// GetSmall 取出一块长度为 ChunkSmall 的缓冲，用完必须 PutSmall 归还。
//
// 返回 *[]byte 而非 []byte：sync.Pool 的存取都要装箱成 interface{}，
// 直接放切片头会因逃逸而每次额外分配；放指针则完全无分配（官方惯用法）。
func GetSmall() *[]byte { return smallPool.Get().(*[]byte) }

// PutSmall 归还 GetSmall 取出的缓冲。
func PutSmall(b *[]byte) { putByte(smallPool, b, ChunkSmall) }

// GetLarge 取出一块长度为 ChunkLarge 的缓冲，用完必须 PutLarge 归还。
func GetLarge() *[]byte { return largePool.Get().(*[]byte) }

// PutLarge 归还 GetLarge 取出的缓冲。
func PutLarge(b *[]byte) { putByte(largePool, b, ChunkLarge) }

// putByte 归还缓冲并归一化长度。
//
// nil 与容量不符的切片被静默丢弃：调用方可能因 defer 里的判空疏漏传进 nil，
// 也可能误把外部切片塞进池里污染档位，两种情况都不值得让整个传输任务失败。
func putByte(p *sync.Pool, b *[]byte, size int) {
	if b == nil || cap(*b) < size {
		return
	}
	*b = (*b)[:size] // 上一次使用可能 reslice 过，归还前恢复到满长
	p.Put(b)
}
