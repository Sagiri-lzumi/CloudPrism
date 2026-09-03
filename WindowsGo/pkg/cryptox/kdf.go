package cryptox

import (
	"crypto/pbkdf2"
	"crypto/sha256"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// DeriveKey 从主密码 + 盐派生 32 字节 AES-256 密钥。
//
// 主密码统一按 UTF-8 编码后参与派生，保证 WindowsPy 与 Go 端字节级一致；
// 派生出的密钥仅存在于内存中，不落盘、不上传云端，调用方负责用后清零。
//
// 对照 WindowsPy/src/cloudprism/crypto/kdf.py:34-46
func DeriveKey(masterPassword string, salt []byte) [protocol.KeyLen]byte {
	return DeriveKeyRaw([]byte(masterPassword), salt)
}

// DeriveKeyRaw 从字节口令 + 盐派生 32 字节密钥，PBKDF2 参数与 DeriveKey 完全一致。
//
// 供非字符串密钥材料复用同一套参数 —— 目前唯一调用方是恢复码块：
// rkey = PBKDF2(恢复码随机密钥, rsalt)（见 vault.py:236）。
//
// 对照 WindowsPy/src/cloudprism/crypto/kdf.py:48-63
func DeriveKeyRaw(password, salt []byte) [protocol.KeyLen]byte {
	var key [protocol.KeyLen]byte

	// 使用 Go 1.24+ 标准库 crypto/pbkdf2 而非 golang.org/x/crypto/pbkdf2：
	// 零第三方依赖、由 FIPS 140 实现背书，且 HMAC-SHA256 走汇编加速。
	// 显式传 sha256.New，避免任何「默认 PRF」差异（Python 侧同样显式指定）。
	dk, err := pbkdf2.Key(sha256.New, string(password), salt, protocol.KDFIterations, protocol.KeyLen)
	if err != nil {
		// 迭代次数与密钥长度都是编译期常量，标准 PBKDF2 在此参数下不可能失败。
		// 唯一的失败路径是 GOFIPS140 模式下口令含 NUL 字节或盐短于 16 字节，
		// 二者都属于调用方违反协议契约（盐恒为 16 字节、口令为 UTF-8 文本），
		// 因此按编程错误处理，而不是给每个调用点都加一条不可达的错误分支。
		panic("cryptox: PBKDF2 参数非法: " + err.Error())
	}

	copy(key[:], dk)
	return key
}
