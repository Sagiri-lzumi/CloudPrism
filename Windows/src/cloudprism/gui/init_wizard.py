"""初始化向导。

五步向导：
  1. 模式：新建Mi库 / 连接已有Mi库
  2. 后端类型：本地文件夹 / WebDAV / 更多（预留）
  3. 后端配置：填写连接信息 + 测试连接
  4. 主密码：新建时输入+二次确认；连接时单次输入
  5. 文件名加密开关：仅新建模式（不可逆提示）；连接模式自动跳过

完成时执行 VaultManager.create_vault / open_vault，产出：
  wizard.session（主密码会话）、wizard.backend、wizard.metadata

详见 Plan/Framework_Windows_Client.md §5.9 与路线图步骤 12。
"""

from __future__ import annotations

from PySide6.QtCore import Signal, Qt
from PySide6.QtWidgets import (
    QButtonGroup,
    QFileDialog,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QPushButton,
    QRadioButton,
    QStackedWidget,
    QVBoxLayout,
    QWizard,
    QWizardPage,
)

from cloudprism.core.session import Session
from cloudprism.core.vault_manager import VaultManager
from cloudprism.storage.backend import StorageBackend
from cloudprism.storage.local_backend import LocalFolderBackend
from cloudprism.storage.webdav_backend import WebDavBackend


# 页面 ID
PAGE_MODE = 1
PAGE_BACKEND_TYPE = 2
PAGE_BACKEND_CFG = 3
PAGE_PASSWORD = 4
PAGE_FILENAME_ENC = 5


class ModePage(QWizardPage):
    """页 1：新建 / 连接模式选择。"""

    def __init__(self, parent=None):
        super().__init__(parent)
        self.setTitle("初始化 CloudPrism")
        self.setSubTitle("选择新建Mi库，或连接已有加密云盘")

        lay = QVBoxLayout(self)
        self.radio_new = QRadioButton("新建Mi库（首次使用，设置主密码）", self)
        self.radio_connect = QRadioButton("连接已有Mi库（输入主密码验证）", self)
        self.radio_new.setChecked(True)
        group = QButtonGroup(self)
        group.addButton(self.radio_new)
        group.addButton(self.radio_connect)
        lay.addWidget(self.radio_new)
        lay.addWidget(self.radio_connect)
        lay.addStretch()

    def nextId(self) -> int:
        """始终进入后端类型选择页。"""
        return PAGE_BACKEND_TYPE


class BackendTypePage(QWizardPage):
    """页 2：选择存储后端类型（不填写具体配置）。"""

    def __init__(self, parent=None):
        super().__init__(parent)
        self.setTitle("选择存储后端")
        self.setSubTitle("选择云盘类型，下一步填写连接信息")

        lay = QVBoxLayout(self)

        # 本地文件夹
        self.radio_local = QRadioButton("本地文件夹（选择一个目录作为云盘根）", self)
        self.radio_local.setChecked(True)

        # WebDAV
        self.radio_webdav = QRadioButton("WebDAV（填写服务器地址与账号）", self)

        # 更多云盘 API（预留占位，暂不可用）
        self.radio_more = QRadioButton("更多云盘 API（阿里云盘、百度网盘等）", self)
        self.radio_more.setEnabled(False)
        more_hint = QLabel("即将推出，敬请期待…", self)
        more_hint.setStyleSheet("color: #888; margin-left: 20px;")

        group = QButtonGroup(self)
        group.addButton(self.radio_local)
        group.addButton(self.radio_webdav)
        group.addButton(self.radio_more)
        group.buttonToggled.connect(self._on_type_changed)

        lay.addWidget(self.radio_local)
        lay.addWidget(self.radio_webdav)
        lay.addWidget(self.radio_more)
        lay.addWidget(more_hint)
        lay.addStretch()

    def _on_type_changed(self, btn, checked: bool) -> None:
        """记录用户选择的后端类型到向导。"""
        if not checked:
            return  # 只处理选中的情况
        wizard = self.wizard()
        if wizard is None:
            return
        if btn == self.radio_local:
            wizard.backend_type = "local"
        elif btn == self.radio_webdav:
            wizard.backend_type = "webdav"

    def initializePage(self) -> None:
        """确保默认值写入向导。"""
        wizard = self.wizard()
        if wizard is not None:
            wizard.backend_type = "local"

    def nextId(self) -> int:
        return PAGE_BACKEND_CFG


class BackendConfigPage(QWizardPage):
    """页 3：根据后端类型显示对应配置表单 + 测试连接按钮。"""

    def __init__(self, parent=None):
        super().__init__(parent)
        self.setTitle("配置连接信息")
        self.setSubTitle("填写完成后请点击「测试连接」验证")

        self._test_passed = False  # 测试连接是否已通过

        lay = QVBoxLayout(self)

        # 堆叠控件：根据后端类型切换显示
        self._stack = QStackedWidget(self)
        lay.addWidget(self._stack)

        # --- 索引 0：本地文件夹配置 ---
        local_widget = self._build_local_page()
        self._stack.addWidget(local_widget)

        # --- 索引 1：WebDAV 配置 ---
        webdav_widget = self._build_webdav_page()
        self._stack.addWidget(webdav_widget)

        # 测试连接区域（所有类型共用）
        test_row = QHBoxLayout()
        self.test_btn = QPushButton("测试连接", self)
        self.test_btn.clicked.connect(self._test_connection)
        self.test_status = QLabel("请填写信息后点击「测试连接」", self)
        self.test_status.setStyleSheet("color: #888;")
        test_row.addWidget(self.test_btn)
        test_row.addWidget(self.test_status, stretch=1)
        lay.addLayout(test_row)
        lay.addStretch()

    # ------------------------------------------------------------------
    # 本地配置子页
    # ------------------------------------------------------------------

    def _build_local_page(self) -> QWizardPage:
        """构造本地文件夹配置子页。"""
        from PySide6.QtWidgets import QWidget
        w = QWidget(self)
        lay = QVBoxLayout(w)
        dir_row = QHBoxLayout()
        self.local_dir_edit = QLineEdit(w)
        self.local_dir_edit.setPlaceholderText("选择本地文件夹…")
        self.local_dir_edit.textChanged.connect(self._on_config_changed)
        browse_btn = QPushButton("浏览…", w)
        browse_btn.clicked.connect(self._browse_dir)
        dir_row.addWidget(self.local_dir_edit)
        dir_row.addWidget(browse_btn)
        lay.addLayout(dir_row)
        lay.addStretch()
        return w

    def _browse_dir(self) -> None:
        """弹出目录选择框。"""
        path = QFileDialog.getExistingDirectory(self, "选择云盘根目录")
        if path:
            self.local_dir_edit.setText(path)

    # ------------------------------------------------------------------
    # WebDAV 配置子页
    # ------------------------------------------------------------------

    def _build_webdav_page(self) -> QWizardPage:
        """构造 WebDAV 配置子页。"""
        from PySide6.QtWidgets import QWidget
        w = QWidget(self)
        lay = QVBoxLayout(w)
        self.webdav_url_edit = QLineEdit(w)
        self.webdav_url_edit.setPlaceholderText("https://dav.example.com/path/")
        self.webdav_url_edit.textChanged.connect(self._on_config_changed)
        self.webdav_user_edit = QLineEdit(w)
        self.webdav_user_edit.setPlaceholderText("用户名")
        self.webdav_user_edit.textChanged.connect(self._on_config_changed)
        self.webdav_pass_edit = QLineEdit(w)
        self.webdav_pass_edit.setPlaceholderText("密码")
        self.webdav_pass_edit.setEchoMode(QLineEdit.Password)
        self.webdav_pass_edit.textChanged.connect(self._on_config_changed)
        lay.addWidget(QLabel("WebDAV 地址：", w))
        lay.addWidget(self.webdav_url_edit)
        lay.addWidget(QLabel("用户名：", w))
        lay.addWidget(self.webdav_user_edit)
        lay.addWidget(QLabel("密码：", w))
        lay.addWidget(self.webdav_pass_edit)
        lay.addStretch()
        return w

    # ------------------------------------------------------------------
    # 配置变更 & 测试连接
    # ------------------------------------------------------------------

    def _on_config_changed(self) -> None:
        """配置变更时重置测试状态。"""
        self._test_passed = False
        self.test_status.setText("配置已变更，请重新测试")
        self.test_status.setStyleSheet("color: #888;")
        self.completeChanged.emit()

    def _test_connection(self) -> None:
        """测试后端连接是否可用。"""
        try:
            backend = self.build_backend()
            # 本地后端：构造成功即通过（目录存在且可访问）
            # WebDAV：尝试列出根目录验证连通性
            if isinstance(backend, WebDavBackend):
                backend.list_dir("")
            self._test_passed = True
            self.test_status.setText("连接成功")
            self.test_status.setStyleSheet("color: #0a0; font-weight: bold;")
        except Exception as e:
            self._test_passed = False
            self.test_status.setText(f"连接失败：{e}")
            self.test_status.setStyleSheet("color: #c00;")
        self.completeChanged.emit()

    # ------------------------------------------------------------------
    # QWizardPage 接口
    # ------------------------------------------------------------------

    def initializePage(self) -> None:
        """根据向导的 backend_type 切换到对应配置子页。"""
        wizard = self.wizard()
        if wizard is not None:
            idx = 0 if wizard.backend_type == "local" else 1
            self._stack.setCurrentIndex(idx)
            # 更新副标题提示
            if wizard.backend_type == "local":
                self.setSubTitle("选择本地文件夹作为云盘根目录")
            else:
                self.setSubTitle("填写 WebDAV 服务器地址与账号")
        # 重置测试状态
        self._test_passed = False
        self.test_status.setText("请填写信息后点击「测试连接」")
        self.test_status.setStyleSheet("color: #888;")

    def isComplete(self) -> bool:  # noqa: N802
        """配置已填写且测试连接通过。"""
        wizard = self.wizard()
        if wizard is None:
            return False
        if not self._test_passed:
            return False
        if wizard.backend_type == "local":
            return bool(self.local_dir_edit.text().strip())
        return all(
            w.text().strip()
            for w in (self.webdav_url_edit, self.webdav_user_edit, self.webdav_pass_edit)
        )

    def nextId(self) -> int:
        return PAGE_PASSWORD

    def build_backend(self) -> StorageBackend:
        """按选择构造后端实例。"""
        wizard = self.wizard()
        backend_type = wizard.backend_type if wizard else "local"
        if backend_type == "local":
            return LocalFolderBackend(self.local_dir_edit.text().strip())
        return WebDavBackend(
            self.webdav_url_edit.text().strip(),
            auth=(
                self.webdav_user_edit.text().strip(),
                self.webdav_pass_edit.text(),
            ),
        )


class PasswordPage(QWizardPage):
    """页 4：主密码输入（新建时含二次确认）。"""

    def __init__(self, parent=None):
        super().__init__(parent)
        self.setTitle("主密码")
        self.setSubTitle("主密码仅存内存，不落盘、不上传云端")

        lay = QVBoxLayout(self)
        self.pw_edit = QLineEdit(self)
        self.pw_edit.setEchoMode(QLineEdit.Password)
        self.pw_edit.setPlaceholderText("输入主密码")
        self.pw_edit.textChanged.connect(lambda: self.completeChanged.emit())
        lay.addWidget(QLabel("主密码：", self))
        lay.addWidget(self.pw_edit)

        # 二次确认（仅新建模式显示）
        self.confirm_label = QLabel("再次输入确认：", self)
        self.confirm_edit = QLineEdit(self)
        self.confirm_edit.setEchoMode(QLineEdit.Password)
        self.confirm_edit.textChanged.connect(lambda: self.completeChanged.emit())
        lay.addWidget(self.confirm_label)
        lay.addWidget(self.confirm_edit)
        lay.addStretch()

    def initializePage(self) -> None:
        """根据模式显示/隐藏确认框。"""
        wizard = self.wizard()
        is_new = wizard.is_new_mode()
        self.confirm_label.setVisible(is_new)
        self.confirm_edit.setVisible(is_new)

    def isComplete(self) -> bool:  # noqa: N802
        """新建：两次一致且非空；连接：非空。"""
        pw = self.pw_edit.text()
        if not pw:
            return False
        if self.wizard().is_new_mode() and pw != self.confirm_edit.text():
            return False
        return True

    def nextId(self) -> int:
        """连接模式跳过文件名加密页，本页即末页。"""
        if not self.wizard().is_new_mode():
            return -1
        return PAGE_FILENAME_ENC


class FilenameEncPage(QWizardPage):
    """页 5：文件名加密开关（仅新建模式可达）。"""

    def __init__(self, parent=None):
        super().__init__(parent)
        self.setTitle("文件名加密（可选）")
        self.setSubTitle("加密云端可见的文件名，保护元信息")

        lay = QVBoxLayout(self)
        # 不可逆警告
        warn = QLabel(
            "⚠ 此选择初始化后【无法中途修改】！\n"
            "如需变更必须新建Mi库并重新加密上传所有文件。",
            self,
        )
        warn.setStyleSheet("color: #b00; font-weight: bold;")
        lay.addWidget(warn)

        self.radio_off = QRadioButton("关闭（云端可见原始文件名，仅内容加密）", self)
        self.radio_on = QRadioButton("开启（文件名一并加密，更安全）", self)
        self.radio_off.setChecked(True)
        group = QButtonGroup(self)
        group.addButton(self.radio_off)
        group.addButton(self.radio_on)
        lay.addWidget(self.radio_off)
        lay.addWidget(self.radio_on)
        lay.addStretch()


class InitWizard(QWizard):
    """初始化向导。"""

    # 完成信号（供 MainWindow 刷新界面）
    finishedSetup = Signal()

    def __init__(self, parent=None):
        super().__init__(parent)
        self.setWindowTitle("CloudPrism 初始化")
        self.setOption(QWizard.NoBackButtonOnStartPage)

        # 完成后的产物
        self.session: Session | None = None
        self.backend: StorageBackend | None = None
        self.metadata = None
        self.error_label_text = ""

        # 后端类型选择（由 BackendTypePage 写入）
        self.backend_type: str = "local"

        # 各页实例
        self.page_mode = ModePage()
        self.page_backend_type = BackendTypePage()
        self.page_backend_cfg = BackendConfigPage()
        self.page_password = PasswordPage()
        self.page_enc = FilenameEncPage()
        self.setPage(PAGE_MODE, self.page_mode)
        self.setPage(PAGE_BACKEND_TYPE, self.page_backend_type)
        self.setPage(PAGE_BACKEND_CFG, self.page_backend_cfg)
        self.setPage(PAGE_PASSWORD, self.page_password)
        self.setPage(PAGE_FILENAME_ENC, self.page_enc)
        # 立即定位到首页（show() 之前 currentPage 为空，便于程序化导航/测试）
        self.restart()

    # ------------------------------------------------------------------
    # 模式与结果
    # ------------------------------------------------------------------

    def is_new_mode(self) -> bool:
        """当前是否新建模式。"""
        return self.page_mode.radio_new.isChecked()

    def _error(self, msg: str) -> None:
        """显示错误（不关闭向导）。"""
        self.error_label_text = msg
        # 用副标题位展示错误，避免模态对话框阻塞测试
        self.page_password.setSubTitle(msg)

    def accept(self) -> None:  # noqa: D102
        """完成：执行新建或连接。"""
        try:
            backend = self.page_backend_cfg.build_backend()
        except Exception as e:
            self._error(f"后端连接失败：{e}")
            return

        pw = self.page_password.pw_edit.text()
        vm = VaultManager(backend)

        if self.is_new_mode():
            # 新建Mi库
            filename_enc = self.page_enc.radio_on.isChecked()
            try:
                meta = vm.create_vault(pw, filename_enc)
            except Exception as e:
                self._error(f"新建Mi库失败：{e}")
                return
        else:
            # 连接已有Mi库
            meta = vm.open_vault(pw)
            if meta is None:
                self._error("密码错误或后端无Mi库，请检查后重试")
                return

        self.backend = backend
        self.metadata = meta
        self.session = Session(pw)
        self.finishedSetup.emit()
        super().accept()
