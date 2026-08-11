"""pytest 共享 fixtures。

参考向量在 tests/vectors.py（可导入模块），便于跨端比对；本文件只放 fixtures。
"""

import pytest


@pytest.fixture
def sample_salt() -> bytes:
    """16 字节全零盐，用于 KDF 参考向量。"""
    return b"\x00" * 16


@pytest.fixture
def sample_master_password() -> str:
    """参考主密码。"""
    return "test"
