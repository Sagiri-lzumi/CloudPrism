package cryptox

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// b32NoPad 是去 = 填充的 RFC 4648 Base32 编解码器，
// 字母表必须与 protocol.Base32Alphabet 一致（由 constants_test.go 锁定）。
var b32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

var (
	// ErrFilenameTooShort 表示 Base32 解码后的字节数装不下 nonce + 标签。
	ErrFilenameTooShort = errors.New("文件名密文长度不足")

	// ErrFilenameAuth 表示 GCM 标签校验失败 —— 密钥错误或文件名被篡改。
	// 对应 Python 侧 decrypt_and_verify 抛出的 ValueError。
	ErrFilenameAuth = errors.New("文件名 GCM 标签校验失败")

	// ErrFilenameUTF8 表示解出的明文不是合法 UTF-8。
	// 对应 Python 侧 .decode("utf-8") 抛出的 UnicodeDecodeError。
	ErrFilenameUTF8 = errors.New("文件名明文不是合法 UTF-8")
)

// B32EncodeNoPad 做 Base32 编码并去除 = 填充（输出大写）。
//
// 选 Base32 而非 Base64 是为了规避云盘对大小写敏感与 +、/、= 等特殊字符的限制；
// 去填充是为了保持云端文件名整洁，解码时再补齐。
//
// 对照 WindowsPy/src/cloudprism/crypto/filename.py:25-35
func B32EncodeNoPad(data []byte) string { return b32NoPad.EncodeToString(data) }

// B32DecodeNoPad 解码去填充的 Base32 字符串。
//
// 先转大写（Base32 大小写不敏感，Python 侧同样统一成大写）。
// Go 的 NoPadding 解码器本身就接受非 8 倍数的合法余数，
// 等价于 Python 侧「补 = 到 8 的倍数后再解码」，无需自己补填充。
//
// 与 Python 侧两处已实测的行为差异（均已用两端真实运行对拍，非推测）：
//
//  1. **余数宽容度**：字符数 % 8 ∈ {1, 3, 6} 时 Python 抛
//     `binascii.Error: Incorrect padding`，Go 则丢弃末尾不完整的量子后
//     成功解码（实测：余数 1 只丢多出的那个字符，余数 3/6 会连同
//     同一量子内的字符一起丢掉）。Go 接受的输入集是 Python 的**严格超集**，
//     且在两边都接受的余数 {0,2,4,5,7} 上解出的字节逐字节相同。
//     关键不变式是：宽容解码**只会少给字节，绝不会给出错的字节**
//     （结果恒为正确数据的前缀），因此下游必然落到 ErrFilenameTooShort
//     或 GCM 认证失败，可观测行为与 Python 一致（都是解密失败），
//     不值得为对齐一条异常类型而手写余数校验。
//     注意合法编码的余数只可能是 {0,2,4,5,7}（ceil(8n/5) mod 8），
//     {1,3,6} 永远不会由 B32EncodeNoPad 产出，只会来自损坏或被截断的输入。
//  2. **尾部废弃位**：最后一个字符可能只有高位参与数据、低位被丢弃，
//     两端同样都**不校验**这些废弃位 —— 实测 A/B/C 三个不同字符解出
//     完全相同的字节，Q/R 则解出不同字节并让 GCM 认证失败，Go 与 Python
//     逐项一致。因此「两个不同的 Base32 串解出同一文件名」是预期行为，
//     不是缺陷，也不要为了「严格」去补校验（那会与 Python 分叉）。
//
// 刻意不做 TrimSpace：Python 端也不去空白，多一层宽松只会让
// 「云端文件名被人手动改过」这类问题更难定位。
//
// 对照 WindowsPy/src/cloudprism/crypto/filename.py:38-59
func B32DecodeNoPad(s string) ([]byte, error) {
	return b32NoPad.DecodeString(strings.ToUpper(s))
}

// EncryptFilename 加密文件名，输出 Base32(nonce12 ‖ ct ‖ tag16) 去 = 填充。
//
// 最终云端文件名 = 本函数输出 + protocol.FileExtension。
//
// nonce 每次随机生成，因此同一文件名两次加密的结果必然不同 —— 这是预期行为
// （否则同名文件在云端会暴露「内容相同」的信息）。定值对拍请用
// encryptFilenameWithNonce。
//
// ⚠️ 用的是 12 字节 nonce（NewGCM12），与 Vault Marker 的 16 字节不可混用。
//
// 对照 WindowsPy/src/cloudprism/crypto/filename.py:71-86
func EncryptFilename(plain string, key []byte) (string, error) {
	var nonce [protocol.FilenameNonceLen]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("生成文件名 nonce 失败: %w", err)
	}
	return encryptFilenameWithNonce(plain, key, nonce[:])
}

// encryptFilenameWithNonce 用调用方给定的 nonce 加密，
// 仅供黄金向量测试与 Python 侧输出对拍（测试与被测代码同包，故不导出）。
func encryptFilenameWithNonce(plain string, key []byte, nonce []byte) (string, error) {
	gcm, err := NewGCM12(key)
	if err != nil {
		return "", err
	}

	out := make([]byte, 0, len(nonce)+len(plain)+protocol.GCMTagLen)
	out = append(out, nonce...)
	// Seal 追加的是「密文 ‖ 标签」，与 Python 的 nonce + ct + tag 拼接顺序逐字节一致
	out = gcm.Seal(out, nonce, []byte(plain), nil)

	return B32EncodeNoPad(out), nil
}

// DecryptFilename 解密文件名。
//
// 三类失败用不同 error 区分，但上层通常都当作「该条目无法显示原名」处理：
// 目录树遇到解不开的名字时应回退展示密文名，而不是让整个列表加载失败。
//
// 对照 WindowsPy/src/cloudprism/crypto/filename.py:88-108
func DecryptFilename(encoded string, key []byte) (string, error) {
	raw, err := B32DecodeNoPad(encoded)
	if err != nil {
		return "", fmt.Errorf("文件名 Base32 解码失败: %w", err)
	}
	if len(raw) < protocol.FilenameNonceLen+protocol.GCMTagLen {
		return "", ErrFilenameTooShort
	}

	nonce := raw[:protocol.FilenameNonceLen]
	body := raw[protocol.FilenameNonceLen:] // 密文 ‖ 标签

	gcm, err := NewGCM12(key)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", ErrFilenameAuth
	}
	// Python 侧的 .decode("utf-8") 会在非法字节上抛 UnicodeDecodeError，
	// Go 的 string() 转换则不做校验，故显式补一次以对齐语义。
	if !utf8.Valid(plain) {
		return "", ErrFilenameUTF8
	}
	return string(plain), nil
}
