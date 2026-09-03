package cryptox

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// CTR 黄金向量。输入为 key = 0x00..0x1f，明文 pt[i] = (i*7+3) % 256（100 字节，
// 刻意不是 16 的倍数），由 WindowsPy 端 AesCtrStreamCipher 真实运行后打印。
//
// 复现方式见 kdf_test.go 顶部的说明；iv 有两组：
//   - ffffffffffffffff0000000000000000：低 64 位全 1，验证跨 64 位边界的进位
//   - ffffffffffffffffffffffffffffffff：全 1，验证 mod 2^128 的整体回绕
const (
	goldenCTRKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	goldenCTRIv  = "ffffffffffffffff0000000000000000"
	goldenCTRIvW = "ffffffffffffffffffffffffffffffff"

	goldenKeystream0 = "91658d77eba9ef4e2a4d5619c6c186b7"
	goldenKeystream1 = "d04cf3ddafe859b4a19910a45bbd2858"

	goldenKeystreamW0 = "e999e41d4ca770da5387117b5d8f57ee"
	goldenKeystreamW1 = "f29000b62a499fd0a9f39a6add2e7780"
	goldenKeystreamW2 = "f05d76ae4ab99fe5a6f69b3148c2363d"

	// 100 字节明文的完整密文
	goldenCTRCipher = "926f9c6ff48fc27a110f1f49919fe3dba3367255207ec4100a2ba9649c73fd84" +
		"814670e903bb4eaaef7bfb1e98bd84b56631233f986844cc155dded0548e6f31" +
		"9875e2bbcac34927669ddfd51880218c9fb657ff8ec240582808a43fb94ad210" +
		"6a38ede4"

	// decrypt_range(ct, first_block=3, start=50, end=90) 的输出
	goldenDecryptRange = "61686f767d848b9299a0a7aeb5bcc3cad1d8dfe6edf4fb020910171e252c333a" +
		"41484f565d646b72"

	// decrypt_range(ct, first_block=6, start=96, end=100) 的输出，只覆盖末块的 4 字节
	goldenDecryptTail = "a3aab1b8"
)

// goldenPlaintext 按向量生成规则重建 100 字节明文，
// 与 Python 侧 bytes((i*7+3)%256 for i in range(100)) 完全一致。
func goldenPlaintext() []byte {
	pt := make([]byte, 100)
	for i := range pt {
		pt[i] = byte((i*7 + 3) % 256)
	}
	return pt
}

func mustNewCTR(t *testing.T, keyHex, ivHex string) *CTR {
	t.Helper()
	c, err := NewCTR(mustHex(t, keyHex), mustHex(t, ivHex))
	if err != nil {
		t.Fatalf("NewCTR 失败: %v", err)
	}
	return c
}

func TestCounterBlockAt(t *testing.T) {
	ivOf := func(hexIV string) [protocol.IVLen]byte {
		var iv [protocol.IVLen]byte
		copy(iv[:], mustHex(t, hexIV))
		return iv
	}
	zero := [protocol.IVLen]byte{}
	maxLo := ivOf("0000000000000000ffffffffffffffff")
	allMax := ivOf(goldenCTRIvW)

	tests := []struct {
		name     string
		iv       [protocol.IVLen]byte
		blockIdx uint64
		want     string
	}{
		{"零 IV 零块", zero, 0, "00000000000000000000000000000000"},
		{"零 IV 首块", zero, 1, "00000000000000000000000000000001"},
		{"零 IV 低 64 位填满", zero, ^uint64(0), "0000000000000000ffffffffffffffff"},
		// 低位溢出必须向高位进一，而不是就地回绕 —— 这是 128-bit 计数器
		// 与「两个独立 64-bit 计数器」的分水岭
		{"低 64 位进位", maxLo, 1, "00000000000000010000000000000000"},
		{"低 64 位进位后再加", maxLo, 3, "00000000000000010000000000000002"},
		// 整体回绕：(2^128-1 + 1) mod 2^128 == 0
		{"128 位整体回绕", allMax, 1, "00000000000000000000000000000000"},
		{"128 位整体回绕后再加", allMax, 2, "00000000000000000000000000000001"},
		{"黄金 IV 第 0 块", ivOf(goldenCTRIv), 0, goldenCTRIv},
		{"黄金 IV 第 7 块", ivOf(goldenCTRIv), 7, "ffffffffffffffff0000000000000007"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CounterBlockAt(tc.iv, tc.blockIdx)
			if hex.EncodeToString(got[:]) != tc.want {
				t.Errorf("CounterBlockAt(%x, %d)\n got = %x\nwant = %s", tc.iv, tc.blockIdx, got, tc.want)
			}
		})
	}
}

func TestKeystreamBlockGolden(t *testing.T) {
	tests := []struct {
		name string
		iv   string
		want [3]string
	}{
		{"跨 64 位边界 IV", goldenCTRIv, [3]string{goldenKeystream0, goldenKeystream1, ""}},
		{"回绕 IV", goldenCTRIvW, [3]string{goldenKeystreamW0, goldenKeystreamW1, goldenKeystreamW2}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := mustNewCTR(t, goldenCTRKey, tc.iv)
			for i, want := range tc.want {
				if want == "" {
					continue
				}
				ks := c.KeystreamBlock(uint64(i)) // 先落地再取切片：数组返回值不可寻址
				if got := hex.EncodeToString(ks[:]); got != want {
					t.Errorf("第 %d 块密钥流\n got = %s\nwant = %s", i, got, want)
				}
			}
		})
	}
}

// TestKeystreamBlockMatchesCounterBlockAt 把「密钥流 = AES-ECB(key, 计数器)」
// 这条定义与 CounterBlockAt 串起来，防止两处各算各的计数器。
func TestKeystreamBlockMatchesCounterBlockAt(t *testing.T) {
	c := mustNewCTR(t, goldenCTRKey, goldenCTRIv)

	// 直接用 KeystreamBlock 加密计数器，应当得到「用计数器 0 加密计数器 n」以外的东西；
	// 这里验证的是 XORAt 与 KeystreamBlock 对同一块给出相同的异或结果
	src := goldenPlaintext()[:protocol.BlockSize]

	want := make([]byte, protocol.BlockSize)
	ks := c.KeystreamBlock(0)
	for i := range want {
		want[i] = src[i] ^ ks[i]
	}

	got := make([]byte, protocol.BlockSize)
	c.XORAt(got, src, 0)
	if !bytes.Equal(got, want) {
		t.Fatalf("XORAt 与逐字节异或 KeystreamBlock 的结果不一致\n got = %x\nwant = %x", got, want)
	}
}

func TestXORAtProducesGoldenCiphertext(t *testing.T) {
	pt := goldenPlaintext()
	want := mustHex(t, goldenCTRCipher)
	if len(want) != len(pt) {
		t.Fatalf("黄金密文长度 %d 与明文长度 %d 不符（CTR 无填充，两者必须相等）", len(want), len(pt))
	}

	c := mustNewCTR(t, goldenCTRKey, goldenCTRIv)
	got := make([]byte, len(pt))
	c.XORAt(got, pt, 0)

	if !bytes.Equal(got, want) {
		t.Fatalf("加密结果与 Python 端不一致\n got = %x\nwant = %x", got, want)
	}
}

// TestXORAtInPlace 验证 dst == src 的原地加解密。
//
// 加解密管线与流式代理都靠这条性质把峰值内存压到「并发数 × 分片大小」，
// 一旦标准库的 XORKeyStream 语义变化或有人改成先拷贝再异或，
// 内存占用会翻倍而功能测试仍然全绿 —— 所以必须显式断言。
func TestXORAtInPlace(t *testing.T) {
	pt := goldenPlaintext()
	ct := mustHex(t, goldenCTRCipher)
	c := mustNewCTR(t, goldenCTRKey, goldenCTRIv)

	buf := append([]byte(nil), ct...)
	c.XORAt(buf, buf, 0) // dst 与 src 为同一切片
	if !bytes.Equal(buf, pt) {
		t.Fatalf("原地解密结果错误\n got = %x\nwant = %x", buf, pt)
	}

	c.XORAt(buf, buf, 0) // 再加密一次应回到密文
	if !bytes.Equal(buf, ct) {
		t.Fatalf("原地重加密结果错误\n got = %x\nwant = %x", buf, ct)
	}
}

// TestXORAtDecryptRangeGolden 复刻 Python 侧 decrypt_range 的调用方式：
// 拉取覆盖若干整块的密文（起点按 16 对齐），从 first_block 开始解密，
// 再切片到 [start, end) 明文区间。
func TestXORAtDecryptRangeGolden(t *testing.T) {
	ct := mustHex(t, goldenCTRCipher)
	c := mustNewCTR(t, goldenCTRKey, goldenCTRIv)

	tests := []struct {
		name       string
		firstBlock uint64
		start, end int // 明文绝对偏移
		want       string
	}{
		{"非对齐区间", 3, 50, 90, goldenDecryptRange},
		// 末块只有 4 字节（100 % 16 == 4），验证不满一块时的截断语义
		{"末块不满 16 字节", 6, 96, 100, goldenDecryptTail},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctStart := int(tc.firstBlock) * protocol.BlockSize
			buf := append([]byte(nil), ct[ctStart:]...)

			c.XORAt(buf, buf, tc.firstBlock)

			local := tc.start - ctStart
			got := buf[local : local+(tc.end-tc.start)]
			if hex.EncodeToString(got) != tc.want {
				t.Fatalf("区间解密结果不符\n got = %x\nwant = %s", got, tc.want)
			}
		})
	}
}

// TestXORAtPartialFinalBlock 验证 src 长度不是 16 的倍数时，
// 末块只用前 len(src)%16 个密钥流字节（对应 Python 的 zip 自然截断）。
func TestXORAtPartialFinalBlock(t *testing.T) {
	full := make([]byte, 48)
	if _, err := rand.Read(full); err != nil {
		t.Fatal(err)
	}

	// 整块加密的结果作为基准：只加密前 n 字节必须与之逐字节相同
	all := make([]byte, len(full))
	mustNewCTR(t, goldenCTRKey, goldenCTRIv).XORAt(all, full, 0)

	for _, n := range []int{1, 7, 15, 17, 31, 33, 47} {
		got := make([]byte, n)
		mustNewCTR(t, goldenCTRKey, goldenCTRIv).XORAt(got, full[:n], 0)

		if !bytes.Equal(got, all[:n]) {
			t.Errorf("长度 %d：末块截断语义与整块加密不一致\n got = %x\nwant = %x", n, got, all[:n])
		}
	}
}

// TestAlignedShardsMatchSequential 是阶段 0 那个缺陷的回归防线。
//
// WindowsPy 端曾因并行分段未做 16 字节对齐，让 ≥8MiB 的非 16 倍数文件
// 整段密钥流错位、密文永久损坏，而解密**不报错、静默输出垃圾**
// （CTR 无完整性校验）。这里断言：按 BlockSize 对齐的分段加密，
// 产物必须与一次性顺序加密逐字节相等。
func TestAlignedShardsMatchSequential(t *testing.T) {
	const total = 3*ChunkSmall + 5 // 刻意非 16 倍数
	pt := make([]byte, total)
	if _, err := rand.Read(pt); err != nil {
		t.Fatal(err)
	}

	sequential := make([]byte, total)
	mustNewCTR(t, goldenCTRKey, goldenCTRIv).XORAt(sequential, pt, 0)

	sharded := make([]byte, total)
	for off := 0; off < total; off += ChunkSmall {
		end := min(off+ChunkSmall, total)
		if off%protocol.BlockSize != 0 {
			t.Fatalf("分段偏移 %d 未按 %d 对齐", off, protocol.BlockSize)
		}
		// 每段独立构造 CTR（模拟多 goroutine 各持一份），从该段的块索引起算
		mustNewCTR(t, goldenCTRKey, goldenCTRIv).XORAt(
			sharded[off:end], pt[off:end], uint64(off/protocol.BlockSize))
	}

	if !bytes.Equal(sharded, sequential) {
		t.Fatal("对齐分段加密的产物与顺序加密不一致")
	}
}

// TestUnalignedShardOffsetCorrupts 反向印证上面那条：
// 分段偏移未对齐时产物必然错误，且不会有任何报错。
// 这个测试记录的是「为什么必须对齐」，而不是一个待修的行为。
func TestUnalignedShardOffsetCorrupts(t *testing.T) {
	pt := make([]byte, 64)
	if _, err := rand.Read(pt); err != nil {
		t.Fatal(err)
	}

	sequential := make([]byte, 64)
	mustNewCTR(t, goldenCTRKey, goldenCTRIv).XORAt(sequential, pt, 0)

	// 从偏移 20（非 16 倍数）起当作新的一段，仍按「块索引 = 20/16 = 1」起算：
	// 该段首字节本应异或块内第 4 个密钥流字节，实际却从第 0 个开始 → 整段错位
	broken := make([]byte, 64)
	copy(broken, sequential[:20])
	mustNewCTR(t, goldenCTRKey, goldenCTRIv).XORAt(broken[20:], pt[20:], 20/protocol.BlockSize)

	if bytes.Equal(broken, sequential) {
		t.Fatal("未对齐分段竟然产出了正确结果，说明本用例已失去警示意义")
	}
}

func TestNewCTRRejectsBadLengths(t *testing.T) {
	key := mustHex(t, goldenCTRKey)
	iv := mustHex(t, goldenCTRIv)

	tests := []struct {
		name string
		key  []byte
		iv   []byte
	}{
		{"密钥过短", key[:16], iv},
		{"密钥过长", append(append([]byte(nil), key...), 0x00), iv},
		{"IV 过短", key, iv[:12]},
		{"IV 为空", key, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewCTR(tc.key, tc.iv); err == nil {
				t.Error("应当拒绝非法长度，实际返回了 nil 错误")
			}
		})
	}
}

func TestXORAtPanicsWhenDstTooShort(t *testing.T) {
	// 静默截断会产出损坏文件，因此这里必须炸出来而不是悄悄少写几字节
	defer func() {
		if recover() == nil {
			t.Fatal("dst 短于 src 时应当 panic")
		}
	}()
	c := mustNewCTR(t, goldenCTRKey, goldenCTRIv)
	c.XORAt(make([]byte, 4), make([]byte, 8), 0)
}

// TestCTRCopyMatchesOriginal 验证复制一个 CTR（或各自新建）后行为一致，
// 这是并行分段加解密的前提：每个 goroutine 可以各持一份，不必共享。
func TestCTRCopyMatchesOriginal(t *testing.T) {
	a := mustNewCTR(t, goldenCTRKey, goldenCTRIv)
	b := mustNewCTR(t, goldenCTRKey, goldenCTRIv)

	src := goldenPlaintext()
	outA, outB := make([]byte, len(src)), make([]byte, len(src))
	a.XORAt(outA, src, 5)
	b.XORAt(outB, src, 5)

	if !bytes.Equal(outA, outB) {
		t.Fatal("两个独立构造的 CTR 行为不一致")
	}
}

// BenchmarkCTRThroughput 衡量原地 XOR 的吞吐。
//
// 目标 ≥1.5 GB/s：走标准库 CTR 的 AES-NI 汇编路径，且 dst == src 不产生拷贝。
// 达不到这个量级说明密钥流被逐块手工生成（例如误用了 KeystreamBlock 循环），
// 那时流式播放的 seek 会明显卡顿。
func BenchmarkCTRThroughput(b *testing.B) {
	c, err := NewCTR(mustHex(b, goldenCTRKey), mustHex(b, goldenCTRIv))
	if err != nil {
		b.Fatal(err)
	}
	buf := make([]byte, ChunkLarge)
	if _, err := rand.Read(buf); err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		c.XORAt(buf, buf, 0) // 原地，零分配
	}
}

// benchCounterSink 接住 CounterBlockAt 的返回值。
//
// 必须有这个包级变量：CounterBlockAt 是无副作用的纯函数，若把结果丢给 `_ =`，
// 编译器会把整个调用连同外层循环一起消除，基准测到的就是个空循环。
var benchCounterSink [protocol.IVLen]byte

// BenchmarkCounterBlockAt 单独衡量计数器算术，确认没有引入 math/big 之类的重家伙。
//
// 判读标准：个位数 ns/op 且 0 allocs/op。若看到数百 ns 或每次都有堆分配，
// 说明实现退化成了 big.Int —— 那会让逐块生成密钥流的 KeystreamBlock 路径
// 慢两个数量级，并行加密的分段起始对齐也会跟着变贵。
func BenchmarkCounterBlockAt(b *testing.B) {
	var iv [protocol.IVLen]byte
	// 刻意贴近低 64 位溢出边界，让每次调用都走进 hi++ 那条分支
	binary.BigEndian.PutUint64(iv[8:], ^uint64(0)-3)

	// 输入逐次变化，避免编译器把 64 次相同调用折叠成一次
	var i uint64
	b.ReportAllocs()
	for b.Loop() {
		i++
		benchCounterSink = CounterBlockAt(iv, i)
	}
}
