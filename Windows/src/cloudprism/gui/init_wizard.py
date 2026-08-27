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
from PySide6.QtGui import QFont
from PySide6.QtWidgets import (
    QButtonGroup,
    QDialog,
    QFileDialog,
    QHBoxLayout,
    QLabel,
    QRadioButton,
    QStackedWidget,
    QTextEdit,
    QVBoxLayout,
    QWizard,
    QWizardPage,
)

# Fluent 组件（均继承自对应 Qt 原生控件，标准 API 全兼容）
from qfluentwidgets import (
    IndeterminateProgressBar,
    LineEdit,
    PrimaryPushButton,
    PushButton,
)

from cloudprism.core.backend_factory import build_backend_from_params
from cloudprism.core.session import Session
from cloudprism.core.vault_manager import VaultManager
from cloudprism.gui.baidu_auth import BaiduAuthDialog
from cloudprism.gui.busy_op import run_busy
from cloudprism.gui.theme import semantic_color
from cloudprism.storage.backend import StorageBackend
from cloudprism.storage.baidu_backend import (
    BaiduCredentialStore,
    BaiduNetdiskBackend,
)
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

        # 百度网盘（需在开放平台申请应用凭证，见 Plan/百度网盘开放平台申请指南.md）
        self.radio_baidu = QRadioButton("百度网盘（需开放平台应用凭证）", self)

        group = QButtonGroup(self)
        group.addButton(self.radio_local)
        group.addButton(self.radio_webdav)
        group.addButton(self.radio_baidu)
        group.buttonToggled.connect(self._on_type_changed)

        lay.addWidget(self.radio_local)
        lay.addWidget(self.radio_webdav)
        lay.addWidget(self.radio_baidu)
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
        elif btn == self.radio_baidu:
            wizard.backend_type = "baidu"

    def initializePage(self) -> None:
        """恢复上次选择；默认本地文件夹。"""
        wizard = self.wizard()
        if wizard is None:
            return
        saved = "local"
        store = getattr(wizard, "store", None)
        if store is not None:
            saved = store.backend_type() or "local"
        if saved == "webdav":
            self.radio_webdav.setChecked(True)
            wizard.backend_type = "webdav"
        elif saved == "baidu":
            self.radio_baidu.setChecked(True)
            wizard.backend_type = "baidu"
        else:
            self.radio_local.setChecked(True)
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

        # --- 索引 2：百度网盘配置 ---
        baidu_widget = self._build_baidu_page()
        self._stack.addWidget(baidu_widget)

        # 测试连接区域（所有类型共用）
        test_row = QHBoxLayout()
        self.test_btn = PrimaryPushButton("测试连接", self)
        self.test_btn.clicked.connect(self._test_connection)
        self.test_status = QLabel("请填写信息后点击「测试连接」", self)
        self.test_status.setStyleSheet(f"color: {semantic_color('muted')};")
        test_row.addWidget(self.test_btn)
        test_row.addWidget(self.test_status, stretch=1)
        lay.addLayout(test_row)

        # 密库位置：新建时选择建库位置；连接时定位已有密库（留空=根目录）
        self.location_label = QLabel("密库位置（可选，留空=后端根目录）：", self)
        self.location_edit = LineEdit(self)
        self.location_edit.setPlaceholderText(
            "子目录名，如 vault-work；留空 = 后端根目录"
        )
        lay.addWidget(self.location_label)
        lay.addWidget(self.location_edit)
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
        self.local_dir_edit = LineEdit(w)
        self.local_dir_edit.setPlaceholderText("选择本地文件夹…")
        self.local_dir_edit.textChanged.connect(self._on_config_changed)
        browse_btn = PushButton("浏览…", w)
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
        self.webdav_url_edit = LineEdit(w)
        self.webdav_url_edit.setPlaceholderText("https://dav.example.com/path/")
        self.webdav_url_edit.textChanged.connect(self._on_config_changed)
        self.webdav_user_edit = LineEdit(w)
        self.webdav_user_edit.setPlaceholderText("用户名")
        self.webdav_user_edit.textChanged.connect(self._on_config_changed)
        self.webdav_pass_edit = LineEdit(w)
        self.webdav_pass_edit.setPlaceholderText("密码")
        self.webdav_pass_edit.setEchoMode(LineEdit.Password)
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
    # 百度网盘配置子页
    # ------------------------------------------------------------------

    def _build_baidu_page(self):
        """构造百度网盘配置子页（引导授权，凭证加密落盘）。"""
        from PySide6.QtWidgets import QWidget
        w = QWidget(self)
        lay = QVBoxLayout(w)
        hint = QLabel(
            "使用百度网盘需在开放平台创建应用并取得凭证\n"
            "（Appid / AppKey / SecretKey）。凭证可在 设置 → 百度网盘\n"
            "中填写与检查（附申请教程），也可点击下方按钮授权。",
            w,
        )
        hint.setStyleSheet(f"color: {semantic_color('muted')};")
        hint.setWordWrap(True)
        lay.addWidget(hint)

        self.baidu_status = QLabel("尚未授权", w)
        self.baidu_status.setStyleSheet(f"color: {semantic_color('err')};")
        lay.addWidget(self.baidu_status)

        self.baidu_auth_btn = PushButton("授权 / 更新凭证…", w)
        self.baidu_auth_btn.clicked.connect(self._open_baidu_auth)
        lay.addWidget(self.baidu_auth_btn)
        lay.addStretch()

        self._baidu_creds: dict | None = None  # 授权成功后的凭证（含 token）
        return w

    def _open_baidu_auth(self) -> None:
        """打开百度授权对话框；成功后记录凭证并刷新状态。"""
        dlg = BaiduAuthDialog(parent=self)
        if dlg.exec() and dlg.token_data is not None:
            self._baidu_creds = dlg._store.load()
            self.baidu_status.setText("✓ 已授权（凭证已加密保存）")
            self.baidu_status.setStyleSheet(f"color: {semantic_color('ok')}; font-weight: bold;")
        self._test_passed = False
        self.test_status.setText("请点击上方「测试连接」验证")
        self.test_status.setStyleSheet(f"color: {semantic_color('muted')};")
        self.completeChanged.emit()

    # ------------------------------------------------------------------
    # 配置变更 & 测试连接
    # ------------------------------------------------------------------

    def _on_config_changed(self) -> None:
        """配置变更时重置测试状态。"""
        self._test_passed = False
        self.test_status.setText("配置已变更，请重新测试")
        self.test_status.setStyleSheet(f"color: {semantic_color('muted')};")
        self.completeChanged.emit()

    def _test_connection(self) -> None:
        """测试后端连接是否可用。"""
        try:
            backend = self.build_backend()
            # 本地后端：构造成功即通过（目录存在且可访问）
            # WebDAV / 百度网盘：尝试列出根目录验证连通性
            if isinstance(backend, (WebDavBackend, BaiduNetdiskBackend)):
                backend.list_dir("")
            self._test_passed = True
            self.test_status.setText("连接成功")
            self.test_status.setStyleSheet(f"color: {semantic_color('ok')}; font-weight: bold;")
        except Exception as e:
            self._test_passed = False
            self.test_status.setText(f"连接失败：{e}")
            self.test_status.setStyleSheet(f"color: {semantic_color('err')};")
        self.completeChanged.emit()

    # ------------------------------------------------------------------
    # QWizardPage 接口
    # ------------------------------------------------------------------

    def initializePage(self) -> None:
        """根据向导的 backend_type 切换到对应配置子页。"""
        wizard = self.wizard()
        store = getattr(wizard, "store", None) if wizard else None
        btype = wizard.backend_type if wizard else "local"
        idx = {"local": 0, "webdav": 1, "baidu": 2}.get(btype, 0)
        self._stack.setCurrentIndex(idx)
        # 密库位置两种模式均可见：连接时靠它定位子目录密库，
        # 否则默认开根目录会把子目录密库误报为密码错误
        # 更新副标题提示并回填上次配置
        if btype == "local":
            self.setSubTitle("选择本地文件夹作为云盘根目录")
            if store is not None and not self.local_dir_edit.text().strip():
                self.local_dir_edit.setText(store.local_dir())
        elif btype == "webdav":
            self.setSubTitle("填写 WebDAV 服务器地址与账号")
            if store is not None and not self.webdav_url_edit.text().strip():
                self.webdav_url_edit.setText(store.webdav_url())
                self.webdav_user_edit.setText(store.webdav_user())
        else:
            self.setSubTitle("完成百度网盘授权后测试连接")
            self._refresh_baidu_status()
        # 重置测试状态
        self._test_passed = False
        self.test_status.setText("请填写信息后点击「测试连接」")
        self.test_status.setStyleSheet(f"color: {semantic_color('muted')};")

    def _refresh_baidu_status(self) -> None:
        """按磁盘凭证刷新百度授权状态（用于页面进入时）。"""
        self._baidu_creds = BaiduCredentialStore().load()
        if self._baidu_creds and self._baidu_creds.get("access_token"):
            self.baidu_status.setText("✓ 已授权（凭证已加密保存）")
            self.baidu_status.setStyleSheet(f"color: {semantic_color('ok')}; font-weight: bold;")
        else:
            self.baidu_status.setText("尚未授权")
            self.baidu_status.setStyleSheet(f"color: {semantic_color('err')};")

    def isComplete(self) -> bool:  # noqa: N802
        """配置已填写且测试连接通过。"""
        wizard = self.wizard()
        if wizard is None:
            return False
        if not self._test_passed:
            return False
        if wizard.backend_type == "local":
            return bool(self.local_dir_edit.text().strip())
        if wizard.backend_type == "baidu":
            return bool(self._baidu_creds and self._baidu_creds.get("access_token"))
        return all(
            w.text().strip()
            for w in (self.webdav_url_edit, self.webdav_user_edit, self.webdav_pass_edit)
        )

    def nextId(self) -> int:
        return PAGE_PASSWORD

    def build_backend(self) -> StorageBackend:
        """按选择构造后端实例（委托 backend_factory，与快速连接共用）。"""
        wizard = self.wizard()
        backend_type = wizard.backend_type if wizard else "local"
        if backend_type == "baidu":
            creds = self._baidu_creds or BaiduCredentialStore().load()
            if not creds or not creds.get("access_token"):
                raise ConnectionError(
                    "尚未配置百度网盘凭证，请先按《百度网盘开放平台申请指南》"
                    "申请凭证并完成授权"
                )
        return build_backend_from_params(
            backend_type,
            local_dir=self.local_dir_edit.text().strip(),
            webdav_url=self.webdav_url_edit.text().strip(),
            webdav_user=self.webdav_user_edit.text().strip(),
            webdav_pass=self.webdav_pass_edit.text(),
        )


class PasswordPage(QWizardPage):
    """页 4：主密码输入（新建时含密库名称与二次确认）。"""

    def __init__(self, parent=None):
        super().__init__(parent)
        self.setTitle("主密码")
        self.setSubTitle("主密码仅存内存，不落盘、不上传云端")

        lay = QVBoxLayout(self)

        # 密库名称（仅新建模式显示）：随 Vault Marker 加密保存，后续可修改
        self.name_label = QLabel("密库名称（可选）：", self)
        self.name_edit = LineEdit(self)
        self.name_edit.setPlaceholderText("例如：我的网盘密库")
        self.name_edit.setMaxLength(32)
        lay.addWidget(self.name_label)
        lay.addWidget(self.name_edit)

        self.pw_edit = LineEdit(self)
        self.pw_edit.setEchoMode(LineEdit.Password)
        self.pw_edit.setPlaceholderText("输入主密码")
        self.pw_edit.textChanged.connect(lambda: self.completeChanged.emit())
        lay.addWidget(QLabel("主密码：", self))
        lay.addWidget(self.pw_edit)

        # 二次确认（仅新建模式显示）
        self.confirm_label = QLabel("再次输入确认：", self)
        self.confirm_edit = LineEdit(self)
        self.confirm_edit.setEchoMode(LineEdit.Password)
        self.confirm_edit.textChanged.connect(lambda: self.completeChanged.emit())
        lay.addWidget(self.confirm_label)
        lay.addWidget(self.confirm_edit)

        # 恢复码开库（仅连接模式显示）：填写后可代替主密码，留空则用主密码
        self.recovery_label = QLabel("恢复码（可选，丢失主密码时凭码开库）：", self)
        self.recovery_edit = LineEdit(self)
        self.recovery_edit.setPlaceholderText("XXXX-XXXX-XXXX-XXXX（留空则使用主密码）")
        lay.addWidget(self.recovery_label)
        lay.addWidget(self.recovery_edit)

        # 后台建库/开库期间的不定进度条（PBKDF2 无百分比可报，
        # 阶段文案由副标题展示；默认隐藏）
        self.busy_bar = IndeterminateProgressBar(self)
        self.busy_bar.setVisible(False)
        lay.addWidget(self.busy_bar)
        lay.addStretch()

    def initializePage(self) -> None:
        """根据模式显示/隐藏名称框、确认框与恢复码框。"""
        wizard = self.wizard()
        is_new = wizard.is_new_mode()
        self.name_label.setVisible(is_new)
        self.name_edit.setVisible(is_new)
        self.confirm_label.setVisible(is_new)
        self.confirm_edit.setVisible(is_new)
        self.recovery_label.setVisible(not is_new)
        self.recovery_edit.setVisible(not is_new)

    def isComplete(self) -> bool:  # noqa: N802
        """新建：两次一致且非空；连接：主密码或恢复码非空。"""
        pw = self.pw_edit.text()
        if self.wizard().is_new_mode():
            return bool(pw) and pw == self.confirm_edit.text()
        return bool(pw) or bool(self.recovery_edit.text().strip())

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
        warn.setStyleSheet(f"color: {semantic_color('err')}; font-weight: bold;")
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


class RecoveryCodeDialog(QDialog):
    """恢复码展示对话框：大字等宽只读展示 + 复制按钮，提示离线保存。

    新建密库成功后与设置页更换恢复码时复用。
    """

    def __init__(self, code: str, parent=None) -> None:
        super().__init__(parent)
        self.setWindowTitle("保存恢复码")
        self.setMinimumWidth(420)
        self._code = code

        lay = QVBoxLayout(self)
        lay.setSpacing(10)

        tip = QLabel(
            "请复制以下恢复码并离线保存（如密码管理器或纸质备份）。\n"
            "丢失主密码时可凭此码开库；恢复码丢失将无法找回。",
            self,
        )
        tip.setWordWrap(True)
        lay.addWidget(tip)

        self.code_edit = QTextEdit(self)
        self.code_edit.setReadOnly(True)
        self.code_edit.setFont(QFont("Consolas", 18))
        self.code_edit.setAlignment(Qt.AlignmentFlag.AlignCenter)
        self.code_edit.setFixedHeight(72)
        self.code_edit.setPlainText(VaultManager.format_recovery_code(code))
        lay.addWidget(self.code_edit)

        note = QLabel(
            "生成新恢复码后，旧恢复码立即失效；恢复码不会上传云端。",
            self,
        )
        note.setStyleSheet(f"color: {semantic_color('muted')};")
        note.setWordWrap(True)
        lay.addWidget(note)

        btn_row = QHBoxLayout()
        copy_btn = PushButton("复制恢复码", self)
        copy_btn.clicked.connect(self._copy_code)
        ok_btn = PrimaryPushButton("我已保存，完成", self)
        ok_btn.clicked.connect(self.accept)
        btn_row.addWidget(copy_btn)
        btn_row.addStretch()
        btn_row.addWidget(ok_btn)
        lay.addLayout(btn_row)

    def _copy_code(self) -> None:
        """将分组格式的恢复码写入剪贴板。"""
        from PySide6.QtWidgets import QApplication

        QApplication.clipboard().setText(
            VaultManager.format_recovery_code(self._code)
        )


class _OpError(Exception):
    """向导重操作的业务错误：消息直接面向用户展示。"""


def _unify_wizard_fonts(wizard) -> None:
    """统一向导字体，与主界面 Fluent 观感对齐（同 _unify_expand_font 惯例）。

    Windows 原生向导样式下标题/正文走系统字体，与应用级 pt 字号脱节；
    此处对向导与每页显式设定像素级 14px 字体（widget 级），
    未被继承链覆盖的 QLabel / QRadioButton / 输入框经 fontInfo 探测补设。
    """
    font = QFont()
    # 字体族回退链：首选 Segoe UI Variable，缺失时回退到中文友好字体
    font.setFamilies(["Segoe UI Variable", "Segoe UI", "Microsoft YaHei UI"])
    font.setPixelSize(14)  # 与库卡片/展开区字号一致
    wizard.setFont(font)
    for page_id in wizard.pageIds():
        page = wizard.page(page_id)
        if page is None:
            continue
        page.setFont(font)
        # 逐类型探测（本版本 PySide6 的 findChildren 不支持元组参数）
        widgets = []
        for cls in (QLabel, QRadioButton, LineEdit):
            widgets.extend(page.findChildren(cls))
        for w in widgets:
            if w.fontInfo().pixelSize() != 14:
                w.setFont(font)


class InitWizard(QWizard):
    """初始化向导。"""

    # 完成信号（供 MainWindow 刷新界面）
    finishedSetup = Signal()
    # 后台建库/开库阶段进度（文案来自 vault_manager 的 progress_cb，
    # 跨线程 emit 自动排队投递主线程）
    progressed = Signal(str)

    # 建库/开库含数秒级 PBKDF2 派生，默认后台线程执行避免冻结界面；
    # 测试置 True 走同步路径（无需事件循环）
    sync_ops = False

    def __init__(self, parent=None, store=None):
        super().__init__(parent)
        self.setWindowTitle("CloudPrism 初始化")
        self.setOption(QWizard.NoBackButtonOnStartPage)
        # Windows 默认 AeroStyle 向导走原生字体，与主界面不匹配；
        # 改 ModernStyle 并统一 14px 像素字体（见 _unify_wizard_fonts）
        self.setWizardStyle(QWizard.WizardStyle.ModernStyle)

        # 设置持久化存储（回填上次连接参数 / 保存本次选择）
        self.store = store

        # 完成后的产物
        self.session: Session | None = None
        self.backend: StorageBackend | None = None
        self.metadata = None
        self.error_label_text = ""

        # 后端类型选择（由 BackendTypePage 写入）
        self.backend_type: str = "local"

        # 密库位置（新建模式可选：空=后端根目录，否则为子目录名）
        self.vault_path: str = ""

        # 新建成功时生成的恢复码（控制器展示后由用户离线保存）
        self.recovery_code: str = ""

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
        # 阶段进度 -> 密码页副标题实时展示（避免数秒空白等待误以为卡死）
        self.progressed.connect(self._on_progress)
        # 立即定位到首页（show() 之前 currentPage 为空，便于程序化导航/测试）
        self.restart()
        # 页面就绪后统一字体（须在页实例创建后执行）
        _unify_wizard_fonts(self)

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

    def _on_progress(self, msg: str) -> None:
        """阶段进度文案实时写入密码页副标题。"""
        self.page_password.setSubTitle(msg)

    def _set_busy(self, busy: bool) -> None:
        """后台建库/开库期间锁定导航按钮并展示不定进度条。

        阶段文案经 progressed 信号逐条更新副标题；结束/出错时
        隐藏进度条，副标题留给错误文案或恢复默认。
        """
        for role in (QWizard.WizardButton.NextButton, QWizard.WizardButton.FinishButton):
            btn = self.button(role)
            if btn is not None:
                btn.setEnabled(not busy)
        self.page_password.busy_bar.setVisible(busy)
        if busy:
            self.page_password.setSubTitle(
                "正在处理，密码校验约需数秒，请稍候…"
            )

    def accept(self) -> None:  # noqa: D102
        """完成：执行新建或连接（重操作在后台线程，界面不冻结）。"""
        try:
            backend = self.page_backend_cfg.build_backend()
        except Exception as e:
            self._error(f"后端连接失败：{e}")
            return

        pw = self.page_password.pw_edit.text()
        vm = VaultManager(backend)
        # 密库位置（空=后端根目录）：新建决定建处，连接决定查找处
        self.vault_path = (
            self.page_backend_cfg.location_edit.text().strip().strip("/")
        )
        is_new = self.is_new_mode()
        filename_enc = self.page_enc.radio_on.isChecked()
        vault_name = self.page_password.name_edit.text().strip()
        recovery = self.page_password.recovery_edit.text().strip()

        def op():
            """后台重操作：建库/开库（含数秒级 PBKDF2 派生）。"""
            try:
                if is_new:
                    # 一次性建库并生成恢复码（派生 2 次，省掉复核重传）
                    meta, code = vm.create_vault_with_recovery(
                        pw, filename_enc, name=vault_name,
                        vault_path=self.vault_path,
                        progress_cb=self.progressed.emit,
                    )
                    return meta, pw, code
                if recovery:
                    meta = vm.open_vault_with_recovery(
                        recovery, self.vault_path,
                        progress_cb=self.progressed.emit,
                    )
                    if meta is None:
                        raise _OpError("恢复码无效，或该位置不存在带恢复码的Mi库")
                    return meta, vm.recovered_password or pw, ""
                meta = vm.open_vault(
                    pw, self.vault_path,
                    progress_cb=self.progressed.emit,
                )
                if meta is None:
                    # 区分"位置无密库"与"密码错误"，避免误导性报错
                    if not vm.has_vault(self.vault_path):
                        raise _OpError("该位置不存在Mi库，请检查密库位置")
                    raise _OpError("主密码错误，请重试")
                return meta, pw, ""
            except _OpError:
                raise
            except Exception as e:  # noqa: BLE001
                raise _OpError(
                    f"新建Mi库失败：{e}" if is_new else f"连接失败：{e}"
                )

        def on_done(result):
            meta, final_pw, code = result
            self._set_busy(False)
            self.metadata = meta
            self.recovery_code = code
            self.backend = backend
            self.session = Session(final_pw)

            # 保存本次连接参数（密码不落盘）
            if self.store is not None:
                self.store.set_backend_type(self.backend_type)
                if self.backend_type == "local":
                    self.store.set_local_dir(
                        self.page_backend_cfg.local_dir_edit.text().strip()
                    )
                elif self.backend_type == "webdav":
                    self.store.set_webdav_url(
                        self.page_backend_cfg.webdav_url_edit.text().strip()
                    )
                    self.store.set_webdav_user(
                        self.page_backend_cfg.webdav_user_edit.text().strip()
                    )

            self.finishedSetup.emit()
            super(InitWizard, self).accept()

        def on_error(msg: str):
            self._set_busy(False)
            self._error(msg or "未知错误")

        self._set_busy(True)
        # 同步模式（测试）原地执行；异步模式持有线程引用防 GC，
        # 完成回调在事件循环内触发，exec() 会在其后返回，引用不提前失效
        self._op_thread = run_busy(
            op, on_done, on_error, parent=self, sync=self.sync_ops,
        )
