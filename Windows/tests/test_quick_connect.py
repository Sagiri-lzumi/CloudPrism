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
    """open_vault / open_vault_with_recovery 按类属性返回（None 表示失败）。"""

    result = FakeMeta()
    recovery_result = None  # 恢复码开库返回值（None = 恢复码无效）
    vault_exists = True     # has_vault 返回值（错误文案区分用）
    last_recovery_code: str | None = None
    last_vault_path: str | None = None  # 最近一次开库收到的位置参数

    def __init__(self, backend) -> None:
        self.backend = backend
        self.recovered_password: str | None = None

    def has_vault(self, vault_path: str = "") -> bool:
        return type(self).vault_exists

    def open_vault(self, pw: str, vault_path: str = ""):
        type(self).last_vault_path = vault_path
        return type(self).result

    def open_vault_with_recovery(self, code: str, vault_path: str = ""):
        type(self).last_recovery_code = code
        type(self).last_vault_path = vault_path
        if type(self).recovery_result is None:
            return None
        self.recovered_password = "recovered-pw"
        return type(self).recovery_result


@pytest.fixture
def fake_vm(monkeypatch):
    """替换 quick_connect 模块内的 VaultManager。"""
    monkeypatch.setattr(qc, "VaultManager", FakeVaultManager)
    FakeVaultManager.result = FakeMeta()
    FakeVaultManager.recovery_result = None
    FakeVaultManager.vault_exists = True
    FakeVaultManager.last_recovery_code = None
    FakeVaultManager.last_vault_path = None
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
# 子目录密库位置传递与错误文案区分
# ---------------------------------------------------------------------------


def test_connect_passes_vault_path(qtbot, fake_vm):
    """记录带子目录位置：按位置开库，摘要展示子目录。"""
    rec = dict(LOCAL_RECORD)
    rec["vault_path"] = "work"
    dlg = QuickConnectDialog(rec, backend_factory=RecordingFactory())
    qtbot.addWidget(dlg)
    assert dlg.vault_path == "work"
    assert "D:/v / work" in dlg._summary.text()
    dlg._pw_edit.setText("pw")
    dlg._connect()
    assert FakeVaultManager.last_vault_path == "work"
    assert dlg.result() == QDialog.DialogCode.Accepted


def test_no_vault_at_location_message(qtbot, fake_vm):
    """该位置无 Marker：报"不存在Mi库"而非误导性的密码错误。"""
    FakeVaultManager.result = None
    FakeVaultManager.vault_exists = False
    dlg = QuickConnectDialog(LOCAL_RECORD, backend_factory=RecordingFactory())
    qtbot.addWidget(dlg)
    dlg._pw_edit.setText("pw")
    dlg._connect()
    assert "不存在" in dlg._status.text()
    assert "主密码错误" not in dlg._status.text()
    assert dlg.result() != QDialog.DialogCode.Accepted


def test_recovery_passes_vault_path(qtbot, fake_vm):
    """恢复码开库同样携带位置参数。"""
    rec = dict(LOCAL_RECORD)
    rec["vault_path"] = "sub"
    FakeVaultManager.recovery_result = FakeMeta()
    dlg = QuickConnectDialog(rec, backend_factory=RecordingFactory())
    qtbot.addWidget(dlg)
    dlg._recovery_check.setChecked(True)
    dlg._recovery_edit.setText("AAAA-BBBB-CCCC-DDDD")
    dlg._connect()
    assert FakeVaultManager.last_vault_path == "sub"
    assert dlg.result() == QDialog.DialogCode.Accepted


# ---------------------------------------------------------------------------
# 恢复码开库
# ---------------------------------------------------------------------------


def test_recovery_toggle_reveals_edit(qtbot, fake_vm):
    """勾选“使用恢复码”展开输入框，取消勾选收起（未显示窗口用 isHidden 判定）。"""
    dlg = QuickConnectDialog(LOCAL_RECORD, backend_factory=RecordingFactory())
    qtbot.addWidget(dlg)
    assert dlg._recovery_edit.isHidden()
    dlg._recovery_check.setChecked(True)
    assert not dlg._recovery_edit.isHidden()
    dlg._recovery_check.setChecked(False)
    assert dlg._recovery_edit.isHidden()


def test_recovery_connect_success(qtbot, fake_vm):
    """恢复码开库成功：无需主密码，会话用还原出的密码构建。"""
    FakeVaultManager.recovery_result = FakeMeta()
    dlg = QuickConnectDialog(LOCAL_RECORD, backend_factory=RecordingFactory())
    qtbot.addWidget(dlg)
    dlg._recovery_check.setChecked(True)
    dlg._recovery_edit.setText("AAAA-BBBB-CCCC-DDDD")
    dlg._connect()
    assert FakeVaultManager.last_recovery_code == "AAAA-BBBB-CCCC-DDDD"
    assert isinstance(dlg.backend, FakeBackend)
    assert dlg.session is not None
    assert dlg.result() == QDialog.DialogCode.Accepted


def test_recovery_invalid_stays_open(qtbot, fake_vm):
    """恢复码无效：提示且不关闭。"""
    dlg = QuickConnectDialog(LOCAL_RECORD, backend_factory=RecordingFactory())
    qtbot.addWidget(dlg)
    dlg._recovery_check.setChecked(True)
    dlg._recovery_edit.setText("ZZZZ-ZZZZ-ZZZZ-ZZZZ")
    dlg._connect()
    assert dlg.metadata is None
    assert "恢复码无效" in dlg._status.text()
    assert dlg.result() != QDialog.DialogCode.Accepted


def test_recovery_empty_blocked(qtbot, fake_vm):
    """勾选恢复码但未输入：提示且不构造后端。"""
    factory = RecordingFactory()
    dlg = QuickConnectDialog(LOCAL_RECORD, backend_factory=factory)
    qtbot.addWidget(dlg)
    dlg._recovery_check.setChecked(True)
    dlg._connect()
    assert "请输入恢复码" in dlg._status.text()
    assert factory.calls == []


# ---------------------------------------------------------------------------
# VaultInfoPage 最近密库列表
# ---------------------------------------------------------------------------


def test_vault_info_page_recent_list(qtbot):
    """记录卡片填充/空态切换/卡片动作信号。"""
    from cloudprism.gui.vault_info_page import VaultInfoPage

    page = VaultInfoPage()
    qtbot.addWidget(page)
    page.show()

    # 空态：列表隐藏，引导文案
    assert not page._recent_list.isVisible()
    assert page._guide_title.text() == "尚未连接密库"

    # 填充两条记录（卡片化：每行一张 RecentVaultCard）
    page.set_recent_vaults([LOCAL_RECORD, WEBDAV_RECORD])
    assert page._recent_list.count() == 2
    assert page._recent_list.isVisible()
    assert page._guide_title.text() == "最近连接的密库"
    card0 = page.card_at(0)
    assert card0 is not None
    assert "D:/v" in card0.detail_label.text()

    # 点卡片「连接」按钮发射快速连接信号（携带记录）
    card1 = page.card_at(1)
    with qtbot.waitSignal(page.quickConnectRequested, timeout=1000) as blocker:
        card1.connect_btn.click()
    assert blocker.args[0]["backend_type"] == "webdav"

    # 卡片右上角移除按钮：发射移除信号（同时播淡出动画）
    with qtbot.waitSignal(page.removeVaultRequested, timeout=1000) as blocker:
        card0.remove_btn.click()
    assert blocker.args[0]["backend_type"] == "local"

    # 清空后回退引导态
    page.set_recent_vaults([])
    assert not page._recent_list.isVisible()
    assert page._guide_title.text() == "尚未连接密库"


def test_vault_info_page_card_double_click(qtbot):
    """双击卡片 = 快速连接。"""
    from PySide6.QtCore import QEvent, QPointF, Qt
    from PySide6.QtGui import QMouseEvent

    from cloudprism.gui.vault_info_page import VaultInfoPage

    page = VaultInfoPage()
    qtbot.addWidget(page)
    page.show()
    page.set_recent_vaults([LOCAL_RECORD])
    card = page.card_at(0)
    assert card is not None

    dbl = QMouseEvent(
        QEvent.Type.MouseButtonDblClick,
        QPointF(10, 10),
        QPointF(10, 10),
        Qt.MouseButton.LeftButton,
        Qt.MouseButton.LeftButton,
        Qt.KeyboardModifier.NoModifier,
    )
    with qtbot.waitSignal(page.quickConnectRequested, timeout=1000) as blocker:
        card.mouseDoubleClickEvent(dbl)
    assert blocker.args[0]["backend_type"] == "local"


def test_card_shows_custom_vault_name(qtbot):
    """记录卡片显示自定义密库名称（非 vault_id 截短）。"""
    from cloudprism.gui.vault_info_page import VaultInfoPage

    page = VaultInfoPage()
    qtbot.addWidget(page)
    rec = dict(LOCAL_RECORD)
    rec["vault_name"] = "工作资料库"
    page.set_recent_vaults([rec])
    card = page.card_at(0)
    assert card is not None
    assert card.name_label.text() == "工作资料库"


def test_vault_info_page_rename_signal(qtbot, monkeypatch):
    """库名称编辑按钮：输入框确认后发射重命名信号。"""
    import cloudprism.gui.vault_info_page as vip_mod
    from cloudprism.gui.vault_info_page import VaultInfoPage

    page = VaultInfoPage()
    qtbot.addWidget(page)
    page.update_info(vault_name="旧名")

    # 拦截模态输入框：模拟用户确认新名称（避免测试中弹窗阻塞）
    monkeypatch.setattr(
        vip_mod.QInputDialog, "getText",
        staticmethod(lambda *a, **kw: ("新名称", True)),
    )
    with qtbot.waitSignal(page.renameRequested, timeout=1000) as blocker:
        page._rename_btn.click()
    assert blocker.args[0] == "新名称"


def test_vault_info_page_rename_cancel_no_signal(qtbot, monkeypatch):
    """输入框取消或名称未变时不发射信号。"""
    import cloudprism.gui.vault_info_page as vip_mod
    from cloudprism.gui.vault_info_page import VaultInfoPage

    page = VaultInfoPage()
    qtbot.addWidget(page)
    page.update_info(vault_name="保持")
    monkeypatch.setattr(
        vip_mod.QInputDialog, "getText",
        staticmethod(lambda *a, **kw: ("", False)),
    )
    fired = []
    page.renameRequested.connect(lambda name: fired.append(name))
    page._rename_btn.click()
    assert fired == []
