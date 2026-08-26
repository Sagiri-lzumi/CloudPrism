"""设置页百度网盘分组测试（pytest-qt）。

注入假存储 / 假授权对话框 / monkeypatch 网络请求，不触网、不碰真实凭证文件。
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6")

from PySide6.QtWidgets import QMessageBox

import cloudprism.gui.side_panel as sp
from cloudprism.gui.baidu_auth import BaiduAuthDialog
from cloudprism.gui.baidu_guide import BaiduGuideDialog, load_guide_text
from cloudprism.gui.side_panel import SettingsPage
from cloudprism.storage.baidu_backend import BaiduCredentialStore


class FakeStore:
    """内存假凭证存储。"""

    def __init__(self, data: dict | None = None) -> None:
        self.data = data
        self.saved: list[dict] = []
        self.cleared = 0

    def load(self):
        return self.data

    def save(self, data: dict) -> None:
        self.data = dict(data)
        self.saved.append(dict(data))

    def clear(self) -> None:
        self.data = None
        self.cleared += 1


class FakeAuthDialog:
    """假授权对话框：记录入参，按类属性决定成功/失败。"""

    instances: list = []
    accepted = True

    def __init__(
        self,
        store=None,
        parent=None,
        http_session=None,
        prefill=None,
        show_credentials_form=True,
    ) -> None:
        self.prefill = prefill
        self.show_credentials_form = show_credentials_form
        self.token_data = {"access_token": "t"} if FakeAuthDialog.accepted else None
        FakeAuthDialog.instances.append(self)

    def exec(self):
        return 1 if FakeAuthDialog.accepted else 0


@pytest.fixture
def fake_auth(monkeypatch):
    """替换 side_panel 引用的 BaiduAuthDialog。"""
    FakeAuthDialog.instances = []
    FakeAuthDialog.accepted = True
    monkeypatch.setattr(sp, "BaiduAuthDialog", FakeAuthDialog)
    return FakeAuthDialog


@pytest.fixture
def page(qtbot):
    """注入假存储的设置页。"""
    p = SettingsPage(baidu_store=FakeStore())
    qtbot.addWidget(p)
    return p


def _fill(page, app_id="1001", app_key="AK123456", secret="SK123456"):
    page._baidu_appid_edit.setText(app_id)
    page._baidu_appkey_edit.setText(app_key)
    page._baidu_secret_edit.setText(secret)


@pytest.fixture
def no_net(monkeypatch):
    """放行网络探测：head 成功，get 返回无 uname 的空数据。"""
    monkeypatch.setattr("requests.head", lambda *a, **k: None)

    class _Resp:
        def json(self):
            return {}

    monkeypatch.setattr("requests.get", lambda *a, **k: _Resp())


# ---------------------------------------------------------------------------
# 状态与回填
# ---------------------------------------------------------------------------


def test_empty_state(page):
    """无凭证：未配置。"""
    assert page._baidu_status.text() == "未配置"


def test_backfill_and_authorized_state(qtbot):
    """已保存凭证回填；有 token 显示已授权。"""
    store = FakeStore(
        {"app_id": "9", "app_key": "AKx", "secret_key": "SKx", "access_token": "t"}
    )
    p = SettingsPage(baidu_store=store)
    qtbot.addWidget(p)
    assert p._baidu_appkey_edit.text() == "AKx"
    assert p._baidu_secret_edit.text() == "SKx"
    assert "已授权" in p._baidu_status.text()


# ---------------------------------------------------------------------------
# 检查
# ---------------------------------------------------------------------------


def test_check_requires_key_and_secret(page):
    """空凭证：报错且不触网。"""
    assert page._check_baidu() is False
    assert "请先填写" in page._baidu_status.text()


def test_check_rejects_whitespace(page):
    """含空白字符：报错。"""
    _fill(page, app_key="AK 123")
    assert page._check_baidu() is False
    assert "空白字符" in page._baidu_status.text()


def test_check_success_saves_credentials(page, no_net):
    """合法格式：通过 + 写入存储 + 文案明示最终验证在授权时。"""
    _fill(page)
    assert page._check_baidu() is True
    assert "格式检查通过" in page._baidu_status.text()
    saved = page._baidu_store.saved[-1]
    assert saved["app_key"] == "AK123456"
    assert saved["secret_key"] == "SK123456"


def test_check_offline_still_saves(page, monkeypatch):
    """网络不可达：不致命，凭证仍保存。"""

    def _boom(*a, **k):
        raise ConnectionError("offline")

    monkeypatch.setattr("requests.head", _boom)
    _fill(page)
    assert page._check_baidu() is True
    assert "网络不可达" in page._baidu_status.text()
    assert page._baidu_store.saved[-1]["app_key"] == "AK123456"


def test_check_live_token_uinfo(qtbot, monkeypatch):
    """已授权时检查实测 token 并显示账号。"""
    store = FakeStore(
        {"app_key": "AKx", "secret_key": "SKx", "access_token": "tok"}
    )
    p = SettingsPage(baidu_store=store)
    qtbot.addWidget(p)

    monkeypatch.setattr("requests.head", lambda *a, **k: None)

    class _Resp:
        def json(self):
            return {"uname": "tester"}

    monkeypatch.setattr("requests.get", lambda *a, **k: _Resp())
    assert p._check_baidu() is True
    assert "tester" in p._baidu_status.text()


def test_check_expired_token(qtbot, monkeypatch):
    """token 失效：提示重新登录。"""
    store = FakeStore(
        {"app_key": "AKx", "secret_key": "SKx", "access_token": "tok"}
    )
    p = SettingsPage(baidu_store=store)
    qtbot.addWidget(p)
    monkeypatch.setattr("requests.head", lambda *a, **k: None)

    class _Resp:
        def json(self):
            return {"errno": 111}

    monkeypatch.setattr("requests.get", lambda *a, **k: _Resp())
    assert p._check_baidu() is True
    assert "token 已失效" in p._baidu_status.text()


# ---------------------------------------------------------------------------
# 教程
# ---------------------------------------------------------------------------


def test_guide_button_opens_dialog(page, monkeypatch):
    """点击教程按钮创建 BaiduGuideDialog（不自动弹出）。"""
    created = []

    class FakeGuide:
        def __init__(self, parent=None, guide_path=None):
            created.append(self)

        def exec(self):
            return 1

    monkeypatch.setattr(sp, "BaiduGuideDialog", FakeGuide)
    page._baidu_guide_btn.click()
    assert len(created) == 1


def test_guide_dialog_loads_path(qtbot, tmp_path):
    """注入路径加载教程正文。"""
    f = tmp_path / "g.md"
    f.write_text("# 测试教程正文", encoding="utf-8")
    dlg = BaiduGuideDialog(guide_path=str(f))
    qtbot.addWidget(dlg)
    assert "测试教程正文" in dlg.text


def test_guide_dialog_fallback(qtbot, tmp_path):
    """路径缺失走兜底文本，不抛异常。"""
    dlg = BaiduGuideDialog(guide_path=str(tmp_path / "nope.md"))
    qtbot.addWidget(dlg)
    assert "教程文件缺失" in dlg.text


def test_default_guide_found():
    """默认能定位仓库内置教程。"""
    assert "百度网盘凭证申请教程" in load_guide_text()


# ---------------------------------------------------------------------------
# 登录
# ---------------------------------------------------------------------------


def test_login_success(page, fake_auth):
    """登录：格式把关 → 传凭证 → 成功后状态已授权。"""
    _fill(page)
    page._login_baidu()
    dlg = FakeAuthDialog.instances[0]
    assert dlg.prefill["app_key"] == "AK123456"
    assert dlg.show_credentials_form is False
    assert "已授权" in page._baidu_status.text()


def test_login_rejected_stays(page, fake_auth):
    """取消授权：不进入已授权态。"""
    FakeAuthDialog.accepted = False
    _fill(page)
    page._login_baidu()
    assert "已授权" not in page._baidu_status.text()


def test_login_blocked_without_keys(page, fake_auth):
    """未填凭证：不打开对话框。"""
    page._login_baidu()
    assert FakeAuthDialog.instances == []


# ---------------------------------------------------------------------------
# 清除
# ---------------------------------------------------------------------------


def test_clear_confirmed(page, monkeypatch):
    """确认清除：存储清空、输入框清空。"""
    _fill(page)
    page._check_baidu = lambda: True  # 确保有已保存数据
    page._save_baidu_credentials(page._collect_baidu_credentials())
    monkeypatch.setattr(
        QMessageBox, "question",
        staticmethod(lambda *a, **k: QMessageBox.StandardButton.Yes),
    )
    page._clear_baidu()
    assert page._baidu_store.cleared == 1
    assert page._baidu_appkey_edit.text() == ""
    assert page._baidu_status.text() == "未配置"


def test_clear_cancelled(page, monkeypatch):
    """取消清除：存储不动。"""
    monkeypatch.setattr(
        QMessageBox, "question",
        staticmethod(lambda *a, **k: QMessageBox.StandardButton.No),
    )
    page._clear_baidu()
    assert page._baidu_store.cleared == 0


# ---------------------------------------------------------------------------
# BaiduCredentialStore.clear / BaiduAuthDialog 复用参数
# ---------------------------------------------------------------------------


def test_store_clear_idempotent(tmp_path):
    """clear() 对不存在的文件容忍；清除后 load 返回 None。"""
    store = BaiduCredentialStore(path=str(tmp_path / "baidu.json"))
    store.clear()  # 不存在不报错
    store.save({"app_key": "AK", "secret_key": "SK"})
    store.clear()
    assert store.load() is None


def test_auth_dialog_hidden_form(tmp_path):
    """show_credentials_form=False：表单隐藏且 prefill 回填。"""
    store = BaiduCredentialStore(path=str(tmp_path / "baidu.json"))
    dlg = BaiduAuthDialog(
        store=store,
        prefill={"app_id": "9", "app_key": "AK123456", "secret_key": "SK"},
        show_credentials_form=False,
    )
    assert dlg._form_widget.isHidden()
    assert dlg._appkey_edit.text() == "AK123456"


def test_auth_dialog_prefill_overrides_store(tmp_path):
    """prefill 优先于存储中的旧凭证。"""
    store = BaiduCredentialStore(path=str(tmp_path / "baidu.json"))
    store.save({"app_key": "OLD", "secret_key": "OLDSK"})
    dlg = BaiduAuthDialog(store=store, prefill={"app_key": "NEW", "secret_key": "NSK"})
    assert dlg._appkey_edit.text() == "NEW"


def test_auth_dialog_default_form_visible(tmp_path):
    """默认模式：凭证表单可见（隐藏标记为 False）。"""
    store = BaiduCredentialStore(path=str(tmp_path / "baidu.json"))
    dlg = BaiduAuthDialog(store=store)
    assert not dlg._form_widget.isHidden()
