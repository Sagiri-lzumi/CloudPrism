"""CloudPrism 协议常量。

本模块集中定义加密容器二进制格式相关的所有常量。Windows 端与 Android 端
必须使用完全一致的取值，详见 Plan/Plan.md §2 与 Plan/Framework_Windows_Client.md §5.1。

任何常量的改动都会破坏跨端互操作性，修改前必须同步两端并升级版本号。
"""

# ---------------------------------------------------------------------------
# 加密文件头（.cpenc）相关常量
# ---------------------------------------------------------------------------

# 文件头魔数：8 字节，标识 CloudPrism 加密容器
# 末两字节 \x00\x01 为版本占位，便于未来区分子格式
MAGIC: bytes = b"CPRISM\x00\x01"

# 文件格式版本号（uint32，大端序）；当前为 1
VERSION: int = 1

# KDF 盐值长度（字节）；文件头与 Vault Marker 统一使用 16 字节盐
SALT_LEN: int = 16

# AES-CTR 流加密初始向量长度（字节）；作为 128-bit 大端计数器初值
IV_LEN: int = 16

# AES 块大小（字节）；CTR 模式按此分块生成密钥流
BLOCK_SIZE: int = 16

# ---------------------------------------------------------------------------
# 密钥派生函数（KDF）相关常量
# ---------------------------------------------------------------------------

# PBKDF2-HMAC-SHA256 迭代次数；两端必须一致，200000 兼顾安全与性能
KDF_ITERATIONS: int = 200_000

# 派生密钥长度（字节）；32 字节对应 AES-256
KEY_LEN: int = 32

# KDF 算法标识，仅作记录与日志用
KDF_ALGO: str = "PBKDF2-HMAC-SHA256"

# ---------------------------------------------------------------------------
# Vault Marker（.cloudprism_vault）相关常量
# ---------------------------------------------------------------------------

# Vault Marker 魔数：12 字节，标识金库标识文件
VAULT_MAGIC: bytes = b"CPRISM_VAULT"

# Vault Marker 文件名；固定存放于云盘根目录，隐藏文件名减少误删
VAULT_MARKER_NAME: str = ".cloudprism_vault"

# Vault Marker 格式版本号（uint32，大端序）；当前为 2
# v2：内部明文尾部追加用户自定义密库名称（name_len + name_utf8），
# 解析端对无名称字段的 v1 文件保持兼容（name 置空）
VAULT_VERSION: int = 2

# 密库名称上限（字符数）；随 Vault Marker 加密保存，跨设备跟随密库
VAULT_NAME_MAX_LEN: int = 32

# Vault Marker 内部明文中的校验魔数；调试用，GCM 标签已提供完整性校验
VAULT_VERIFY_MAGIC: bytes = b"CPV\x00"

# Vault Marker 内部明文中 Reserved 字段长度（字节）
VAULT_RESERVED_LEN: int = 8

# AES-GCM 认证标签长度（字节）
GCM_TAG_LEN: int = 16

# 文件名加密用的 AES-GCM nonce 长度（字节）
FILENAME_NONCE_LEN: int = 12

# ---------------------------------------------------------------------------
# 文件名与后端相关常量
# ---------------------------------------------------------------------------

# 加密文件统一扩展名
FILE_EXTENSION: str = ".cpenc"

# Base32 编码字母表（RFC 4648），不含填充符 =
BASE32_ALPHABET: str = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
