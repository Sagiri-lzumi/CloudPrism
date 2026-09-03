package cryptox

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// Vault Marker（.cloudprism_vault）用于识别云盘是否被本系统加密，
// 并作为用户主密码的校验入口。文件由「明文前缀」与「AES-256-GCM 加密载荷」
// 两段组成，v3 起载荷之后还可追加恢复码块。
//
// 明文前缀（68 字节，全大端）：
//
//	偏移  长度  字段
//	0     12    Magic        "CPRISM_VAULT"
//	12    4     Version      uint32 BE
//	16    16    VaultID      UUID
//	32    16    Salt         PBKDF2 盐（明文，非密钥）
//	48    16    IV           GCM nonce（16 字节，两端均不截断）
//	64    4     PayloadLen   uint32 BE，加密载荷（密文 + 标签）总长
//
// 加密载荷（偏移 68，长度 = PayloadLen）：AES-256-GCM(内部明文) ‖ Tag[16]
//
// 文件尾部（v3，可选，紧随载荷）：
//
//	recoveryLen   uint16 BE，恢复块字节长（可为 0，无块时直接缺省）
//	recoveryBlob  rsalt(16) ‖ AES-GCM(rkey, 主密码 UTF-8) ‖ Tag[16]
//	              rkey = PBKDF2(恢复码随机密钥, rsalt)，GCM nonce 复用 rsalt
//
// 内部明文（v2/v3；v1 无名称字段，到 VerifyMagic 即结束）：
//
//	偏移  长度  字段
//	0     1     FilenameEncryptionFlag   0x00/0x01
//	1     4     ProtocolVersion          uint32 BE
//	5     8     Reserved                 恒为全 0
//	13    4     VerifyMagic              "CPV\x00"
//	17    2     NameLen                  uint16 BE，名称 UTF-8 字节长（可 0）
//	19    N     Name                     UTF-8 编码的密库名称
//
// ⚠️ 上表偏移以 vault.py:99-106 的**实际拼装顺序**为唯一真源。
// vault.py 的 docstring 曾把 VerifyMagic 写成偏移 9（其后 NameLen@17、Name@19
// 又与代码一致，可见 docstring 自身就矛盾）。照 docstring 实现会让 GCM 标签
// 必然校验失败，症状是「所有现存密库都打不开，且伪装成主密码错误」，
// 排查极其困难。
//
// 密钥由主密码 + Salt 派生；GCM 认证标签即密码校验器 ——
// 标签通过 = 密码正确，失败 = 密码错误。
//
// 对照 WindowsPy/src/cloudprism/crypto/vault.py:1-40
const (
	// vaultPrefixLen 是明文前缀长度：12 + 4 + 16 + 16 + 16 + 4 = 68。
	vaultPrefixLen = 68

	// vaultInnerNameOffset 是内部明文中 NameLen 字段的偏移：1 + 4 + 8 + 4 = 17。
	vaultInnerNameOffset = 17

	// vaultInnerHeadLen 是内部明文除 Name 之外的固定长度：17 + 2 = 19。
	vaultInnerHeadLen = vaultInnerNameOffset + 2

	// vaultPayloadPos 是 PayloadLen 字段在前缀中的偏移。
	vaultPayloadPos = 64
)

var (
	// ErrVaultMagic 表示魔数不符，目标不是 Vault Marker。
	ErrVaultMagic = errors.New("Vault Marker 魔数不匹配")

	// ErrVaultShort 表示字节数不足以容纳声明的布局：文件被截断，
	// 或 PayloadLen 与实际长度矛盾。
	ErrVaultShort = errors.New("Vault Marker 数据不足")

	// ErrVaultPassword 表示 GCM 标签校验失败 —— **主密码错误**。
	// 上层通常把它直接呈现为「主密码错误」，与 Python 端 verify 返回 None 后的处理一致。
	ErrVaultPassword = errors.New("主密码错误")

	// ErrVaultField 表示构造 Marker 时字段长度不合法。
	ErrVaultField = errors.New("Vault Marker 字段长度不合法")

	// ErrRecoverySecret 表示恢复块 GCM 标签校验失败 —— 恢复码错误。
	ErrRecoverySecret = errors.New("恢复码错误")

	// ErrRecoveryFormat 表示恢复块布局异常，或解出的主密码不是合法 UTF-8。
	ErrRecoveryFormat = errors.New("恢复块格式异常")
)

// VaultMetadata 是 Vault Marker 的明文前缀字段 + 内部明文中的配置。
//
// 对照 WindowsPy/src/cloudprism/crypto/vault.py:55-66
type VaultMetadata struct {
	Version         uint32 // Marker 格式版本（1 / 2 / 3）
	VaultID         []byte // 16 字节 UUID
	Salt            []byte // 16 字节 KDF 盐
	IV              []byte // 16 字节 GCM nonce
	FilenameEnc     bool   // 文件名加密开关，初始化后不可变更
	ProtocolVersion uint32 // 协议版本，与 .cpenc 的 protocol.Version 一致
	Name            string // 用户自定义密库名称（v1 旧文件为空）
	HasRecovery     bool   // 是否携带恢复码块（v3 尾部；旧文件为 false）
}

// CreateMarker 创建 Vault Marker 文件的完整字节序列（明文前缀 + GCM 载荷 [+ 恢复块]）。
//
// recoveryBlob 非空时以「recoveryLen + blob」追加到尾部（v3）；
// 传 nil 或空切片则一个字节都不追加，与 Python 侧 `if recovery_blob:` 的判断一致。
//
// 对照 WindowsPy/src/cloudprism/crypto/vault.py:75-125
func CreateMarker(meta VaultMetadata, masterPassword string, recoveryBlob []byte) ([]byte, error) {
	if err := meta.validate(); err != nil {
		return nil, err
	}
	if len(recoveryBlob) > 0xFFFF {
		return nil, fmt.Errorf("%w: 恢复块长度 %d 超出 uint16 上限", ErrVaultField, len(recoveryBlob))
	}

	key := DeriveKey(masterPassword, meta.Salt)

	// 名称截断防御：先按【字符数】截到上限，再 UTF-8 编码。
	// Python 的 meta.name[:32] 是字符切片；Go 若按字节截会切坏多字节字符，
	// 两端产出的 Marker 将不再逐字节相等（全中文名称下差 3 倍）。
	nameBytes := []byte(truncateRunes(meta.Name, protocol.VaultNameMaxLen))

	// 内部明文：标志 + 协议版本 + 保留 + 校验魔数 + 名称字段
	inner := make([]byte, 0, vaultInnerHeadLen+len(nameBytes))
	if meta.FilenameEnc {
		inner = append(inner, 0x01)
	} else {
		inner = append(inner, 0x00)
	}
	inner = binary.BigEndian.AppendUint32(inner, meta.ProtocolVersion)
	var reserved [protocol.VaultReservedLen]byte // Reserved 字段恒为全 0
	inner = append(inner, reserved[:]...)
	inner = append(inner, protocol.VaultVerifyMagic...)
	inner = binary.BigEndian.AppendUint16(inner, uint16(len(nameBytes)))
	inner = append(inner, nameBytes...)

	// AES-256-GCM 加密，**16 字节 nonce**（直接用 Marker 的 IV，两端均不截断）
	gcm, err := NewGCM16(key[:])
	if err != nil {
		return nil, err
	}
	payload := gcm.Seal(nil, meta.IV, inner, nil) // 密文 ‖ 标签

	size := vaultPrefixLen + len(payload)
	if len(recoveryBlob) > 0 {
		size += 2 + len(recoveryBlob)
	}
	data := make([]byte, size)

	copy(data[0:12], protocol.VaultMagic)
	binary.BigEndian.PutUint32(data[12:16], meta.Version)
	copy(data[16:32], meta.VaultID)
	copy(data[32:48], meta.Salt)
	copy(data[48:64], meta.IV)
	binary.BigEndian.PutUint32(data[vaultPayloadPos:vaultPrefixLen], uint32(len(payload)))
	copy(data[vaultPrefixLen:], payload)

	if len(recoveryBlob) > 0 {
		tail := vaultPrefixLen + len(payload)
		binary.BigEndian.PutUint16(data[tail:tail+2], uint16(len(recoveryBlob)))
		copy(data[tail+2:], recoveryBlob)
	}
	return data, nil
}

// VerifyMarker 校验主密码并解析 Vault Metadata。
//
// 对应 Python 侧 verify 返回 None 的三种情形，Go 用 error 区分原因：
//   - ErrVaultMagic    魔数不符（不是 Marker 文件）
//   - ErrVaultShort    字节不足，或 PayloadLen 与实际长度矛盾
//   - ErrVaultPassword GCM 标签校验失败 = **主密码错误**
//
// 上层（vault manager / bind 层）通常把三者统一呈现为「主密码错误」以对齐
// WindowsPy 的行为；区分它们只是为了让日志可诊断。
//
// 返回的 VaultID / Salt / IV 都是新分配的副本，不别名入参：调用方传进来的
// 很可能是池化缓冲或马上要被复用的下载缓冲，持有子切片会读到脏数据。
//
// 对照 WindowsPy/src/cloudprism/crypto/vault.py:127-220
func VerifyMarker(fileBytes []byte, masterPassword string) (*VaultMetadata, error) {
	if len(fileBytes) < vaultPrefixLen {
		return nil, ErrVaultShort
	}
	if string(fileBytes[:protocol.VaultMagicLen]) != protocol.VaultMagic {
		return nil, ErrVaultMagic
	}

	version := binary.BigEndian.Uint32(fileBytes[12:16])
	vaultID := fileBytes[16:32]
	salt := fileBytes[32:48]
	iv := fileBytes[48:64]
	payloadLen := int(binary.BigEndian.Uint32(fileBytes[vaultPayloadPos:vaultPrefixLen]))

	// PayloadLen 取自文件本身，必须先与真实长度核对再切片：
	// 被篡改成 4GiB 量级的取值会让下一行的切片直接 panic。
	// Python 侧靠 BytesIO.read 读不满返回 None 天然免疫，Go 必须显式拦住。
	if payloadLen < protocol.GCMTagLen {
		return nil, ErrVaultShort
	}
	if len(fileBytes)-vaultPrefixLen < payloadLen {
		return nil, ErrVaultShort
	}
	payload := fileBytes[vaultPrefixLen : vaultPrefixLen+payloadLen]

	// v3 尾部恢复码块容错探测：仅需 PayloadLen 即可定位，无需解密载荷；
	// 无尾部或布局异常的 v1/v2 文件 HasRecovery = false。
	hasRecovery := false
	if tail := fileBytes[vaultPrefixLen+payloadLen:]; len(tail) >= 2 {
		recoveryLen := int(binary.BigEndian.Uint16(tail[:2]))
		hasRecovery = recoveryLen > 0 && len(tail)-2 >= recoveryLen
	}

	key := DeriveKey(masterPassword, salt)
	gcm, err := NewGCM16(key[:])
	if err != nil {
		return nil, err
	}
	inner, err := gcm.Open(nil, iv, payload, nil)
	if err != nil {
		return nil, ErrVaultPassword // GCM 标签校验失败 = 主密码错误
	}

	// 内部明文至少要装得下 flag(1) + ProtocolVersion(4)。
	// Python 侧此处直接下标访问 inner[0] 与 inner[1:5]，载荷过短会抛未捕获的
	// IndexError / struct.error；Go 折叠为格式错误。可观测行为不变：
	// 两端都不会在载荷畸形时返回一个「成功」的 metadata。
	if len(inner) < 5 {
		return nil, ErrVaultShort
	}

	meta := &VaultMetadata{
		Version:         version,
		VaultID:         bytes.Clone(vaultID),
		Salt:            bytes.Clone(salt),
		IV:              bytes.Clone(iv),
		FilenameEnc:     inner[0] == 0x01,
		ProtocolVersion: binary.BigEndian.Uint32(inner[1:5]),
		HasRecovery:     hasRecovery,
	}

	// 名称字段容错解析：v1 旧文件没有剩余字节 → Name 为空；
	// NameLen 越界或字节不足等异常布局同样回退空名称，**不阻断校验**。
	//
	// ⚠️ Go 的 inner[17:] 在 len(inner) < 17 时会 panic，而 Python 的切片越界
	// 只会得到空串，因此必须先判长度再切。
	if len(inner) > vaultInnerNameOffset {
		tail := inner[vaultInnerNameOffset:]
		if len(tail) >= 2 {
			nameLen := int(binary.BigEndian.Uint16(tail[:2]))
			if len(tail)-2 >= nameLen {
				// 解码失败（非法 UTF-8）回退空名称，同样不阻断校验
				if raw := tail[2 : 2+nameLen]; utf8.Valid(raw) {
					meta.Name = string(raw)
				}
			}
		}
	}
	return meta, nil
}

// BuildRecoveryBlob 构造恢复码块：rsalt(16) ‖ AES-GCM(rkey, 主密码 UTF-8) ‖ Tag[16]。
//
// rkey = PBKDF2(恢复码随机密钥, rsalt)，GCM nonce **复用 rsalt**（16 字节）。
// rsalt 每次随机生成，因此不存在 nonce 重用风险。
//
// 用户离线保存的「恢复码」就是 recoverySecret 的 Base32 形式（16 字符），
// 凭它可在忘记主密码时解出主密码；服务端零参与。
//
// 对照 WindowsPy/src/cloudprism/crypto/vault.py:226-239
func BuildRecoveryBlob(recoverySecret []byte, masterPassword string) ([]byte, error) {
	var rsalt [protocol.SaltLen]byte
	if _, err := rand.Read(rsalt[:]); err != nil {
		return nil, fmt.Errorf("生成恢复块盐失败: %w", err)
	}
	return buildRecoveryBlobWithSalt(recoverySecret, masterPassword, rsalt[:])
}

// buildRecoveryBlobWithSalt 用调用方给定的 rsalt 构造恢复块，
// 仅供黄金向量测试与 Python 侧输出对拍（测试与被测代码同包，故不导出）。
func buildRecoveryBlobWithSalt(recoverySecret []byte, masterPassword string, rsalt []byte) ([]byte, error) {
	rkey := DeriveKeyRaw(recoverySecret, rsalt)
	gcm, err := NewGCM16(rkey[:]) // 恢复块同样是 16 字节 nonce
	if err != nil {
		return nil, err
	}

	blob := make([]byte, 0, len(rsalt)+len(masterPassword)+protocol.GCMTagLen)
	blob = append(blob, rsalt...)
	return gcm.Seal(blob, rsalt, []byte(masterPassword), nil), nil
}

// DecryptRecoveryBlob 凭恢复码随机密钥解密恢复块，还原主密码。
//
// 对应 Python 侧返回 None 的三种情况，Go 用两类 error 表达：
// ErrRecoveryFormat（长度不足 / 明文非 UTF-8）与 ErrRecoverySecret（GCM 标签失败）。
// 界面上两者都应显示为「恢复码无效」，不透露具体原因。
//
// 对照 WindowsPy/src/cloudprism/crypto/vault.py:241-265
func DecryptRecoveryBlob(blob, recoverySecret []byte) (string, error) {
	// 最短合法布局：rsalt(16) + 至少 1 字节密文 + Tag(16) = 33
	if len(blob) < protocol.SaltLen+protocol.GCMTagLen+1 {
		return "", ErrRecoveryFormat
	}
	rsalt := blob[:protocol.SaltLen]
	body := blob[protocol.SaltLen:] // 密文 ‖ 标签

	rkey := DeriveKeyRaw(recoverySecret, rsalt)
	gcm, err := NewGCM16(rkey[:])
	if err != nil {
		return "", err
	}
	password, err := gcm.Open(nil, rsalt, body, nil)
	if err != nil {
		return "", ErrRecoverySecret
	}
	if !utf8.Valid(password) {
		return "", ErrRecoveryFormat
	}
	return string(password), nil
}

// SplitRecoveryTail 把 Marker 拆成（主体, 恢复块尾部）。
//
// 尾部 = recoveryLen(2) + recoveryBlob；无尾部时返回 (原文件, nil)。
//
// 供重命名等整体重写场景**原样保留**既有恢复块：重写 Marker 只改内部明文里的
// 名称，恢复块必须字节不动地搬过去，否则用户手里的恢复码会立即失效。
//
// 布局异常（长度不足 / PayloadLen 越界）时返回 (原文件, nil)，与 Python 一致 ——
// 宁可不动，也不要产出一个被截断的 Marker。
//
// 对照 WindowsPy/src/cloudprism/crypto/vault.py:267-286
func SplitRecoveryTail(fileBytes []byte) (head, tail []byte) {
	// 前缀本身已含 4 字节 PayloadLen，这里再多要 4 字节是照抄 Python 的
	// `len < prefix_len + 4` 守卫；对真实 Marker（载荷至少 16 字节）永不触发，
	// 只影响畸形输入的返回值，保持逐分支一致以免两端行为分叉。
	if len(fileBytes) < vaultPrefixLen+4 {
		return fileBytes, nil
	}
	payloadLen := int(binary.BigEndian.Uint32(fileBytes[vaultPayloadPos:vaultPrefixLen]))
	headEnd := vaultPrefixLen + payloadLen
	if headEnd > len(fileBytes) {
		return fileBytes, nil
	}
	return fileBytes[:headEnd], fileBytes[headEnd:]
}

// GenerateMetadata 生成一份全新的 VaultMetadata，VaultID / Salt / IV 均由 CSPRNG 填充。
//
// 供初始化向导调用。Python 侧用 uuid.uuid4().bytes 生成 VaultID，Go 侧直接取
// 16 字节随机数：VaultID 在两端都只作为不透明标识符存储与回读，从不解析 UUID
// 的版本位与变体位，因此不必刻意构造 v4 格式。
//
// 对照 WindowsPy/src/cloudprism/crypto/vault.py:288-312
func GenerateMetadata(filenameEnc bool, name string) (VaultMetadata, error) {
	meta := VaultMetadata{
		Version:         protocol.VaultVersion,
		VaultID:         make([]byte, protocol.VaultIDLen),
		Salt:            make([]byte, protocol.SaltLen),
		IV:              make([]byte, protocol.IVLen),
		FilenameEnc:     filenameEnc,
		ProtocolVersion: protocol.Version,
		Name:            name,
	}
	for _, dst := range [][]byte{meta.VaultID, meta.Salt, meta.IV} {
		if _, err := rand.Read(dst); err != nil {
			return VaultMetadata{}, fmt.Errorf("生成 Vault 随机字段失败: %w", err)
		}
	}
	return meta, nil
}

// validate 检查构造 Marker 所需的定长字段。
//
// Python 侧不做这些检查（靠 struct.pack 与切片拼接自然成型），但 Go 的 copy
// 会静默截断：长度不符就会拼出一个布局错位的 Marker —— 那是种「能写出去、
// 却永远打不开」的坏文件，必须在构造前拦住。
func (m VaultMetadata) validate() error {
	switch {
	case len(m.VaultID) != protocol.VaultIDLen:
		return fmt.Errorf("%w: VaultID 应为 %d 字节，实为 %d",
			ErrVaultField, protocol.VaultIDLen, len(m.VaultID))
	case len(m.Salt) != protocol.SaltLen:
		return fmt.Errorf("%w: Salt 应为 %d 字节，实为 %d",
			ErrVaultField, protocol.SaltLen, len(m.Salt))
	case len(m.IV) != protocol.IVLen:
		return fmt.Errorf("%w: IV 应为 %d 字节，实为 %d",
			ErrVaultField, protocol.IVLen, len(m.IV))
	}
	return nil
}

// truncateRunes 按【字符数】截断字符串，最多保留 max 个 rune。
//
// 对应 Python 的 s[:max] —— 那是字符切片而不是字节切片。
// 对全中文名称（每字 3 字节）而言两者相差 3 倍；按字节截会切出非法 UTF-8，
// 既破坏两端 Marker 的逐字节一致性，也会让名称在界面上显示成乱码。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	n := 0
	for i := range s { // i 是每个 rune 的字节起始偏移
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}
