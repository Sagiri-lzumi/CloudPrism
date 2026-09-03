package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// ⚠️ 本协议存在两种 GCM nonce 长度，且不可互换：
//
//	16 字节 —— Vault Marker 加密载荷、恢复码块
//	12 字节 —— 文件名加密、缩略图磁盘缓存
//
// 分叉的根源是 WindowsPy 端 vault.py:109 直接把 Marker 的 16 字节 IV 当 GCM
// nonce 用（未截断），而 filename.py:82 用的是 FILENAME_NONCE_LEN=12。
// Go 标准库的 cipher.NewGCM 只接受 12 字节，因此 Vault 路径必须显式放宽
// nonce 长度，否则所有现存密库都打不开，且症状伪装成「主密码错误」，
// 排查极其困难。下面两个构造函数就是这条分叉的唯一入口。

// NewGCM16 构造 nonce 为 16 字节的 AES-256-GCM。
//
// 仅供 Vault Marker 载荷与恢复码块使用（见 CreateMarker / VerifyMarker /
// BuildRecoveryBlob / DecryptRecoveryBlob）。
//
// 对照 WindowsPy/src/cloudprism/crypto/vault.py:109、vault.py:237
func NewGCM16(key []byte) (cipher.AEAD, error) {
	block, err := newAES256(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, protocol.IVLen)
}

// NewGCM12 构造标准 12 字节 nonce 的 AES-256-GCM。
//
// 仅供文件名加密与缩略图磁盘缓存使用（见 EncryptFilename / DecryptFilename）。
//
// 对照 WindowsPy/src/cloudprism/crypto/filename.py:82-83
func NewGCM12(key []byte) (cipher.AEAD, error) {
	block, err := newAES256(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// newAES256 构造 AES-256 分组密码，并显式拒绍其它密钥长度。
//
// 标准库的 aes.NewCipher 同时接受 16/24/32 字节（AES-128/192/256），
// Python 侧的 AES.new 同样宽容。但本协议只用 AES-256（密钥恒由 DeriveKey
// 产出 32 字节），因此把「不是 32 字节」当场拒绍比让它静默降级为 AES-128 安全得多：
// 后者会让 Go 端写出 Python 端解不开的密库，而且从密文字节上看不出任何区别。
func newAES256(key []byte) (cipher.Block, error) {
	if len(key) != protocol.KeyLen {
		return nil, fmt.Errorf("密钥长度必须为 %d 字节（AES-256），实为 %d", protocol.KeyLen, len(key))
	}
	return aes.NewCipher(key)
}

// 关于 Seal/Open 的字节顺序（两端一致，无需额外转换）：
//
//   - Go 的 AEAD.Seal(dst, nonce, plaintext, nil) 追加的是「密文 ‖ 标签」，
//     与 Python 的 ct + tag 拼接顺序逐字节相同；
//   - Go 的 AEAD.Open(dst, nonce, ciphertext, nil) 期望入参同样是「密文 ‖ 标签」，
//     因此 Python 侧手工切出的 tag 在 Go 侧不需要单独传递。
