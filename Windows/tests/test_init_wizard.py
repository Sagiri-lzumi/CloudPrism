"""初始化向导与Mi库管理器测试。"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6")

from cloudprism import constants
from cloudprism.core.vault_manager import VaultError, VaultManager
from cloudprism.gui.init_wizard import InitWizard
from cloudprism.storage.local_backend import LocalFolderBackend

from PySide6.QtWidgets import QLabel, QRadioButton, QWizard


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

    def test_create_vault_with_name(self, tmp_path):
        """新建：自定义名称随 Marker 加密保存。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        meta = vm.create_vault("pw123", filename_enc=False, name="工作资料库")
        assert meta.name == "工作资料库"
        opened = vm.open_vault("pw123")
        assert opened is not None
        assert opened.name == "工作资料库"

    def test_rename_vault_roundtrip(self, tmp_path):
        """重命名：名称更新且 vault_id / 加密配置不变。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        created = vm.create_vault("pw123", filename_enc=True, name="旧名")
        renamed = vm.rename_vault("pw123", "新名称")
        assert renamed.name == "新名称"
        assert renamed.vault_id == created.vault_id
        assert renamed.filename_enc is True
        # 落盘后重新打开仍为新名称（名称随文件保存）
        reopened = VaultManager(LocalFolderBackend(root)).open_vault("pw123")
        assert reopened is not None
        assert reopened.name == "新名称"
        assert reopened.vault_id == created.vault_id

    def test_rename_vault_wrong_password(self, tmp_path):
        """重命名：错误密码抛 VaultError 且不改动云端数据。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        vm.create_vault("pw123", filename_enc=False, name="原名")
        with pytest.raises(VaultError):
            vm.rename_vault("wrong", "新名")
        # 云端名称未被破坏
        assert vm.open_vault("pw123").name == "原名"

    def test_rename_vault_no_vault(self, tmp_path):
        """重命名：后端无Mi库抛 VaultError。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        with pytest.raises(VaultError):
            vm.rename_vault("pw", "任意名")

    def test_list_vaults_root_and_subdirs(self, tmp_path):
        """多密库扫描：根目录 + 一级子目录的 Marker。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        assert vm.list_vaults() == []
        vm.create_vault("pw", filename_enc=False)
        vm.create_vault("pw2", filename_enc=False, vault_path="backup")
        (root / "plain_dir").mkdir()  # 无 Marker 的普通目录不算密库
        paths = sorted(v["path"] for v in vm.list_vaults())
        assert paths == ["", "backup"]

    def test_create_vault_with_recovery_roundtrip(self, tmp_path):
        """一次性建库带恢复码：主密码与恢复码均可开库（子目录位置）。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        meta, code = vm.create_vault_with_recovery(
            "pw123", filename_enc=True, name="新库", vault_path="work",
        )
        assert len(code) == constants.RECOVERY_CODE_LEN
        assert meta.has_recovery is True
        assert meta.name == "新库"
        # 主密码开库（子目录位置）仍带恢复块
        opened = vm.open_vault("pw123", "work")
        assert opened is not None
        assert opened.has_recovery is True
        # 凭恢复码开库还原主密码（含分组/小写清洗）
        meta2 = vm.open_vault_with_recovery(code, "work")
        assert meta2 is not None
        assert vm.recovered_password == "pw123"

    def test_create_vault_with_recovery_on_existing_raises(self, tmp_path):
        """一次性建库：目标已有密库同样抛 VaultError（防覆盖）。"""
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        vm.create_vault_with_recovery("pw", filename_enc=False)
        with pytest.raises(VaultError):
            vm.create_vault_with_recovery("pw2", filename_enc=False)


class TestRecoveryCode:
    """恢复码（v3）：生成 / 凭码开库 / 编解码。"""

    def _make(self, tmp_path):
        root = tmp_path / "b"; root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        vm.create_vault("master-pw", filename_enc=False, name="恢复库")
        return vm

    def test_generate_returns_code_and_meta(self, tmp_path):
        """生成：16 位恢复码 + has_recovery 元信息，重开后仍生效。"""
        vm = self._make(tmp_path)
        code, meta = vm.generate_recovery_code("master-pw")
        assert len(code) == constants.RECOVERY_CODE_LEN
        assert meta.has_recovery is True
        reopened = vm.open_vault("master-pw")
        assert reopened.has_recovery is True

    def test_generate_wrong_password_raises(self, tmp_path):
        vm = self._make(tmp_path)
        with pytest.raises(VaultError):
            vm.generate_recovery_code("wrong")

    def test_open_with_recovery_roundtrip(self, tmp_path):
        """凭恢复码开库：还原主密码与元信息。"""
        vm = self._make(tmp_path)
        code, _meta = vm.generate_recovery_code("master-pw")
        meta = vm.open_vault_with_recovery(code)
        assert meta is not None
        assert meta.name == "恢复库"
        assert vm.recovered_password == "master-pw"

    def test_open_with_grouped_code(self, tmp_path):
        """带分组分隔符的恢复码（用户复制格式）同样可用。"""
        vm = self._make(tmp_path)
        code, _ = vm.generate_recovery_code("master-pw")
        grouped = "-".join(code[i:i + 4] for i in range(0, len(code), 4))
        assert vm.open_vault_with_recovery(grouped.lower()) is not None

    def test_open_with_wrong_recovery_code(self, tmp_path):
        """格式合法但错误的恢复码：返回 None 且不泄露主密码。"""
        vm = self._make(tmp_path)
        vm.generate_recovery_code("master-pw")
        wrong = "A" * constants.RECOVERY_CODE_LEN
        assert vm.open_vault_with_recovery(wrong) is None
        assert vm.recovered_password is None

    def test_open_with_recovery_no_block(self, tmp_path):
        """未启用恢复码的密库：凭任意码返回 None。"""
        vm = self._make(tmp_path)
        assert vm.open_vault_with_recovery("A" * constants.RECOVERY_CODE_LEN) is None

    def test_old_code_invalid_after_rotation(self, tmp_path):
        """更换恢复码：旧码立即失效，新码生效。"""
        vm = self._make(tmp_path)
        old_code, _ = vm.generate_recovery_code("master-pw")
        new_code, _ = vm.generate_recovery_code("master-pw")
        assert old_code != new_code
        assert vm.open_vault_with_recovery(old_code) is None
        assert vm.open_vault_with_recovery(new_code) is not None

    def test_rename_keeps_recovery_block(self, tmp_path):
        """重命名不使既有恢复码失效。"""
        vm = self._make(tmp_path)
        code, _ = vm.generate_recovery_code("master-pw")
        vm.rename_vault("master-pw", "改名后")
        meta = vm.open_vault_with_recovery(code)
        assert meta is not None
        assert meta.name == "改名后"
        assert vm.recovered_password == "master-pw"

    def test_encode_decode_roundtrip(self):
        """编解码往返 + 格式清洗（分隔符/小写）。"""
        secret = bytes(range(constants.RECOVERY_SECRET_LEN))
        code = VaultManager.encode_recovery_code(secret)
        assert VaultManager.decode_recovery_code(code) == secret
        grouped = VaultManager.format_recovery_code(code)
        assert grouped.count("-") == len(code) // 4 - 1
        assert VaultManager.decode_recovery_code(grouped.lower()) == secret

    def test_decode_invalid_code_returns_none(self):
        assert VaultManager.decode_recovery_code("") is None
        assert VaultManager.decode_recovery_code("SHORT") is None
        assert VaultManager.decode_recovery_code("1" * 16) is None  # 非 Base32 字符


# ---------------------------------------------------------------------------
# InitWizard（GUI 流程）
# ---------------------------------------------------------------------------


def _make_wizard(qtbot):
    """构造向导并注册到 qtbot。"""
    w = InitWizard()
    qtbot.addWidget(w)
    return w


def _setup_local_backend(wizard, tmp_path):
    """辅助：为向导配置本地后端并通过测试连接。"""
    root = tmp_path / "vault_root"
    root.mkdir(exist_ok=True)
    wizard.page_backend_type.radio_local.setChecked(True)
    wizard.backend_type = "local"
    wizard.page_backend_cfg.local_dir_edit.setText(str(root))
    # 模拟测试连接通过
    wizard.page_backend_cfg._test_passed = True
    return root


class TestInitWizardNewVault:
    """新建Mi库向导流程。"""

    def test_new_vault_flow(self, qtbot, tmp_path):
        """新建：选类型 -> 配置目录 -> 密码确认 -> 文件名加密关闭 -> 完成。"""
        w = _make_wizard(qtbot)

        # 页1：新建模式（默认）
        assert w.is_new_mode() is True
        # 页2+3：本地后端配置
        root = _setup_local_backend(w, tmp_path)
        # 页4：密码
        w.page_password.pw_edit.setText("secret-pw")
        w.page_password.confirm_edit.setText("secret-pw")
        # 页5：文件名加密关闭
        w.page_enc.radio_off.setChecked(True)

        # 执行完成
        w.accept()

        assert w.metadata is not None
        assert w.metadata.filename_enc is False
        assert w.session is not None
        assert isinstance(w.backend, LocalFolderBackend)
        # 根目录已写入 Vault Marker
        assert (root / constants.VAULT_MARKER_NAME).is_file()

    def test_new_vault_with_name(self, qtbot, tmp_path):
        """新建：填写密库名称后写入 metadata。"""
        w = _make_wizard(qtbot)
        root = _setup_local_backend(w, tmp_path)
        w.page_password.name_edit.setText("我的第一个库")
        w.page_password.pw_edit.setText("pw")
        w.page_password.confirm_edit.setText("pw")
        w.page_enc.radio_off.setChecked(True)
        w.accept()
        assert w.metadata is not None
        assert w.metadata.name == "我的第一个库"
        # 落盘验证：重新打开同目录仍可读回名称（名称随文件保存）
        reopened = VaultManager(LocalFolderBackend(root)).open_vault("pw")
        assert reopened is not None
        assert reopened.name == "我的第一个库"

    def test_new_vault_filename_enc_on(self, qtbot, tmp_path):
        """新建：开启文件名加密。"""
        w = _make_wizard(qtbot)
        _setup_local_backend(w, tmp_path)
        w.page_password.pw_edit.setText("pw")
        w.page_password.confirm_edit.setText("pw")
        w.page_enc.radio_on.setChecked(True)
        w.accept()
        assert w.metadata is not None
        assert w.metadata.filename_enc is True

    def test_mismatched_password_not_complete(self, qtbot, tmp_path):
        """新建：两次密码不一致时页 4 不完整。"""
        w = _make_wizard(qtbot)
        _setup_local_backend(w, tmp_path)
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
        _setup_local_backend(w, tmp_path)
        w.page_password.pw_edit.setText("new")
        w.page_password.confirm_edit.setText("new")
        w.accept()
        # 失败：无产物，错误信息记录
        assert w.metadata is None
        assert "失败" in w.error_label_text or w.error_label_text

    def test_new_vault_generates_recovery_code(self, qtbot, tmp_path):
        """新建即生成恢复码：元信息 has_recovery=True 且落盘生效。"""
        w = _make_wizard(qtbot)
        root = _setup_local_backend(w, tmp_path)
        w.page_password.pw_edit.setText("pw")
        w.page_password.confirm_edit.setText("pw")
        w.page_enc.radio_off.setChecked(True)
        w.accept()
        assert w.metadata is not None
        assert w.recovery_code
        assert len(w.recovery_code) == constants.RECOVERY_CODE_LEN
        assert w.metadata.has_recovery is True
        # 落盘验证：重开后恢复块仍在
        reopened = VaultManager(LocalFolderBackend(root)).open_vault("pw")
        assert reopened is not None
        assert reopened.has_recovery is True


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
        # 导航：模式页 -> 后端类型页
        w.next()
        # 后端类型页：默认本地
        assert w.backend_type == "local"
        # 导航：后端类型页 -> 后端配置页
        w.next()
        w.page_backend_cfg.local_dir_edit.setText(str(root))
        w.page_backend_cfg._test_passed = True
        # 导航：后端配置页 -> 密码页（连接模式隐藏名称框与确认框）
        w.next()
        assert w.page_password.confirm_edit.isVisibleTo(w.page_password) is False
        assert w.page_password.name_edit.isVisibleTo(w.page_password) is False
        w.page_password.pw_edit.setText(pw)
        w.accept()
        assert w.metadata is not None
        assert w.metadata.filename_enc is True

    def test_connect_wrong_password(self, qtbot, tmp_path):
        """连接：错误密码报错且不产出。"""
        root, _pw = self._prepare_vault(tmp_path)
        w = _make_wizard(qtbot)
        w.page_mode.radio_connect.setChecked(True)
        _setup_local_backend(w, tmp_path)
        w.page_password.pw_edit.setText("wrong-pw")
        w.accept()
        assert w.metadata is None
        assert w.session is None
        assert "密码错误" in w.error_label_text

    def test_connect_subdir_vault_with_location(self, qtbot, tmp_path):
        """连接模式：按填写的位置定位子目录密库。"""
        root = tmp_path / "vault_root"
        root.mkdir()
        VaultManager(LocalFolderBackend(root)).create_vault(
            "sub-pw", filename_enc=False, vault_path="work",
        )
        w = _make_wizard(qtbot)
        w.page_mode.radio_connect.setChecked(True)
        _setup_local_backend(w, tmp_path)
        w.page_backend_cfg.location_edit.setText("work")
        w.page_password.pw_edit.setText("sub-pw")
        w.accept()
        assert w.metadata is not None
        assert w.vault_path == "work"

    def test_connect_no_vault_at_location_message(self, qtbot, tmp_path):
        """连接模式：位置无密库时报"不存在"而非误导性密码错误。"""
        root = tmp_path / "vault_root"
        root.mkdir()  # 空目录，无 Marker
        w = _make_wizard(qtbot)
        w.page_mode.radio_connect.setChecked(True)
        _setup_local_backend(w, tmp_path)
        w.page_password.pw_edit.setText("any")
        w.accept()
        assert w.metadata is None
        assert "不存在" in w.error_label_text
        assert "密码错误" not in w.error_label_text

    def test_location_visible_in_connect_mode(self, qtbot):
        """位置框在连接模式同样可见（子目录密库定位依赖它）。"""
        w = _make_wizard(qtbot)
        w.page_mode.radio_connect.setChecked(True)
        w.backend_type = "local"
        w.page_backend_cfg.initializePage()
        assert not w.page_backend_cfg.location_edit.isHidden()
        assert not w.page_backend_cfg.location_label.isHidden()


# ---------------------------------------------------------------------------
# 新增：向导字体统一（ModernStyle + 14px 像素字体）
# ---------------------------------------------------------------------------


class TestWizardFonts:
    """向导观感与主界面一致：样式与字号。"""

    def test_wizard_uses_modern_style(self, qtbot):
        w = _make_wizard(qtbot)
        assert w.wizardStyle() == QWizard.WizardStyle.ModernStyle

    def test_wizard_body_fonts_14px(self, qtbot):
        """页内标签/单选钮/输入框统一 14px 像素字体。"""
        w = _make_wizard(qtbot)
        labels = w.page_backend_cfg.findChildren(QLabel)
        assert labels
        assert all(l.fontInfo().pixelSize() == 14 for l in labels)
        radios = w.page_backend_type.findChildren(QRadioButton)
        assert radios
        assert all(r.fontInfo().pixelSize() == 14 for r in radios)
        assert w.page_password.pw_edit.fontInfo().pixelSize() == 14


# ---------------------------------------------------------------------------
# 新增：后端类型页 & 测试连接
# ---------------------------------------------------------------------------


class TestBackendTypePage:
    """后端类型选择页测试。"""

    def test_backend_type_page_default_local(self, qtbot):
        """默认选中本地文件夹。"""
        w = _make_wizard(qtbot)
        assert w.page_backend_type.radio_local.isChecked()
        assert w.backend_type == "local"

    def test_backend_type_page_select_webdav(self, qtbot):
        """选中 WebDAV 时更新 backend_type。"""
        w = _make_wizard(qtbot)
        w.page_backend_type.radio_webdav.setChecked(True)
        assert w.backend_type == "webdav"


class TestBackendConfigPage:
    """后端配置页 & 测试连接测试。"""

    def test_backend_config_test_connection_local(self, qtbot, tmp_path):
        """本地后端：目录存在时测试连接成功。"""
        root = tmp_path / "vault_root"
        root.mkdir()
        w = _make_wizard(qtbot)
        w.backend_type = "local"
        w.page_backend_cfg.local_dir_edit.setText(str(root))
        # 点击测试连接
        w.page_backend_cfg._test_connection()
        assert w.page_backend_cfg._test_passed is True
        assert "成功" in w.page_backend_cfg.test_status.text()

    def test_backend_config_test_connection_local_invalid(self, qtbot):
        """本地后端：目录不存在时测试连接失败。"""
        w = _make_wizard(qtbot)
        w.backend_type = "local"
        w.page_backend_cfg.local_dir_edit.setText("/nonexistent/path/abc123")
        w.page_backend_cfg._test_connection()
        assert w.page_backend_cfg._test_passed is False
        assert "失败" in w.page_backend_cfg.test_status.text()

    def test_backend_config_incomplete_without_test(self, qtbot, tmp_path):
        """未通过测试连接时 isComplete 返回 False。"""
        root = tmp_path / "vault_root"
        root.mkdir()
        w = _make_wizard(qtbot)
        w.backend_type = "local"
        w.page_backend_cfg.local_dir_edit.setText(str(root))
        # 未点击测试连接
        assert w.page_backend_cfg._test_passed is False
        assert w.page_backend_cfg.isComplete() is False

    def test_backend_config_config_changed_resets_test(self, qtbot, tmp_path):
        """配置变更后测试状态被重置。"""
        root = tmp_path / "vault_root"
        root.mkdir()
        w = _make_wizard(qtbot)
        w.backend_type = "local"
        w.page_backend_cfg.local_dir_edit.setText(str(root))
        w.page_backend_cfg._test_passed = True
        # 修改配置 -> 测试状态重置
        w.page_backend_cfg.local_dir_edit.setText(str(root) + "/sub")
        assert w.page_backend_cfg._test_passed is False
