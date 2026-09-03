package cryptox

import (
	"bytes"
	"sync"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// TestChunkSizesAreBlockAligned 把两档缓冲与 CTR 的 16 字节对齐要求绑在一起。
//
// 这是本文件最有价值的一条断言。WindowsPy 端曾因并行加密的分段长度用 ceil
// 而未对齐到 16，导致 ≥8MiB 且大小非 16 倍数的文件密文永久错位、解密不报错
// 静默输出垃圾数据（已于阶段 0 修复）。Go 端的分段步长直接取这两档常量，
// 因此只要有人把它们改成非 16 倍数的「更优」取值，同一缺陷就会原地复活，
// 而且症状依旧伪装成「网盘上的文件坏了」。
func TestChunkSizesAreBlockAligned(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{
		{"ChunkSmall", ChunkSmall},
		{"ChunkLarge", ChunkLarge},
	} {
		if tc.size <= 0 {
			t.Errorf("%s = %d 必须为正", tc.name, tc.size)
		}
		if tc.size%protocol.BlockSize != 0 {
			t.Errorf("%s = %d 不是 %d 的整数倍，按它步长分段会重现密文错位缺陷",
				tc.name, tc.size, protocol.BlockSize)
		}
	}

	// 管线主缓冲必须严格大于代理分片缓冲，否则两档划分失去意义
	if ChunkLarge <= ChunkSmall {
		t.Errorf("ChunkLarge(%d) 应大于 ChunkSmall(%d)", ChunkLarge, ChunkSmall)
	}
	// 具体取值决定了「峰值内存 = 并发数 × ChunkLarge」这条容量模型，
	// 改动它应当是有意识的决定而不是顺手调参
	if ChunkSmall != 256*1024 {
		t.Errorf("ChunkSmall = %d，应为 256 KiB", ChunkSmall)
	}
	if ChunkLarge != 1024*1024 {
		t.Errorf("ChunkLarge = %d，应为 1 MiB", ChunkLarge)
	}
}

// TestGetReturnsFullLengthBuffer 确认取出的缓冲长度恰为档位值。
//
// 若 newBytePool 被改成 make([]byte, 0, size)（理由是「调用方自己会 reslice」），
// 长度就成了 0：XORAt 对空切片既不报错也不干活，明文会被原样写出。
// 症状是「加密上传的文件解密出乱码」，与阶段 0 那个缺陷一样难查。
func TestGetReturnsFullLengthBuffer(t *testing.T) {
	tests := []struct {
		name string
		get  func() *[]byte
		put  func(*[]byte)
		size int
	}{
		{"small", GetSmall, PutSmall, ChunkSmall},
		{"large", GetLarge, PutLarge, ChunkLarge},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.get()
			defer tc.put(b)

			if len(*b) != tc.size {
				t.Errorf("len = %d，应为 %d", len(*b), tc.size)
			}
			if cap(*b) < tc.size {
				t.Errorf("cap = %d，应不小于 %d", cap(*b), tc.size)
			}
		})
	}

	// 两档必须是彼此独立的池：混用会让 GetLarge 拿到 256 KiB 的缓冲，
	// 管线按 1 MiB 步长推进时就会漏掉四分之三的数据
	t.Run("档位互不串池", func(t *testing.T) {
		s := GetSmall()
		PutSmall(s)

		b := GetLarge()
		defer PutLarge(b)
		if len(*b) != ChunkLarge {
			t.Errorf("GetLarge 返回 len = %d，应为 %d", len(*b), ChunkLarge)
		}
	})
}

// TestPutByteGuards 覆盖 putByte 的两个静默丢弃分支。
//
// 这两道守卫不只是「防污染」：当 cap < size 时，紧随其后的归一化 `(*b)[:size]`
// 自己就会 panic（切片超出容量）。调用方在 defer 里归还时若误传了外部切片，
// 少了守卫就是一次传输任务的原地崩溃 —— 而 defer 里的 panic 会顶掉真正的
// 业务错误，排查时看到的是完全无关的堆栈。
func TestPutByteGuards(t *testing.T) {
	const size = 64

	t.Run("nil 不 panic", func(t *testing.T) {
		putByte(newBytePool(size), nil, size)
	})

	t.Run("容量不足不 panic", func(t *testing.T) {
		short := make([]byte, size/2)
		putByte(newBytePool(size), &short, size)
	})

	// 塞入若干带哨兵值的短切片。守卫若失效，putByte 会在归一化那行 panic；
	// 即便将来有人把归一化改成不 panic 的写法，哨兵值也会在这里暴露出来。
	// 取 9 次（比 Put 的 8 次多一次）以覆盖池内全部条目；即使 GC 恰好清空了池，
	// 取到的全新缓冲同样满足断言，不会误报。
	t.Run("外部切片不入池", func(t *testing.T) {
		p := newBytePool(size)
		for range 8 {
			short := bytes.Repeat([]byte{0xAA}, size/2)
			putByte(p, &short, size)
		}
		for range 9 {
			b := p.Get().(*[]byte)
			if len(*b) != size {
				t.Fatalf("取出的缓冲长度 = %d，应为 %d", len(*b), size)
			}
			if bytes.Contains(*b, []byte{0xAA}) {
				t.Fatal("外部短切片被放进了池里，档位已被污染")
			}
		}
	})
}

// TestPutByteRestoresLength 确认归还时把长度恢复到满档。
//
// 调用方常会 reslice —— 代理只写出前 n 字节时天然会 `*b = (*b)[:n]`。
// 若归还时不归一化，下一个使用者拿到的就是短缓冲：len 不足会让 XORAt
// 少异或一截，密文里于是混入未加密的明文片段，解密时表现为随机位置的乱码。
//
// 同样是 Put 多份再 Get 更多份：GC 清空池只会让断言退化为「检查全新缓冲」，
// 不会误报；只有归一化真被删掉时才会取到 len == 7 的缓冲。
func TestPutByteRestoresLength(t *testing.T) {
	const size = 64
	p := newBytePool(size)

	for range 8 {
		b := p.Get().(*[]byte)
		*b = (*b)[:7] // 模拟调用方 reslice 后忘记恢复
		putByte(p, b, size)
	}
	for range 9 {
		b := p.Get().(*[]byte)
		if len(*b) != size {
			t.Fatalf("归还时未恢复长度：取出的缓冲 len = %d，应为 %d", len(*b), size)
		}
		putByte(p, b, size)
	}
}

// TestPoolConcurrentAccess 确认并发取还不会串数据。
//
// 池化缓冲是跨 goroutine 复用的可变状态：若 Get 把同一个仍被他人持有的切片
// 发放了两次（例如误把包级单例当池用），各 goroutine 写入的哨兵值就会互相覆盖。
//
// 为何靠哨兵值而不靠 `-race`：竞态检测器在 Windows 上需要 cgo，而本项目的
// 发布形态是 CGO_ENABLED=0、开发机也没装 gcc，因此 `-race` 根本跑不起来。
// 哨兵值比对是确定性的：它直接检查「数据是否被改写」这个后果，
// 不依赖检测器能否恰好调度到冲突时序。
//
// 注意池化缓冲**不保证归零** —— 上一个使用者写进去的明文可能还在里面。
// 因此调用方必须整块覆写后再使用，本测试的哨兵写入就是这种用法的样板；
// 这也是「隐私上不残留」依赖调用方纪律、而非池本身的明示约定。
func TestPoolConcurrentAccess(t *testing.T) {
	const (
		workers = 8
		rounds  = 200
	)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range workers {
		go func(id byte) {
			defer wg.Done()
			// 每个 worker 用自己的哨兵字节填满整块缓冲
			pattern := bytes.Repeat([]byte{id + 1}, ChunkSmall)
			for range rounds {
				b := GetSmall()
				copy(*b, pattern)
				// 持有期间必须始终是自己的哨兵值；被改写即说明缓冲被重复发放
				if !bytes.Equal(*b, pattern) {
					t.Errorf("worker %d 的缓冲在持有期间被其它 goroutine 覆盖", id)
					PutSmall(b)
					return
				}
				PutSmall(b)
			}
		}(byte(w))
	}
	wg.Wait()
}
