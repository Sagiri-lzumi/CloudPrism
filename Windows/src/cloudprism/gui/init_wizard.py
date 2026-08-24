"""初始化向导。

四步向导：
  1. 模式：新建Mi库 / 连接已有Mi库
  2. 后端：本地文件夹（选择目录） / WebDAV（URL+账号密码）
  3. 主密码：新建时输入+二次确认；连接时单次输入
  4. 文件名加密开关：仅新建模式（不可逆提示）；连接模式自动跳过

完成时执行 VaultManager.create_vault / open_vault，产出：
  wizard.session（主密码会话）、wizard.backend、wizard.metadata

详见 Plan/Framework_Windows_Client.md §5.9 与路线图步骤 12。
"""

from __future__ import annotations

from PySide6.QtCore import Signal
from PySide6.QtWidgets import (
    QButtonGroup,
    QFileDialog,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QPushButton,
    QRadioButton,
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
PAGE_BACKEND = 2
PAGE_PASSWORD = 3
PAGE_FILENAME_ENC = 4


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


class BackendPage(QWizardPage):
    """页 2：存储后端选择与配置。"""

    def __init__(self, parent=None):
        super().__init__(parent)
        self.setTitle("选择存储后端")
        self.setSubTitle("本地文件夹或 WebDAV，后续可扩展更多云盘")

        lay = QVBoxLayout(self)

        self.radio_local = QRadioButton("本地文件夹（选择一个目录作为云盘根）", self)
        self.radio_webdav = QRadioButton("WebDAV（填写服务器地址与账号）", self)
        self.radio_local.setChecked(True)
        mode_group = QButtonGroup(self)
        mode_group.addButton(self.radio_local)
        mode_group.addButton(self.radio_webdav)
        mode_group.buttonClicked.connect(lambda: self.completeChanged.emit())
        lay.addWidget(self.radio_local)

        # 本地目录选择行
        dir_row = QHBoxLayout()
        self.local_dir_edit = QLineEdit(self)
        self.local_dir_edit.setPlaceholderText("选择本地文件夹…")
        self.local_dir_edit.textChanged.connect(lambda: self.completeChanged.emit())
        browse_btn = QPushButton("浏览…", self)
        browse_btn.clicked.connect(self._browse_dir)
        dir_row.addWidget(self.local_dir_edit)
        dir_row.addWidget(browse_btn)
        lay.addLayout(dir_row)

        lay.addWidget(self.radio_webdav)

        # WebDAV 配置表单
        form = QVBoxLayout()
        self.webdav_url_edit = QLineEdit(self)
        self.webdav_url_edit.setPlaceholderText("https://dav.example.com/path/")
        self.webdav_user_edit = QLineEdit(self)
        self.webdav_user_edit.setPlaceholderText("用户名")
        self.webdav_pass_edit = QLineEdit(self)
        self.webdav_pass_edit.setPlaceholderText("密码")
        self.webdav_pass_edit.setEchoMode(QLineEdit.Password)
        for w in (self.webdav_url_edit, self.webdav_user_edit, self.webdav_pass_edit):
            w.textChanged.connect(lambda: self.completeChanged.emit())
            form.addWidget(w)
        lay.addLayout(form)
        lay.addStretch()

        self.registerField("local_dir*", self.local_dir_edit)
        self.registerField("webdav_url", self.webdav_url_edit)
        self.registerField("webdav_user", self.webdav_user_edit)
        self.registerField("webdav_pass", self.webdav_pass_edit)

    def _browse_dir(self) -> None:
        """弹出目录选择框。"""
        path = QFileDialog.getExistingDirectory(self, "选择云盘根目录")
        if path:
            self.local_dir_edit.setText(path)

    def isComplete(self) -> bool:  # noqa: N802
        """本地：目录已填；WebDAV：三项均填。"""
        if self.radio_local.isChecked():
            return bool(self.local_dir_edit.text().strip())
        return all(
            w.text().strip()
            for w in (self.webdav_url_edit, self.webdav_user_edit, self.webdav_pass_edit)
        )

    def build_backend(self) -> StorageBackend:
        """按选择构造后端实例。"""
        if self.radio_local.isChecked():
            return LocalFolderBackend(self.local_dir_edit.text().strip())
        return WebDavBackend(
            self.webdav_url_edit.text().strip(),
            auth=(
                self.webdav_user_edit.text().strip(),
                self.webdav_pass_edit.text(),
            ),
        )


class PasswordPage(QWizardPage):
    """页 3：主密码输入（新建时含二次确认）。"""

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
    """页 4：文件名加密开关（仅新建模式可达）。"""

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

        # 各页实例（测试与逻辑直接访问）
        self.page_mode = ModePage()
        self.page_backend = BackendPage()
        self.page_password = PasswordPage()
        self.page_enc = FilenameEncPage()
        self.setPage(PAGE_MODE, self.page_mode)
        self.setPage(PAGE_BACKEND, self.page_backend)
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
            backend = self.page_backend.build_backend()
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
