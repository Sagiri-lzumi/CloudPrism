"""pytest 共享 fixtures。

参考向量在 tests/vectors.py（可导入模块），便于跨端比对；本文件只放 fixtures。
"""

import pytest


@pytest.fixture
def fetch_wait(qtbot):
    """触发目录树懒加载并等待异步完成。

    fetchMore 已改为后台线程执行（避免网盘网络往返阻塞 UI），
    测试中需等事件循环把完成信号投递回主线程后再断言。
    """
    from PySide6.QtCore import QModelIndex

    def _fetch(model, index=QModelIndex(), timeout=3000):
        if model.canFetchMore(index):
            model.fetchMore(index)
        # 加载完成后 canFetchMore 变假；失败时保持为真 -> 超时暴露问题
        qtbot.waitUntil(lambda: not model.canFetchMore(index), timeout=timeout)

    return _fetch


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


@pytest.fixture(autouse=True)
def _portable_paths_tmp(tmp_path, monkeypatch):
    """测试环境把便携数据文件重定向到临时目录。

    避免 AppController 等构造默认 SettingsStore / 百度凭证存储时，
    向项目目录的 data/ 或注册表写入测试数据。
    """
    monkeypatch.setattr(
        "cloudprism.core.settings_store.config_file",
        lambda create=False: str(tmp_path / "cloudprism.ini"),
    )
    monkeypatch.setattr(
        "cloudprism.core.paths.baidu_credential_file",
        lambda create=False: str(tmp_path / "baidu.json"),
    )
