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


@pytest.fixture(autouse=True)
def _sync_vault_ops(monkeypatch):
    """测试环境把密库重操作（建库/开库）切到同步路径。

    产品默认后台线程执行避免冻结界面；测试里同步执行才能
    在 accept()/_connect() 返回后立即断言产物。
    """
    from cloudprism.gui.init_wizard import InitWizard
    from cloudprism.gui.quick_connect import QuickConnectDialog

    monkeypatch.setattr(InitWizard, "sync_ops", True)
    monkeypatch.setattr(QuickConnectDialog, "sync_ops", True)
