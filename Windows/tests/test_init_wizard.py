"""初始化向导与Mi库管理器测试。"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6")

from cloudprism import constants
from cloudprism.core.vault_manager import VaultManager
from cloudprism.gui.init_wizard import InitWizard
from cloudprism.storage.local_backend import LocalFolderBackend


# ---------------------------------------------------------------------------
# VaultManager（非 GUI）
# ---------------------------------------------------------------------------


class TestVaultManager:
    """Mi库新建/连接核心流程。"""

    def test_has_vault_false_initially(self, tmp_path):
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        assert vm.has_vault() is False

    def test_create_vault(self, tmp_path):
        """新建：根目录出现 Vault Marker。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        meta = vm.create_vault("pw123", filename_enc=True)
        assert (root / constants.VAULT_MARKER_NAME).is_file()
        assert meta.filename_enc is True

    def test_create_on_existing_raises(self, tmp_path):
        """重复新建抛 VaultError（防覆盖）。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        vm.create_vault("pw123", filename_enc=False)
        with pytest.raises(Exception):
            vm.create_vault("another", filename_enc=True)

    def test_open_vault_correct_password(self, tmp_path):
        """连接：正确密码返回元信息。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        created = vm.create_vault("pw123", filename_enc=True)
        opened = vm.open_vault("pw123")
        assert opened is not None
        assert opened.vault_id == created.vault_id
        assert opened.filename_enc is True

    def test_open_vault_wrong_password(self, tmp_path):
        """连接：错误密码返回 None。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        vm.create_vault("pw123", filename_enc=False)
        assert vm.open_vault("wrong") is None

    def test_open_vault_no_vault(self, tmp_path):
        """连接：无Mi库返回 None。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        assert vm.open_vault("pw") is None


# ---------------------------------------------------------------------------
# InitWizard（GUI 流程）
# ---------------------------------------------------------------------------


def _make_wizard(qtbot):
    """构造向导并注册到 qtbot。"""
    w = InitWizard()
    qtbot.addWidget(w)
    return w


class TestInitWizardNewVault:
    """新建Mi库向导流程。"""

    def test_new_vault_flow(self, qtbot, tmp_path):
        """新建：目录 -> 密码确认 -> 文件名加密关闭 -> 完成。"""
        root = tmp_path / "vault_root"
        root.mkdir()
        w = _make_wizard(qtbot)

        # 页1：新建模式（默认）
        assert w.is_new_mode() is True
        # 页2：本地后端
        w.page_backend.radio_local.setChecked(True)
        w.page_backend.local_dir_edit.setText(str(root))
        # 页3：密码
        w.page_password.pw_edit.setText("secret-pw")
        w.page_password.confirm_edit.setText("secret-pw")
        # 页4：文件名加密关闭
        w.page_enc.radio_off.setChecked(True)

        # 执行完成
        w.accept()

        assert w.metadata is not None
        assert w.metadata.filename_enc is False
        assert w.session is not None
        assert isinstance(w.backend, LocalFolderBackend)
        # 根目录已写入 Vault Marker
        assert (root / constants.VAULT_MARKER_NAME).is_file()

    def test_new_vault_filename_enc_on(self, qtbot, tmp_path):
        """新建：开启文件名加密。"""
        root = tmp_path / "vault_root"
        root.mkdir()
        w = _make_wizard(qtbot)
        w.page_backend.local_dir_edit.setText(str(root))
        w.page_password.pw_edit.setText("pw")
        w.page_password.confirm_edit.setText("pw")
        w.page_enc.radio_on.setChecked(True)
        w.accept()
        assert w.metadata is not None
        assert w.metadata.filename_enc is True

    def test_mismatched_password_not_complete(self, qtbot, tmp_path):
        """新建：两次密码不一致时页 3 不完整。"""
        root = tmp_path / "vault_root"
        root.mkdir()
        w = _make_wizard(qtbot)
        w.page_backend.local_dir_edit.setText(str(root))
        w.page_password.pw_edit.setText("a")
        w.page_password.confirm_edit.setText("b")
        assert w.page_password.isComplete() is False
        # 一致后恢复完整
        w.page_password.confirm_edit.setText("a")
        assert w.page_password.isComplete() is True

    def test_create_on_existing_shows_error(self, qtbot, tmp_path):
        """新建到已有Mi库：报错且不关闭产物。"""
        root = tmp_path / "vault_root"
        root.mkdir()
        VaultManager(LocalFolderBackend(root)).create_vault("old", filename_enc=False)

        w = _make_wizard(qtbot)
        w.page_backend.local_dir_edit.setText(str(root))
        w.page_password.pw_edit.setText("new")
        w.page_password.confirm_edit.setText("new")
        w.accept()
        # 失败：无产物，错误信息记录
        assert w.metadata is None
        assert "失败" in w.error_label_text or w.error_label_text


class TestInitWizardConnect:
    """连接已有Mi库向导流程。"""

    def _prepare_vault(self, tmp_path, pw="connect-pw"):
        root = tmp_path / "vault_root"
        root.mkdir()
        VaultManager(LocalFolderBackend(root)).create_vault(pw, filename_enc=True)
        return root, pw

    def test_connect_correct_password(self, qtbot, tmp_path):
        """连接：正确密码成功（真实导航流程）。"""
        root, pw = self._prepare_vault(tmp_path)
        w = _make_wizard(qtbot)
        w.page_mode.radio_connect.setChecked(True)
        assert w.is_new_mode() is False
        # 导航：模式页 -> 后端页
        w.next()
        w.page_backend.local_dir_edit.setText(str(root))
        # 后端页 -> 密码页（触发 initializePage 隐藏确认框）
        w.next()
        assert w.page_password.confirm_edit.isVisibleTo(w.page_password) is False
        w.page_password.pw_edit.setText(pw)
        w.accept()
        assert w.metadata is not None
        assert w.metadata.filename_enc is True

    def test_connect_wrong_password(self, qtbot, tmp_path):
        """连接：错误密码报错且不产出。"""
        root, _pw = self._prepare_vault(tmp_path)
        w = _make_wizard(qtbot)
        w.page_mode.radio_connect.setChecked(True)
        w.page_backend.local_dir_edit.setText(str(root))
        w.page_password.pw_edit.setText("wrong-pw")
        w.accept()
        assert w.metadata is None
        assert w.session is None
        assert "密码错误" in w.error_label_text
