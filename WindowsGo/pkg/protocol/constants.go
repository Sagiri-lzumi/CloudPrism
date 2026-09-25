// Package protocol 集中定义 CloudPrism 加密容器的二进制格式常量。
//
// 本包是 Go 端的最内层：不 import 任何项目内其它包，只依赖标准库，
// 因而可独立编译与单测。参考实现与 Go 端必须使用完全一致的取值，
// 任何常量改动都会破坏跨端互操作性，修改前必须同步两端并升级版本号。
//
// 每条常量都标注了 Python 侧真源行号（参考实现 constants.py）。
// 真源以代码为准、不以文档反推：vault.py 的 docstring 曾把 VerifyMagic 的偏移
// 写成 9（实际拼装为 13），照文档实现会导致所有现存密库打不开且症状伪装成
// 「主密码错误」。
package protocol

// ---------------------------------------------------------------------------
// 加密文件头（.cpenc）
// ---------------------------------------------------------------------------

const (
	// Magic 是文件头魔数：8 字节，标识 CloudPrism 加密容器；
	// 末两字节 \x00\x01 为版本占位，便于未来区分子格式。
	// 对照 constants.py:15
	Magic = "CPRISM\x00\x01"

	// MagicLen 是 Magic 的字节长度。
	MagicLen = len(Magic)

	// Version 是文件格式版本号（uint32，大端序），当前为 1。
	// 对照 constants.py:18
	Version uint32 = 1

	// SaltLen 是 KDF 盐值长度（字节）；文件头与 Vault Marker 统一使用 16 字节盐。
	// 对照 constants.py:21
	SaltLen = 16

	// IVLen 是 AES-CTR 流加密初始向量长度（字节）；作为 128-bit 大端计数器初值。
	// 对照 constants.py:24
	IVLen = 16

	// BlockSize 是 AES 块大小（字节）；CTR 模式按此分块生成密钥流。
	// 对照 constants.py:27
	BlockSize = 16
)

// ---------------------------------------------------------------------------
// 密钥派生函数（KDF）
// ---------------------------------------------------------------------------

const (
	// KDFIterations 是 PBKDF2-HMAC-SHA256 迭代次数；两端必须一致，
	// 200000 兼顾安全与性能。
	// 对照 constants.py:34
	KDFIterations = 200_000

	// KeyLen 是派生密钥长度（字节）；32 字节对应 AES-256。
	// 对照 constants.py:37
	KeyLen = 32

	// KDFAlgo 是 KDF 算法标识，仅作记录与日志用。
	// 对照 constants.py:40
	KDFAlgo = "PBKDF2-HMAC-SHA256"
)

// ---------------------------------------------------------------------------
// Vault Marker（.cloudprism_vault）
// ---------------------------------------------------------------------------

const (
	// VaultMagic 是 Vault Marker 魔数：12 字节，标识密库标识文件。
	// 对照 constants.py:47
	VaultMagic = "CPRISM_VAULT"

	// VaultMagicLen 是 VaultMagic 的字节长度。
	VaultMagicLen = len(VaultMagic)

	// VaultMarkerName 是 Vault Marker 文件名；固定存放于云盘根目录，
	// 隐藏文件名减少误删。
	// 对照 constants.py:50
	VaultMarkerName = ".cloudprism_vault"

	// SyncIndexName 是增量同步索引文件名；存放于密库根（或子目录密库位置），
	// 内容为整体加密的 JSON（{相对路径: {size, mtime, uploaded_at}}）。
	// 对照 constants.py:54
	SyncIndexName = ".cloudprism_index"

	// VaultVersion 是 Vault Marker 格式版本号（uint32，大端序），当前为 3。
	//   - v2：内部明文尾部追加用户自定义密库名称（name_len + name_utf8），
	//     解析端对无名称字段的 v1 文件保持兼容（name 置空）
	//   - v3：文件尾部（GCM 载荷之后）追加恢复码块（recovery_len + recovery_blob），
	//     解析端按 PayloadLen 读载荷，无恢复块的 v1/v2 文件不受影响（容错解析）
	//
	// 对照 constants.py:56-61
	VaultVersion uint32 = 3

	// VaultIDLen 是 Vault Marker 中 Vault ID（UUID）的字节长度。
	// Python 侧未定义常量、直接写字面量 16（vault.py:156），此处补齐以消除魔数。
	VaultIDLen = 16

	// VaultNameMaxLen 是密库名称上限（**字符数**，不是字节数）；
	// 随 Vault Marker 加密保存，跨设备跟随密库。
	//
	// ⚠️ Python 侧 vault.py:96 是 meta.name[:32] 的**字符**切片，之后才 UTF-8 编码；
	// Go 端必须同样按 rune 截断（见 cryptox.truncateRunes），按字节截会切坏多字节字符，
	// 两端产出的 Marker 将不再逐字节相等。
	// 对照 constants.py:64
	VaultNameMaxLen = 32

	// VaultVerifyMagic 是 Vault Marker 内部明文中的校验魔数；
	// 调试用，GCM 标签已提供完整性校验。
	// 对照 constants.py:67
	VaultVerifyMagic = "CPV\x00"

	// VaultReservedLen 是 Vault Marker 内部明文中 Reserved 字段的长度（字节）。
	// 对照 constants.py:70
	VaultReservedLen = 8

	// GCMTagLen 是 AES-GCM 认证标签长度（字节）。
	// 对照 constants.py:73
	GCMTagLen = 16

	// RecoveryCodeLen 是恢复码长度（Base32 字符数）；16 字符对应 10 字节随机密钥，
	// 界面按 XXXX-XXXX-XXXX-XXXX 分组展示；恢复码仅离线保存，服务端零参与。
	// 对照 constants.py:77
	RecoveryCodeLen = 16

	// RecoverySecretLen 是恢复码对应的随机密钥字节数（16 个 Base32 字符 = 10 字节）。
	// 对照 constants.py:80
	RecoverySecretLen = 10

	// FilenameNonceLen 是文件名加密用的 AES-GCM nonce 长度（字节）。
	//
	// ⚠️ 与 Vault Marker / 恢复块使用的 16 字节 nonce 不同（vault.py:109 直接把
	// 16 字节 IV 当 nonce）。这是协议中最易踩的分叉：混用两种长度会让 GCM 标签
	// 校验必然失败，症状是「所有密库都打不开且报密码错误」。
	// 详见 cryptox.NewGCM12 与 cryptox.NewGCM16。
	// 对照 constants.py:83
	FilenameNonceLen = 12
)

// ---------------------------------------------------------------------------
// 文件名与后端
// ---------------------------------------------------------------------------

const (
	// FileExtension 是加密文件统一扩展名。
	// 对照 constants.py:90
	FileExtension = ".cpenc"

	// Base32Alphabet 是 Base32 编码字母表（RFC 4648），不含填充符 =。
	//
	// 用途已收窄到**恢复码**（RecoveryCodeLen 个字符，形如 XXXX-XXXX-…）；
	// 文件名密文自 v1.02 起改用 Base64URL（见 Base64URLAlphabet），
	// 解码端仍兼容 Base32 老名字。
	// 对照 constants.py:93
	Base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

	// Base64URLAlphabet 是文件名密文自 v1.02 起使用的 Base64URL 字母表，
	// 不含填充符 =（RawURLEncoding → 末尾以 -/_ 收束）。
	//
	// 为什么从 Base32 换过来：Base32 每 5 字节膨胀成 8 字符（+60%），
	// 一份 40 字中文名的密文可达 240+ 字符，逼近甚至越过云盘与 Windows
	// 单段文件名的 255 字节上限；Base64URL 只膨胀 +33%，同样只用
	// `A-Za-z0-9-_` 这些文件名安全字符（无 +、/、=）。
	Base64URLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

	// FilenameMaxPlainBytes 是文件名**明文**的字节上限，超出按 rune 安全截断
	// （见 cryptox.truncateRunes / cryptox.EncryptFilename）。
	//
	// 上限由云端的硬约束倒推：密文名 = Base64URL(nonce12 ‖ 密文 ‖ tag16) + ".cpenc"，
	// 要保证整名 ≤ 255 字节（主流网盘与 Windows 单段文件名的通行上限）：
	//
	//	ceil((150 + 12 + 16) × 4/3) = 238，+ len(".cpenc") = 244 ≤ 255
	//
	// 即 150 字节明文（约 50 个汉字 / 150 个 ASCII 字符）是「绝不撞限」的
	// 最大可用值。截断只发生在超过这个长度的名字上，且只截尾巴。
	FilenameMaxPlainBytes = 150

	// FilenameMaxEncodedBytes 是「密文名 + 扩展名」的长度上限，
	// 由 FilenameMaxPlainBytes 推导而来，供测试与文档引用（勿手改）。
	FilenameMaxEncodedBytes = 244
)
