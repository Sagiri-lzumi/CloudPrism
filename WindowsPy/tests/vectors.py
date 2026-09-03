"""跨端参考向量。

由 Windows 端实跑产出，供 Android 端对应实现逐字节比对，确保两端主密码
派生、文件头布局、Base32 编码等完全一致。任一端改动相关算法或参数后
须重新生成并同步本文件。

这些值仅作校验用，不含密钥或敏感信息。
"""

# KDF 跨端参考向量：derive_key("test", b"\x00"*16) 的 32 字节输出（hex）
# 由 Windows 端 PyCryptodome PBKDF2-HMAC-SHA256(iter=200000) 实跑产出
# 对应测试：tests/test_kdf.py::TestKdf::test_reference_vector
KDF_REFERENCE_KEY_HEX: str = (
    "188492f1d0c361353e6e9c33acc423f6"
    "cee47b05f2f547dafda40984b2615257"
)

# 对应的输入参数，便于 Android 端复现
KDF_REFERENCE_INPUT = {
    "master_password": "test",
    "salt_hex": "00" * 16,
    "iterations": 200000,
    "key_len": 32,
    "algo": "PBKDF2-HMAC-SHA256",
}
