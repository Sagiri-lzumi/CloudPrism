package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// CounterBlockAt 计算第 blockIdx 块的 16 字节计数器值：
//
//	counter = (int_be(iv) + blockIdx) mod 2^128
//
// 128-bit 大端加法用「高 64 位 + 低 64 位」两个 uint64 完成：低位相加溢出时
// 向高位进一，高位再溢出即自然回绕（等价于 mod 2^128）。
//
// 刻意不使用 math/big —— 流式路径上每 16 字节就要算一次计数器，
// big.Int 的堆分配与通用运算会直接成为吞吐瓶颈。
//
// 对照 WindowsPy/src/cloudprism/crypto/stream_cipher.py:41-48
func CounterBlockAt(iv [protocol.IVLen]byte, blockIdx uint64) [protocol.IVLen]byte {
	var counter [protocol.IVLen]byte

	hi := binary.BigEndian.Uint64(iv[0:8])
	lo := binary.BigEndian.Uint64(iv[8:16])

	sum := lo + blockIdx
	if sum < lo { // 低 64 位溢出，向高 64 位进一
		hi++
	}

	binary.BigEndian.PutUint64(counter[0:8], hi)
	binary.BigEndian.PutUint64(counter[8:16], sum)
	return counter
}

// CTR 是缓存了 AES key schedule 的随机访问流加密器。
//
// 只持有 key 派生出的 cipher.Block 与 IV，不持有任何数据状态，
// 因此可对同一文件的任意区间重复调用 XORAt —— 流式代理的 Range 请求、
// 缩略图的头部随机读取都依赖这一点。
//
// 并发：字段在构造后只读，且 crypto/aes 的 Block 实现无内部状态，
// 故 XORAt 可被多个 goroutine 并发调用（各 goroutine 必须使用各自的
// dst/src 缓冲）。注意这是标准库实现细节而非 cipher.Block 的接口契约，
// 若将来换成别的 Block 实现，应改为每 goroutine 各持一个 CTR
// （复制 key schedule 的成本远低于一次 PBKDF2）。
type CTR struct {
	iv    [protocol.IVLen]byte
	block cipher.Block
}

// NewCTR 构造 AES-256-CTR 随机访问加解密器。
//
// key 必须为 32 字节、iv 必须为 16 字节，否则返回错误
// （对应 Python 侧 AesCtrStreamCipher.__init__ 抛出的 ValueError）。
// 文件头的 SaltLen/IVLen 是从文件里读出来的，被篡改时长度可能不为 16，
// 因此这两个检查是真实可达的防线，不是形式主义的断言。
//
// 对照 WindowsPy/src/cloudprism/crypto/stream_cipher.py:32-39
func NewCTR(key, iv []byte) (*CTR, error) {
	if len(key) != protocol.KeyLen {
		return nil, fmt.Errorf("密钥长度必须为 %d 字节，实为 %d", protocol.KeyLen, len(key))
	}
	if len(iv) != protocol.IVLen {
		return nil, fmt.Errorf("IV 长度必须为 %d 字节，实为 %d", protocol.IVLen, len(iv))
	}

	block, err := aes.NewCipher(key) // key schedule 在此展开一次，之后所有分块复用
	if err != nil {
		return nil, err
	}

	c := &CTR{block: block}
	copy(c.iv[:], iv)
	return c, nil
}

// KeystreamBlock 返回第 blockIdx 块的 16 字节密钥流，即 AES-ECB(key, counter)。
//
// 主要供测试与 Python 端 _keystream_block 对拍；业务路径应直接用 XORAt，
// 后者走标准库 CTR 的 AES-NI 汇编实现，比逐块 ECB 更快。
//
// 对照 WindowsPy/src/cloudprism/crypto/stream_cipher.py:41-48
func (c *CTR) KeystreamBlock(blockIdx uint64) [protocol.BlockSize]byte {
	var out [protocol.BlockSize]byte
	counter := CounterBlockAt(c.iv, blockIdx)
	c.block.Encrypt(out[:], counter[:])
	return out
}

// XORAt 用从 blockIdx 起始的密钥流异或 src，结果写入 dst。
//
// 三条必须保持的语义：
//
//   - **允许 dst 与 src 为同一切片**（原地加解密，零拷贝）。加解密管线与流式代理
//     靠它把峰值内存压到「并发数 × 分片大小」，而不是让整份密文与明文同时驻留；
//   - src 长度**不必是 16 的倍数**：末块只用前 len(src)%16 个密钥流字节，
//     与 Python 端 zip(block, ks) 的自然截断完全一致；
//   - 加解密对称，同一函数既加密也解密（CTR 无填充、明文长度 == 密文长度）。
//
// dst 容量小于 src 长度时 panic：这是调用方的分配错误，静默截断会产出损坏文件。
//
// 对照 WindowsPy/src/cloudprism/crypto/stream_cipher.py:71-76
func (c *CTR) XORAt(dst, src []byte, blockIdx uint64) {
	if len(dst) < len(src) {
		panic(fmt.Sprintf("cryptox: dst 容量 %d 小于 src 长度 %d", len(dst), len(src)))
	}

	counter := CounterBlockAt(c.iv, blockIdx)
	// 标准库 CTR 把入参当作 128-bit 大端计数器逐块自增并回绕，
	// 与 Python 端 (iv_int + n) % 2^128 的语义一致，故可直接复用其汇编实现，
	// 无需自己写逐块 ECB + 异或循环。
	cipher.NewCTR(c.block, counter[:]).XORKeyStream(dst[:len(src)], src)
}
