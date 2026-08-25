"""QuickConnectDialog 单元测试（pytest-qt）。

注入假工厂与假 VaultManager，不触碰真实后端/网络。
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6")

from PySide6.QtWidgets import QDialog

import cloudprism.gui.quick_connect as qc
from cloudprism.gui.quick_connect import QuickConnectDialog


class FakeMeta:
    """假密库元信息。"""


class FakeBackend:
    """假后端。"""


class FakeVaultManager:
    """open_vault 按类属性返回（None 表示密码错误）。"""

    result = FakeMeta()

    def __init__(self, backend) -> None:
        self.backend = backend

    def open_vault(self, pw: str):
        return type(self).result


@pytest.fixture
def fake_vm(monkeypatch):
    """替换 quick_connect 模块内的 VaultManager。"""
    monkeypatch.setattr(qc, "VaultManager", FakeVaultManager)
    FakeVaultManager.result = FakeMeta()
    return FakeVaultManager


class RecordingFactory:
    """记录调用参数的假工厂。"""

    def __init__(self, raise_exc: Exception | None = None) -> None:
        self.calls: list[tuple] = []
        self.raise_exc = raise_exc

    def __call__(self, backend_type, **kw):
        self.calls.append((backend_type, kw))
        if self.raise_exc is not None:
            raise self.raise_exc
        return FakeBackend()


LOCAL_RECORD = {
    "key": "local|D:/v",
    "backend_type": "local",
    "label": "本地文件夹",
    "path": "D:/v",
    "vault_name": "abcd1234",
    "last_used": "2026-08-26 10:00",
}

WEBDAV_RECORD = {
    "key": "webdav|https://d.x",
    "backend_type": "webdav",
    "label": "WebDAV",
    "path": "https://d.x",
    "webdav_user": "u1",
    "vault_name": "abcd1234",
}


def test_connect_success(qtbot, fake_vm):
    """成功：产物存于属性并接受对话框。"""
    factory = RecordingFactory()
    dlg = QuickConnectDialog(LOCAL_RECORD, backend_factory=factory)
    qtbot.addWidget(dlg)
    dlg._pw_edit.setText("pw")
    dlg._connect()
    assert isinstance(dlg.backend, FakeBackend)
    assert dlg.metadata is fake_vm.result
    assert dlg.session is not None
    assert dlg.result() == QDialog.DialogCode.Accepted
    # 工厂收到 local 参数
    btype, kw = factory.calls[0]
    assert btype == "local" and kw["local_dir"] == "D:/v"


def test_wrong_password_stays_open(qtbot, fake_vm):
    """密码错误（open_vault 返回 None）：提示且不关闭。"""
    FakeVaultManager.result = None
    dlg = QuickConnectDialog(LOCAL_RECORD, backend_factory=RecordingFactory())
    qtbot.addWidget(dlg)
    dlg._pw_edit.setText("wrong")
    dlg._connect()
    assert dlg.metadata is None
    assert "主密码错误" in dlg._status.text()
    assert dlg.result() != QDialog.DialogCode.Accepted


def test_empty_password_blocked(qtbot, fake_vm):
    """未输入主密码：不调用工厂。"""
    factory = RecordingFactory()
    dlg = QuickConnectDialog(LOCAL_RECORD, backend_factory=factory)
    qtbot.addWidget(dlg)
    dlg._connect()
    assert "请输入主密码" in dlg._status.text()
    assert factory.calls == []


def test_factory_error_shown(qtbot, fake_vm):
    """工厂抛错（如目录不可达）：显示具体错误不关闭。"""
    factory = RecordingFactory(raise_exc=FileNotFoundError("目录不存在"))
    dlg = QuickConnectDialog(LOCAL_RECORD, backend_factory=factory)
    qtbot.addWidget(dlg)
    dlg._pw_edit.setText("pw")
    dlg._connect()
    assert "连接失败" in dlg._status.text()
    assert "目录不存在" in dlg._status.text()
    assert dlg.metadata is None


def test_webdav_requires_server_password(qtbot, fake_vm):
    """webdav 记录：出现服务器密码框且必填。"""
    factory = RecordingFactory()
    dlg = QuickConnectDialog(WEBDAV_RECORD, backend_factory=factory)
    qtbot.addWidget(dlg)
    assert dlg._webdav_pass_edit is not None
    dlg._pw_edit.setText("pw")
    dlg._connect()  # 未输服务器密码
    assert "服务器密码" in dlg._status.text()
    assert factory.calls == []
    # 补齐后成功且参数透传
    dlg._webdav_pass_edit.setText("sp")
    dlg._connect()
    assert dlg.result() == QDialog.DialogCode.Accepted
    btype, kw = factory.calls[0]
    assert btype == "webdav"
    assert kw["webdav_url"] == "https://d.x"
    assert kw["webdav_user"] == "u1"
    assert kw["webdav_pass"] == "sp"


def test_local_record_no_webdav_field(qtbot, fake_vm):
    """local 记录：不出现服务器密码框。"""
    dlg = QuickConnectDialog(LOCAL_RECORD, backend_factory=RecordingFactory())
    qtbot.addWidget(dlg)
    assert dlg._webdav_pass_edit is None


# ---------------------------------------------------------------------------
# VaultInfoPage 最近密库列表
# ---------------------------------------------------------------------------


def test_vault_info_page_recent_list(qtbot):
    """记录列表填充/空态切换/双击信号。"""
    from cloudprism.gui.vault_info_page import VaultInfoPage

    page = VaultInfoPage()
    qtbot.addWidget(page)
    page.show()

    # 空态：列表隐藏，引导文案
    assert not page._recent_list.isVisible()
    assert page._guide_title.text() == "尚未连接密库"

    # 填充两条记录
    page.set_recent_vaults([LOCAL_RECORD, WEBDAV_RECORD])
    assert page._recent_list.count() == 2
    assert page._recent_list.isVisible()
    assert page._guide_title.text() == "最近连接的密库"
    assert "D:/v" in page._recent_list.item(0).text()

    # 双击发射快速连接信号（携带记录）
    with qtbot.waitSignal(page.quickConnectRequested, timeout=1000) as blocker:
        page._recent_list.setCurrentRow(1)
        page._connect_selected()
    assert blocker.args[0]["backend_type"] == "webdav"

    # 移除信号携带记录
    with qtbot.waitSignal(page.removeVaultRequested, timeout=1000) as blocker:
        page._recent_list.setCurrentRow(0)
        page._remove_selected()
    assert blocker.args[0]["backend_type"] == "local"

    # 清空后回退引导态
    page.set_recent_vaults([])
    assert not page._recent_list.isVisible()
    assert page._guide_title.text() == "尚未连接密库"
