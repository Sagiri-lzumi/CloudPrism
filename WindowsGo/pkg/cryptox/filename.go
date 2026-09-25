package cryptox

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// b32NoPad 是去 = 填充的 RFC 4648 Base32 编解码器，
// 字母表必须与 protocol.Base32Alphabet 一致（由 constants_test.go 锁定）。
//
// ⚠️ 自 v1.02 起它已**不再是文件名密文的编码**，只剩两个用途：
//  1. 解 v1.01 及更早版本、以及 Python 端写下的老密文名（向后兼容）；
//  2. 恢复码（RecoveryCodeLen 个字符）——那是对用户展示的短码，格式不动。
var b32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// b64NoPad 是去 = 填充的 Base64URL 编解码器（字母表 A-Za-z0-9-_）。
//
// 文件名密文自 v1.02 起用它，理由见 protocol.Base64URLAlphabet 的注释：
// 同样只用文件名安全字符，但相对 Base32 少膨胀 27 个百分点。
var b64NoPad = base64.RawURLEncoding

var (
	// ErrFilenameTooShort 表示解码后的字节数装不下 nonce + 标签。
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
// 对照参考实现 filename.py:25-35
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
// 对照参考实现 filename.py:38-59
func B32DecodeNoPad(s string) ([]byte, error) {
	return b32NoPad.DecodeString(strings.ToUpper(s))
}

// B64EncodeNoPad 做 Base64URL 编码并去除 = 填充。
//
// 输出只用 `A-Za-z0-9-_`：没有 `+`、`/`（会被当路径分隔符或 URL 特殊字符）、
// 没有 `=`（部分网盘视为非法字符并触发重命名）。
//
// ⚠️ Base64 大小写敏感，这正是 Base32 当初被选中的理由（老密文名即使被
// 同步工具转成小写也还能解）。换成 Base64URL 后，**密文名一旦被人为改写
// 大小写就无法还原** —— 这是换取「短 27%」付出的代价，已在 v1.02 与用户
// 确认。解码端仍保留 Base32 回退，故老名字不受影响。
func B64EncodeNoPad(data []byte) string { return b64NoPad.EncodeToString(data) }

// B64DecodeNoPad 解码去填充的 Base64URL 字符串（不做大小写归一）。
func B64DecodeNoPad(s string) ([]byte, error) { return b64NoPad.DecodeString(s) }

// ErrFilenameEncoding 表示两种候选编码都解码失败（输入里既有非 Base64URL
// 字符、也过不了 Base32 字母表），通常意味着文件名被人为改写过。
var ErrFilenameEncoding = errors.New("文件名密文编码无法识别")

// EncryptFilename 加密文件名，输出 Base64URL(nonce12 ‖ ct ‖ tag16) 去 = 填充。
//
// 最终云端文件名 = 本函数输出 + protocol.FileExtension。
//
// nonce 每次随机生成，因此同一文件名两次加密的结果必然不同 —— 这是预期行为
// （否则同名文件在云端会暴露「内容相同」的信息）。定值对拍请用
// encryptFilenameWithNonce。
//
// 明文超过 protocol.FilenameMaxPlainBytes 时按 rune 安全截断（只截尾巴）：
// 云端与 Windows 的单段文件名上限是 255 字节，不设上限的长名字（真实存在的
// 中文长标题视频）会让密文名越限而被云端拒绝。截断是 v1.02 与用户确认的取舍。
//
// ⚠️ 用的是 12 字节 nonce（NewGCM12），与 Vault Marker 的 16 字节不可混用。
//
// 对照参考实现 filename.py:71-86（nonce 拼接顺序与 GCM 细节一致，
// 仅外层编码由 Base32 换成 Base64URL）
func EncryptFilename(plain string, key []byte) (string, error) {
	raw, err := encryptFilenameRaw(plain, key, nil)
	if err != nil {
		return "", err
	}
	return B64EncodeNoPad(raw), nil
}

// EncryptDirName 加密**目录名**，与 EncryptFilename 的唯一差别是
// nonce 由「密钥 + 明文」确定性派生（HMAC-SHA256 的定长截断）。
//
// 为什么目录必须确定性而文件不必：
//
//	同一逻辑目录在一次上传里会被加密多次——Mkdir 建目录一次，每个待传文件的
//	父目录段各一次。随机 nonce 下这些密文互不相同，于是「上传一个文件夹」
//	会在云端炸出多个解密后同名、内容却分散的目录（v1.01 及更早的真实缺陷）。
//	确定性映射让「一个逻辑目录 ↔ 一个密文名」成立，Mkdir 真正幂等，
//	重传/同步也会落回同一棵树而不是另起一棵。
//
// 安全性：nonce 是明文的确定函数，所以同名目录恒得同一密文（泄露「两个目录
// 同名」这一本就由目录列表暴露的信息）；不同明文撞 nonce 的概率是 2^-96，
// 与随机 nonce 的生日界同量级。
func EncryptDirName(plain string, key []byte) (string, error) {
	raw, err := encryptFilenameRaw(plain, key, deterministicDirNonce(plain, key))
	if err != nil {
		return "", err
	}
	return B64EncodeNoPad(raw), nil
}

// dirNonceDomain 是目录名 nonce 的派生域分隔符。
// 独立前缀保证目录名 nonce 与任何别的 HMAC 用途（将来若有）不会撞到同一串。
const dirNonceDomain = "cloudprism.dirname.v1\x00"

// deterministicDirNonce 由密钥与明文推出目录名的固定 nonce（12 字节）。
func deterministicDirNonce(plain string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(dirNonceDomain))
	mac.Write([]byte(plain))
	return mac.Sum(nil)[:protocol.FilenameNonceLen]
}

// encryptFilenameRaw 产出 nonce ‖ 密文 ‖ 标签 的原始字节（未编码）。
//
// nonce 为 nil 时随机生成；否则用调用方给定的 nonce（测试定值对拍、
// 或 EncryptDirName 的确定性派生）。
func encryptFilenameRaw(plain string, key []byte, nonce []byte) ([]byte, error) {
	if nonce == nil {
		nonce = make([]byte, protocol.FilenameNonceLen)
		if _, err := rand.Read(nonce); err != nil {
			return nil, fmt.Errorf("生成文件名 nonce 失败: %w", err)
		}
	}

	gcm, err := NewGCM12(key)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, len(nonce)+len(plain)+protocol.GCMTagLen)
	out = append(out, nonce...)
	// Seal 追加的是「密文 ‖ 标签」，与 Python 的 nonce + ct + tag 拼接顺序逐字节一致
	return gcm.Seal(out, nonce, []byte(truncateFilename(plain)), nil), nil
}

// encryptFilenameWithNonce 用调用方给定的 nonce 加密并编码，
// 仅供黄金向量测试与 Python 侧输出对拍（测试与被测代码同包，故不导出）。
func encryptFilenameWithNonce(plain string, key []byte, nonce []byte) (string, error) {
	raw, err := encryptFilenameRaw(plain, key, nonce)
	if err != nil {
		return "", err
	}
	return B64EncodeNoPad(raw), nil
}

// truncateFilename 把超长明文按 protocol.FilenameMaxPlainBytes 截断。
//
// 按 rune 边界收缩（不产出半个字符）：云端文件名是字节还是字符计数的实现
// 各有不同，但截在字符边界上两边都合法。输入本身不是合法 UTF-8 时无法谈
// 「字符边界」（Python 侧也可能手工加密过任意字节），此时按字节直接切。
func truncateFilename(plain string) string {
	max := protocol.FilenameMaxPlainBytes
	if len(plain) <= max {
		return plain
	}
	if !utf8.ValidString(plain) {
		return plain[:max]
	}
	b := []byte(plain)[:max]
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1] // 退掉被切断的尾字符
	}
	return string(b)
}

// DecryptFilename 解密文件名。
//
// 编码按「先 Base64URL（v1.02 起的格式）、后 Base32（老格式）」两轮尝试：
// 两者的字母表是包含关系（Base32 ⊂ Base64URL），单看字符串分不出格式，
// 由 GCM 标签来裁决——错误格式必然过不了认证。
//
// 两轮都失败时，错误以 Base32 那轮为准：能被 Base32 解码的输入几乎必然是
// 老名字（新名字含 `-`/`_`/小写字母，过不了 Base32 字母表），此时 Base32
// 给出的「长度不足 / 认证失败 / 非 UTF-8」才描述真实病因；反过来若只有
// Base64URL 能解码，就报那一轮的错。上层（decryptName）对失败一视同仁
// ——回退展示密文名，所以这只影响日志与诊断精度。
//
// 三类失败用不同 error 区分，但上层通常都当作「该条目无法显示原名」处理：
// 目录树遇到解不开的名字时应回退展示密文名，而不是让整个列表加载失败。
//
// 对照参考实现 filename.py:88-108
func DecryptFilename(encoded string, key []byte) (string, error) {
	plain, b64Decoded, b64Err := decryptFilenameWith(encoded, key, B64DecodeNoPad)
	if b64Err == nil {
		return plain, nil
	}
	plain, b32Decoded, b32Err := decryptFilenameWith(encoded, key, B32DecodeNoPad)
	if b32Err == nil {
		return plain, nil
	}
	if b32Decoded {
		return "", b32Err
	}
	if b64Decoded {
		return "", b64Err
	}
	return "", b64Err
}

// decryptFilenameWith 用指定的编码解码器走一遍解密。
//
// 第二个返回值表示「编码解码成功」——即失败发生在解码之后（长度或 GCM），
// 调用方据此判断这一轮是否值得作为最终错误上报。
func decryptFilenameWith(
	encoded string, key []byte, decode func(string) ([]byte, error),
) (plain string, decoded bool, err error) {
	raw, err := decode(encoded)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrFilenameEncoding, err)
	}
	if len(raw) < protocol.FilenameNonceLen+protocol.GCMTagLen {
		return "", true, ErrFilenameTooShort
	}

	nonce := raw[:protocol.FilenameNonceLen]
	body := raw[protocol.FilenameNonceLen:] // 密文 ‖ 标签

	gcm, err := NewGCM12(key)
	if err != nil {
		return "", true, err
	}
	plainBytes, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", true, ErrFilenameAuth
	}
	// Python 侧的 .decode("utf-8") 会在非法字节上抛 UnicodeDecodeError，
	// Go 的 string() 转换则不做校验，故显式补一次以对齐语义。
	if !utf8.Valid(plainBytes) {
		return "", true, ErrFilenameUTF8
	}
	return string(plainBytes), true, nil
}
