"""页面容器：文件树 / 传输队列 / 设置。

密库信息页见 vault_info_page.py。四个页面由主窗口的
FluentWindow 导航栏切换（旧版 SidePanel 堆栈容器已退役）。
设置页包含外观、连接信息、缓存管理、传输设置、安全设置。
"""

from __future__ import annotations

import os
import shutil
import tempfile

from PySide6.QtCore import Signal, Qt
from PySide6.QtGui import QFont
from PySide6.QtWidgets import (
    QAbstractItemView,
    QFormLayout,
    QHBoxLayout,
    QInputDialog,
    QLabel,
    QListWidgetItem,
    QMessageBox,
    QVBoxLayout,
    QWidget,
)

# Fluent 组件（均继承自对应 Qt 原生控件，标准 API 全兼容）
from qfluentwidgets import (
    ComboBox,
    ComboBoxSettingCard,
    ExpandGroupSettingCard,
    FluentIcon,
    LineEdit,
    ListWidget,
    OptionsConfigItem,
    OptionsValidator,
    PrimaryPushButton,
    PushButton,
    PushSettingCard,
    ScrollArea,
    SettingCard,
    SettingCardGroup,
    SpinBox,
    SubtitleLabel,
    TitleLabel,
)

from cloudprism.gui.baidu_auth import BaiduAuthDialog
from cloudprism.gui.baidu_guide import BaiduGuideDialog
from cloudprism.gui.dir_tree_model import DirTreeModel
from cloudprism.gui.file_tree_view import FileTreeView
from cloudprism.gui.theme import semantic_color
from cloudprism.storage.baidu_backend import BaiduCredentialStore


def _unify_expand_font(card) -> None:
    """统一展开设置卡二级区的字体，与一级卡片观感对齐。

    应用级字号为 pt 单位（可随设置项调节），而库卡片标题经样式表固定
    14px；展开区原生控件若继承应用字体会显得偏大，故在卡片 ``view``
    容器上显式设定像素级字体（widget 级，不受后续 app.setFont 影响），
    子控件（输入框/按钮等）经字体继承链同步生效。
    部分 QLabel 未设置 objectName 时字体解析不到 view 级字体，
    另用 fontInfo 探测并逐个补设，确保展开区全部文字同字号。
    """
    font = QFont()
    # 字体族回退链：首选 Segoe UI Variable，缺失时回退到中文友好字体
    font.setFamilies(["Segoe UI Variable", "Segoe UI", "Microsoft YaHei UI"])
    font.setPixelSize(14)  # 与库卡片标题字号一致
    card.view.setFont(font)
    # 探测未被继承链覆盖的 QLabel（如 QFormLayout 行标签）并补设字体；
    # 库自身带 objectName 的标签（titleLabel/contentLabel）由样式表接管，不动
    for label in card.view.findChildren(QLabel):
        if label.objectName() in ("titleLabel", "contentLabel"):
            continue
        if label.fontInfo().pixelSize() != 14:
            label.setFont(font)


# ---------------------------------------------------------------------------
# 文件树页
# ---------------------------------------------------------------------------


class FilesPage(QWidget):
    """文件浏览器页：支持拖放的目录树视图。"""

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        lay = QVBoxLayout(self)
        lay.setContentsMargins(0, 0, 0, 0)

        # 使用自定义的 FileTreeView（支持拖入上传、拖出下载）
        self.tree = FileTreeView(self)
        self.tree.setEditTriggers(QAbstractItemView.NoEditTriggers)
        self.tree.setUniformRowHeights(True)
        lay.addWidget(self.tree)

    def set_model(self, model: DirTreeModel) -> None:
        """装载目录树模型。"""
        self.tree.setModel(model)


# ---------------------------------------------------------------------------
# 传输队列页
# ---------------------------------------------------------------------------


class TransfersPage(QWidget):
    """传输队列页：显示进行中和已完成的传输任务。"""

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        lay = QVBoxLayout(self)
        lay.setContentsMargins(12, 12, 12, 12)
        lay.setSpacing(8)

        header = QLabel("传输队列", self)
        header.setStyleSheet("font-weight: bold; font-size: 16px;")
        lay.addWidget(header)

        desc = QLabel("上传和下载任务将显示在此处", self)
        desc.setStyleSheet(f"color: {semantic_color('muted')}; font-size: 13px;")
        lay.addWidget(desc)

        self.task_list = ListWidget(self)
        self.task_list.setAlternatingRowColors(True)
        lay.addWidget(self.task_list)

    def add_task(self, name: str, direction: str) -> QListWidgetItem:
        """添加传输任务条目。"""
        icon_text = "↑" if direction == "upload" else "↓"
        item = QListWidgetItem(f"{icon_text} {name} — 等待中")
        self.task_list.addItem(item)
        return item

    def update_task(self, item: QListWidgetItem, progress: float) -> None:
        """更新任务进度。"""
        name = item.text().split(" — ")[0]
        item.setText(f"{name} — {int(progress * 100)}%")

    def finish_task(self, item: QListWidgetItem, success: bool = True) -> None:
        """标记任务完成或失败。"""
        name = item.text().split(" — ")[0]
        status = "完成" if success else "失败"
        item.setText(f"{name} — {status}")


# ---------------------------------------------------------------------------
# 设置页
# ---------------------------------------------------------------------------


class SettingsPage(QWidget):
    """设置面板：外观 + 连接信息 + 缓存 + 传输 + 安全 + 百度网盘 + 性能。"""

    # 信号
    cacheSettingsChanged = Signal(int, str)  # (cache_limit_mb, cache_path)
    clearCacheRequested = Signal()
    themeChanged = Signal(str)       # "dark" / "light" / "system"
    fontSizeChanged = Signal(int)    # 12 / 14 / 16 / 18
    reconnectRequested = Signal()    # 重新连接密库
    autoLockChanged = Signal()       # 自动锁定时长变更（供控制器同步定时器）
    vaultRenameRequested = Signal(str)  # 修改当前密库名称（参数为新名称）

    # 默认缓存配置
    DEFAULT_CACHE_LIMIT_MB = 512
    DEFAULT_CACHE_SUBDIR = "cloudprism_cache"

    def __init__(self, parent=None, baidu_store=None) -> None:
        super().__init__(parent)

        # 百度凭证存储（DPAPI 加密落盘）；测试可注入假存储
        self._baidu_store = baidu_store or BaiduCredentialStore()

        # 可滚动区域（Fluent 风格，透明无边框）
        scroll = ScrollArea(self)
        scroll.setWidgetResizable(True)
        scroll.setFrameShape(ScrollArea.NoFrame)
        scroll.setObjectName("settingsScrollArea")

        content = QWidget()
        lay = QVBoxLayout(content)
        lay.setContentsMargins(24, 16, 24, 24)
        lay.setSpacing(12)

        # 页头标题（Fluent 设置页风格）
        title = TitleLabel("设置", content)
        lay.addWidget(title)
        subtitle = SubtitleLabel("外观、连接、缓存与安全等选项", content)
        subtitle.setStyleSheet(f"color: {semantic_color('muted')};")
        lay.addWidget(subtitle)
        lay.addSpacing(8)

        # ---- 外观 ----
        appearance_group = SettingCardGroup("外观", content)

        self._theme_combo_card = ComboBoxSettingCard(
            OptionsConfigItem(
                "cloudprism", "theme", "system",
                OptionsValidator(["system", "dark", "light"]),
            ),
            FluentIcon.CONSTRACT, "主题",
            "跟随系统或手动切换明暗配色",
            texts=["跟随系统", "深色", "浅色"], parent=appearance_group,
        )
        self._theme_combo_card.comboBox.currentIndexChanged.connect(self._on_theme_changed)
        appearance_group.addSettingCard(self._theme_combo_card)

        self._font_size_card = ComboBoxSettingCard(
            OptionsConfigItem(
                "cloudprism", "font_size", 14,
                OptionsValidator([12, 14, 16, 18]),
            ),
            FluentIcon.FONT, "字体大小", "调整全局文字尺寸",
            texts=["小 (12px)", "中 (14px)", "大 (16px)", "特大 (18px)"],
            parent=appearance_group,
        )
        self._font_size_card.comboBox.setCurrentIndex(1)  # 默认中
        self._font_size_card.comboBox.currentIndexChanged.connect(self._on_font_size_changed)
        appearance_group.addSettingCard(self._font_size_card)

        lay.addWidget(appearance_group)

        # ---- 连接信息 ----
        conn_group = SettingCardGroup("连接信息", content)

        # 密库名称（随 Vault Marker 加密保存，可修改）
        self._vault_name_card = SettingCard(
            FluentIcon.TAG, "密库名称", None, conn_group)
        self._vault_name_label = QLabel("未连接", self._vault_name_card)
        self._vault_rename_btn = PushButton("修改…", self._vault_name_card)
        self._vault_rename_btn.setToolTip("名称随密库文件保存，跨设备可见")
        self._vault_rename_btn.clicked.connect(self._on_vault_rename_clicked)
        self._vault_name_card.hBoxLayout.addWidget(
            self._vault_name_label, 0, Qt.AlignmentFlag.AlignRight)
        self._vault_name_card.hBoxLayout.addSpacing(8)
        self._vault_name_card.hBoxLayout.addWidget(
            self._vault_rename_btn, 0, Qt.AlignmentFlag.AlignRight)
        self._vault_name_card.hBoxLayout.addSpacing(16)
        conn_group.addSettingCard(self._vault_name_card)

        self._backend_type_card = SettingCard(
            FluentIcon.LIBRARY, "后端类型", None, conn_group)
        self._backend_type_label = QLabel("未连接", self._backend_type_card)
        self._backend_type_card.hBoxLayout.addWidget(
            self._backend_type_label, 0, Qt.AlignmentFlag.AlignRight)
        conn_group.addSettingCard(self._backend_type_card)

        self._backend_path_card = SettingCard(
            FluentIcon.LINK, "路径 / URL", None, conn_group)
        self._backend_path_label = QLabel("-", self._backend_path_card)
        self._backend_path_label.setWordWrap(True)
        self._backend_path_card.hBoxLayout.addWidget(
            self._backend_path_label, 0, Qt.AlignmentFlag.AlignRight)
        conn_group.addSettingCard(self._backend_path_card)

        self._filename_enc_card = SettingCard(
            FluentIcon.INFO, "文件名加密", None, conn_group)
        self._filename_enc_label = QLabel("-", self._filename_enc_card)
        self._filename_enc_card.hBoxLayout.addWidget(
            self._filename_enc_label, 0, Qt.AlignmentFlag.AlignRight)
        conn_group.addSettingCard(self._filename_enc_card)

        self._reconnect_btn = PushSettingCard(
            "切换密库（打开向导）…", FluentIcon.UPDATE,
            "重新连接", "新建或连接其他密库；快速重连请用密库页的最近记录",
            parent=conn_group,
        )
        self._reconnect_btn.clicked.connect(self.reconnectRequested.emit)
        conn_group.addSettingCard(self._reconnect_btn)

        lay.addWidget(conn_group)

        # ---- 缓存 ----
        cache_group = SettingCardGroup("缓存", content)

        # 缓存大小限制（展开卡片内嵌 SpinBox：保留 setSuffix/valueChanged 等原生接口）
        cache_limit_card = ExpandGroupSettingCard(
            FluentIcon.HISTORY, "缓存大小限制",
            "流式代理与传输管线的内存/磁盘缓冲上限", parent=cache_group,
        )
        limit_row = QWidget(cache_limit_card)
        limit_lay = QHBoxLayout(limit_row)
        limit_lay.setContentsMargins(48, 6, 24, 6)
        limit_lay.setSpacing(8)
        self._cache_limit_spin = SpinBox(limit_row)
        self._cache_limit_spin.setRange(64, 4096)
        self._cache_limit_spin.setValue(self.DEFAULT_CACHE_LIMIT_MB)
        self._cache_limit_spin.setSuffix(" MB")
        self._cache_limit_spin.setToolTip("流式代理与传输管线的内存/磁盘缓冲上限")
        self._cache_limit_spin.valueChanged.connect(self._emit_cache_settings)
        limit_lay.addWidget(self._cache_limit_spin)
        limit_lay.addStretch()
        cache_limit_card.addGroupWidget(limit_row)
        _unify_expand_font(cache_limit_card)
        cache_group.addSettingCard(cache_limit_card)

        # 缓存位置（展开分组卡片：路径输入 + 浏览 / 占用 + 清除）
        cache_loc_card = ExpandGroupSettingCard(
            FluentIcon.FOLDER, "缓存位置", "缓存文件存放路径与磁盘占用",
            parent=cache_group,
        )
        cache_row1 = QWidget(cache_loc_card)
        row1_lay = QHBoxLayout(cache_row1)
        row1_lay.setContentsMargins(48, 6, 24, 6)
        row1_lay.setSpacing(8)
        self._cache_path_edit = LineEdit(cache_row1)
        self._cache_path_edit.setText(self._default_cache_path())
        self._cache_path_edit.setPlaceholderText("缓存文件存放路径")
        self._cache_path_edit.textChanged.connect(self._emit_cache_settings)
        row1_lay.addWidget(self._cache_path_edit, 1)
        browse_btn = PushButton("浏览…", cache_row1)
        browse_btn.clicked.connect(self._browse_cache_path)
        row1_lay.addWidget(browse_btn)
        cache_loc_card.addGroupWidget(cache_row1)

        cache_row2 = QWidget(cache_loc_card)
        row2_lay = QHBoxLayout(cache_row2)
        row2_lay.setContentsMargins(48, 6, 24, 6)
        row2_lay.setSpacing(8)
        self._cache_usage_label = QLabel("计算中…", cache_row2)
        self._cache_usage_label.setStyleSheet(f"color: {semantic_color('muted')};")
        row2_lay.addWidget(self._cache_usage_label)
        row2_lay.addStretch()
        clear_btn = PushButton("清除缓存", cache_row2)
        clear_btn.clicked.connect(self._on_clear_cache)
        row2_lay.addWidget(clear_btn)
        cache_loc_card.addGroupWidget(cache_row2)
        _unify_expand_font(cache_loc_card)
        cache_group.addSettingCard(cache_loc_card)

        lay.addWidget(cache_group)

        # ---- 传输 ----
        transfer_group = SettingCardGroup("传输", content)

        self._chunk_size_card = ComboBoxSettingCard(
            OptionsConfigItem(
                "cloudprism", "chunk_size", 1,
                OptionsValidator([0, 1, 2, 3]),
            ),
            FluentIcon.DOCUMENT, "分块大小",
            "上传/下载与加密分块的尺寸",
            texts=["256 KB", "512 KB", "1 MB", "4 MB"], parent=transfer_group,
        )
        self._chunk_size_card.comboBox.setCurrentIndex(1)  # 默认 512KB
        transfer_group.addSettingCard(self._chunk_size_card)

        # 并发传输数（预留功能：当前传输队列为串行）
        concurrent_card = ExpandGroupSettingCard(
            FluentIcon.SPEED_HIGH, "并发传输数（预留）",
            "当前版本传输任务串行执行", parent=transfer_group,
        )
        concurrent_row = QWidget(concurrent_card)
        c_lay = QHBoxLayout(concurrent_row)
        c_lay.setContentsMargins(48, 6, 24, 6)
        c_lay.setSpacing(8)
        self._concurrent_spin = SpinBox(concurrent_row)
        self._concurrent_spin.setRange(1, 4)
        self._concurrent_spin.setValue(1)
        # 并发传输为预留功能：单任务内多核加密已可充分利用 CPU，此处禁用交互
        self._concurrent_spin.setEnabled(False)
        self._concurrent_spin.setToolTip("预留功能：当前版本传输任务串行执行")
        c_lay.addWidget(self._concurrent_spin)
        c_lay.addStretch()
        concurrent_card.addGroupWidget(concurrent_row)
        _unify_expand_font(concurrent_card)
        transfer_group.addSettingCard(concurrent_card)

        lay.addWidget(transfer_group)

        # ---- 安全 ----
        security_group = SettingCardGroup("安全", content)

        self._auto_lock_card = ComboBoxSettingCard(
            OptionsConfigItem(
                "cloudprism", "auto_lock", 0,
                OptionsValidator([0, 1, 2, 3]),
            ),
            FluentIcon.VPN, "自动锁定",
            "无操作后自动锁定密库的时间",
            texts=["从不", "5 分钟", "15 分钟", "30 分钟"], parent=security_group,
        )
        security_group.addSettingCard(self._auto_lock_card)

        lay.addWidget(security_group)

        # ---- 百度网盘 ----
        baidu_group = SettingCardGroup("百度网盘", content)

        baidu_card = ExpandGroupSettingCard(
            FluentIcon.ROBOT, "百度网盘凭证",
            "AppKey / SecretKey 等开发者凭证（DPAPI 加密落盘）",
            parent=baidu_group,
        )
        baidu_body = QWidget(baidu_card)
        baidu_form = QFormLayout(baidu_body)
        baidu_form.setContentsMargins(48, 6, 24, 6)
        baidu_form.setHorizontalSpacing(12)
        baidu_form.setVerticalSpacing(8)

        self._baidu_appid_edit = LineEdit(baidu_body)
        self._baidu_appkey_edit = LineEdit(baidu_body)
        self._baidu_secret_edit = LineEdit(baidu_body)
        self._baidu_secret_edit.setEchoMode(LineEdit.EchoMode.Password)
        self._baidu_signkey_edit = LineEdit(baidu_body)
        self._baidu_signkey_edit.setEchoMode(LineEdit.EchoMode.Password)
        baidu_form.addRow("Appid：", self._baidu_appid_edit)
        baidu_form.addRow("AppKey：", self._baidu_appkey_edit)
        baidu_form.addRow("SecretKey：", self._baidu_secret_edit)
        baidu_form.addRow("SignKey（可选）：", self._baidu_signkey_edit)

        # 申请教程：按需查看，不主动弹出（扁平链接样式）
        guide_row = QHBoxLayout()
        self._baidu_guide_btn = PushButton("如何申请凭证…", baidu_body)
        self._baidu_guide_btn.setStyleSheet(
            f"color: {semantic_color('link')}; text-align: left; border: none; "
            "background: transparent; padding-left: 0;"
        )
        self._baidu_guide_btn.setCursor(Qt.CursorShape.PointingHandCursor)
        self._baidu_guide_btn.clicked.connect(self._show_baidu_guide)
        guide_row.addWidget(self._baidu_guide_btn)
        guide_row.addStretch()
        baidu_form.addRow(guide_row)

        # 检查 / 登录 / 清除 + 状态显示
        action_row = QHBoxLayout()
        self._baidu_check_btn = PushButton("检查", baidu_body)
        self._baidu_check_btn.setToolTip(
            "校验格式与网络连通性；凭证最终有效性由登录授权时百度服务器验证"
        )
        self._baidu_check_btn.clicked.connect(self._check_baidu)
        self._baidu_login_btn = PrimaryPushButton("登录百度账号…", baidu_body)
        self._baidu_login_btn.clicked.connect(self._login_baidu)
        self._baidu_clear_btn = PushButton("清除", baidu_body)
        self._baidu_clear_btn.clicked.connect(self._clear_baidu)
        self._baidu_status = QLabel("", baidu_body)
        self._baidu_status.setWordWrap(True)
        action_row.addWidget(self._baidu_check_btn)
        action_row.addWidget(self._baidu_login_btn)
        action_row.addWidget(self._baidu_clear_btn)
        action_row.addWidget(self._baidu_status, stretch=1)
        baidu_form.addRow(action_row)

        baidu_card.addGroupWidget(baidu_body)
        _unify_expand_font(baidu_card)
        baidu_group.addSettingCard(baidu_card)

        lay.addWidget(baidu_group)

        # ---- 性能 ----
        perf_group = SettingCardGroup("性能", content)

        total_cores = os.cpu_count() or 4
        cores_card = ExpandGroupSettingCard(
            FluentIcon.SPEED_HIGH, "加密最大内核数",
            f"系统共 {total_cores} 个逻辑核心", parent=perf_group,
        )
        cores_row = QWidget(cores_card)
        cores_lay = QHBoxLayout(cores_row)
        cores_lay.setContentsMargins(48, 6, 24, 6)
        cores_lay.setSpacing(8)
        self._max_cores_spin = SpinBox(cores_row)
        self._max_cores_spin.setRange(1, total_cores)
        self._max_cores_spin.setValue(max(1, total_cores - 2))  # 默认留 2 核给系统
        self._max_cores_spin.setSuffix(" 核")
        self._max_cores_spin.setToolTip(f"系统共 {total_cores} 个逻辑核心")
        cores_lay.addWidget(self._max_cores_spin)
        cores_lay.addStretch()
        cores_card.addGroupWidget(cores_row)
        _unify_expand_font(cores_card)
        perf_group.addSettingCard(cores_card)

        lay.addWidget(perf_group)

        lay.addStretch()

        scroll.setWidget(content)

        outer = QVBoxLayout(self)
        outer.setContentsMargins(0, 0, 0, 0)
        outer.addWidget(scroll)

        # 刷新缓存占用显示
        self._refresh_cache_usage()

        # 回填已保存的百度凭证并刷新授权状态
        self._load_baidu_credentials()

    # ------------------------------------------------------------------
    # 兼容别名（既有测试与持久化经旧属性名访问卡片内部控件）
    # ------------------------------------------------------------------

    @property
    def _theme_combo(self):
        """主题下拉框（指向主题设置卡片内部 ComboBox）。"""
        return self._theme_combo_card.comboBox

    @property
    def _font_size_combo(self):
        """字体大小下拉框（指向字号设置卡片内部 ComboBox）。"""
        return self._font_size_card.comboBox

    @property
    def _chunk_size_combo(self):
        """分块大小下拉框（指向分块设置卡片内部 ComboBox）。"""
        return self._chunk_size_card.comboBox

    @property
    def _auto_lock_combo(self):
        """自动锁定下拉框（指向自动锁定设置卡片内部 ComboBox）。"""
        return self._auto_lock_card.comboBox

    # ------------------------------------------------------------------
    # 百度网盘凭证（用户自输模式：不落代码仓库，DPAPI 加密落盘）
    # ------------------------------------------------------------------

    def _load_baidu_credentials(self) -> None:
        """回填已保存凭证并刷新授权状态显示。"""
        saved = self._baidu_store.load() or {}
        self._baidu_appid_edit.setText(saved.get("app_id", ""))
        self._baidu_appkey_edit.setText(saved.get("app_key", ""))
        self._baidu_secret_edit.setText(saved.get("secret_key", ""))
        self._baidu_signkey_edit.setText(saved.get("sign_key", ""))
        if saved.get("access_token"):
            self._set_baidu_status("已授权 ✓", "ok")
        elif saved.get("app_key"):
            self._set_baidu_status("已配置，未登录", "warn")
        else:
            self._set_baidu_status("未配置", "muted")

    def _set_baidu_status(self, text: str, color: str) -> None:
        """更新百度分组状态标签（color 为语义键，见 theme.semantic_color）。"""
        self._baidu_status.setText(text)
        self._baidu_status.setStyleSheet(f"color: {semantic_color(color)};")

    def _collect_baidu_credentials(self) -> dict | None:
        """收集并做格式检查；不合法返回 None（状态栏已提示）。"""
        creds = {
            "app_id": self._baidu_appid_edit.text().strip(),
            "app_key": self._baidu_appkey_edit.text().strip(),
            "secret_key": self._baidu_secret_edit.text().strip(),
            "sign_key": self._baidu_signkey_edit.text().strip(),
        }
        if not creds["app_key"] or not creds["secret_key"]:
            self._set_baidu_status("请先填写 AppKey 与 SecretKey", "err")
            return None
        labels = {"app_id": "Appid", "app_key": "AppKey",
                  "secret_key": "SecretKey", "sign_key": "SignKey"}
        for key, value in creds.items():
            if value and any(ch.isspace() for ch in value):
                self._set_baidu_status(f"{labels[key]} 不能包含空白字符", "err")
                return None
        return creds

    def _save_baidu_credentials(self, creds: dict) -> None:
        """持久化凭证（保留存储中已有的 token 字段）。"""
        merged = dict(self._baidu_store.load() or {})
        merged.update(creds)
        self._baidu_store.save(merged)

    def _check_baidu(self) -> bool:
        """检查：格式 → 连通性 → （已授权时）token 实测。

        返回格式检查是否通过（供登录动作把关）。百度 OAuth 无
        client_credentials 模式，授权前服务器端无法验证凭证真伪，
        最终有效性在登录换取 token 时验证。
        """
        creds = self._collect_baidu_credentials()
        if creds is None:
            return False

        import requests

        self._baidu_check_btn.setEnabled(False)
        self._set_baidu_status("检查中…", "muted")
        try:
            # 连通性探测（不致命：允许离线填表，稍后再试）
            try:
                requests.head("https://openapi.baidu.com", timeout=4)
            except Exception as e:  # noqa: BLE001
                self._save_baidu_credentials(creds)
                self._set_baidu_status(
                    f"网络不可达：{e}（格式检查已通过，凭证已保存）", "warn"
                )
                return True

            # 已授权则用 uinfo 接口实测 token 并显示账号信息
            token = (self._baidu_store.load() or {}).get("access_token", "")
            if token:
                try:
                    r = requests.get(
                        "https://pan.baidu.com/rest/2.0/xpan/nas",
                        params={"method": "uinfo", "access_token": token},
                        timeout=4,
                    )
                    data = r.json()
                except Exception:  # noqa: BLE001
                    data = {}
                if "uname" in data:
                    self._save_baidu_credentials(creds)
                    self._set_baidu_status(
                        f"已授权 ✓，账号：{data.get('uname', '-')}", "ok"
                    )
                    return True
                self._set_baidu_status("token 已失效，请重新登录", "warn")

            self._save_baidu_credentials(creds)
            if token:
                return True  # token 失效提示已在上方显示，格式仍算通过
            self._set_baidu_status(
                "✓ 格式检查通过，可点击「登录百度账号…」（最终有效性由授权时"
                "百度服务器验证）",
                "ok",
            )
            return True
        finally:
            self._baidu_check_btn.setEnabled(True)

    def _login_baidu(self) -> None:
        """格式把关后打开授权对话框，登录用户的百度账号。"""
        creds = self._collect_baidu_credentials()
        if creds is None:
            return
        self._save_baidu_credentials(creds)
        dlg = BaiduAuthDialog(
            store=self._baidu_store,
            parent=self,
            prefill=creds,
            show_credentials_form=False,
        )
        if dlg.exec() and dlg.token_data is not None:
            self._set_baidu_status("已授权 ✓", "ok")

    def _clear_baidu(self) -> None:
        """清除本机凭证与授权（确认框）。"""
        ret = QMessageBox.question(
            self,
            "清除百度网盘凭证",
            "确定删除本机保存的百度网盘凭证与授权？",
        )
        if ret != QMessageBox.StandardButton.Yes:
            return
        self._baidu_store.clear()
        for edit in (
            self._baidu_appid_edit,
            self._baidu_appkey_edit,
            self._baidu_secret_edit,
            self._baidu_signkey_edit,
        ):
            edit.clear()
        self._set_baidu_status("未配置", "muted")

    def _show_baidu_guide(self) -> None:
        """展示凭证申请教程（按需查看，不主动弹出）。"""
        dlg = BaiduGuideDialog(parent=self)
        dlg.exec()

    # ------------------------------------------------------------------
    # 公开方法
    # ------------------------------------------------------------------

    def update_connection_info(
        self,
        backend_type: str,
        backend_path: str,
        filename_enc: bool,
        vault_name: str = "-",
    ) -> None:
        """更新连接信息（含自定义密库名称）显示。"""
        self._vault_name_label.setText(vault_name)
        self._backend_type_label.setText(backend_type)
        self._backend_path_label.setText(backend_path)
        self._filename_enc_label.setText("开" if filename_enc else "关")
        self._backend_path_label.setWordWrap(True)

    def _on_vault_rename_clicked(self) -> None:
        """密库名称修改按钮：弹输入框收集新名称，非空且变化时发射信号。"""
        current = self._vault_name_label.text()
        if current in ("", "-", "未连接"):
            return
        new_name, ok = QInputDialog.getText(
            self,
            "修改密库名称",
            "新名称（随密库文件保存，最多 32 字符）：",
            text=current,
        )
        new_name = (new_name or "").strip()
        if ok and new_name and new_name != current:
            self.vaultRenameRequested.emit(new_name[:32])

    @property
    def cache_limit_mb(self) -> int:
        """当前缓存大小限制（MB）。"""
        return self._cache_limit_spin.value()

    @property
    def cache_path(self) -> str:
        """当前缓存路径。"""
        return self._cache_path_edit.text().strip()

    @property
    def max_cores(self) -> int:
        """加密最大内核数。"""
        return self._max_cores_spin.value()

    @property
    def chunk_size(self) -> int:
        """当前分块大小（字节）。"""
        sizes = [256 * 1024, 512 * 1024, 1 << 20, 4 << 20]
        idx = self._chunk_size_combo.currentIndex()
        return sizes[idx] if 0 <= idx < len(sizes) else sizes[1]

    @property
    def auto_lock_minutes(self) -> int:
        """自动锁定超时（分钟），0 表示从不。"""
        minutes = [0, 5, 15, 30]
        idx = self._auto_lock_combo.currentIndex()
        return minutes[idx] if 0 <= idx < len(minutes) else 0

    # ------------------------------------------------------------------
    # 持久化
    # ------------------------------------------------------------------

    def attach_store(self, store) -> None:
        """接入持久化存储：载入已保存值，并为各控件接线自动保存。"""
        self._store = store

        # 载入时屏蔽信号，避免触发副作用（如缓存信号重算）
        widgets = (
            self._theme_combo,
            self._font_size_combo,
            self._cache_limit_spin,
            self._cache_path_edit,
            self._chunk_size_combo,
            self._max_cores_spin,
            self._auto_lock_combo,
        )
        for w in widgets:
            w.blockSignals(True)
        self._theme_combo.setCurrentIndex(store.theme_index())
        # 字体大小 -> 下拉索引（12/14/16/18）
        sizes = [12, 14, 16, 18]
        fs = store.font_size()
        self._font_size_combo.setCurrentIndex(
            sizes.index(fs) if fs in sizes else 1
        )
        self._cache_limit_spin.setValue(store.cache_limit_mb())
        if store.cache_path():
            self._cache_path_edit.setText(store.cache_path())
        self._chunk_size_combo.setCurrentIndex(store.chunk_index())
        saved_cores = store.max_cores()
        if saved_cores > 0:
            self._max_cores_spin.setValue(
                max(self._max_cores_spin.minimum(),
                    min(saved_cores, self._max_cores_spin.maximum()))
            )
        self._auto_lock_combo.setCurrentIndex(store.auto_lock_index())
        for w in widgets:
            w.blockSignals(False)
        # 缓存路径可能变更，刷新占用显示与信号同步
        self._refresh_cache_usage()
        self._emit_cache_settings()

        # 变更即保存（下次启动可恢复）
        self._theme_combo.currentIndexChanged.connect(store.set_theme_index)
        self._font_size_combo.currentIndexChanged.connect(
            lambda i: store.set_font_size(sizes[i]) if 0 <= i < len(sizes) else None
        )
        self._cache_limit_spin.valueChanged.connect(store.set_cache_limit_mb)
        self._cache_path_edit.textChanged.connect(store.set_cache_path)
        self._chunk_size_combo.currentIndexChanged.connect(store.set_chunk_index)
        self._max_cores_spin.valueChanged.connect(store.set_max_cores)
        self._auto_lock_combo.currentIndexChanged.connect(store.set_auto_lock_index)
        self._auto_lock_combo.currentIndexChanged.connect(
            lambda _i: self.autoLockChanged.emit()
        )

    # ------------------------------------------------------------------
    # 内部方法
    # ------------------------------------------------------------------

    def _on_theme_changed(self, index: int) -> None:
        """主题切换。"""
        themes = ["system", "dark", "light"]
        if 0 <= index < len(themes):
            self.themeChanged.emit(themes[index])

    def _on_font_size_changed(self, index: int) -> None:
        """字体大小切换。"""
        sizes = [12, 14, 16, 18]
        if 0 <= index < len(sizes):
            self.fontSizeChanged.emit(sizes[index])

    def _default_cache_path(self) -> str:
        """默认缓存路径。"""
        return os.path.join(tempfile.gettempdir(), self.DEFAULT_CACHE_SUBDIR)

    def _browse_cache_path(self) -> None:
        """浏览选择缓存目录。"""
        from PySide6.QtWidgets import QFileDialog

        path = QFileDialog.getExistingDirectory(self, "选择缓存目录")
        if path:
            self._cache_path_edit.setText(path)

    def _emit_cache_settings(self) -> None:
        """发射缓存设置变更信号。"""
        self.cacheSettingsChanged.emit(
            self._cache_limit_spin.value(),
            self._cache_path_edit.text().strip(),
        )

    def _refresh_cache_usage(self) -> None:
        """计算并显示当前缓存目录磁盘占用。"""
        cache_dir = self._cache_path_edit.text().strip()
        if not cache_dir or not os.path.isdir(cache_dir):
            self._cache_usage_label.setText("0 B")
            return
        total = 0
        for dirpath, _dirnames, filenames in os.walk(cache_dir):
            for f in filenames:
                fp = os.path.join(dirpath, f)
                try:
                    total += os.path.getsize(fp)
                except OSError:
                    pass
        self._cache_usage_label.setText(_human_size(total))

    def _on_clear_cache(self) -> None:
        """清除缓存目录内容。"""
        cache_dir = self._cache_path_edit.text().strip()
        if cache_dir and os.path.isdir(cache_dir):
            shutil.rmtree(cache_dir, ignore_errors=True)
            os.makedirs(cache_dir, exist_ok=True)
        self._refresh_cache_usage()
        self.clearCacheRequested.emit()


def _human_size(n: int) -> str:
    """字节数 -> 人类可读大小。"""
    size = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if size < 1024 or unit == "TB":
            return f"{int(size)} {unit}" if unit == "B" else f"{size:.1f} {unit}"
        size /= 1024
    return f"{n} B"
