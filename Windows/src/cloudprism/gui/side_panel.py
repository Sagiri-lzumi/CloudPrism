"""侧面板容器：文件树 / 传输队列 / 密库信息 / 设置。

QStackedWidget 容纳四个子面板，由活动栏（ActivityBar）切换。
设置页包含外观、连接信息、缓存管理、传输设置、安全设置。
"""

from __future__ import annotations

import os
import shutil
import tempfile

from PySide6.QtCore import Signal, Qt
from PySide6.QtWidgets import (
    QAbstractItemView,
    QComboBox,
    QFormLayout,
    QGroupBox,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QListWidget,
    QListWidgetItem,
    QMessageBox,
    QPushButton,
    QSpinBox,
    QStackedWidget,
    QVBoxLayout,
    QWidget,
)

from cloudprism.gui.baidu_auth import BaiduAuthDialog
from cloudprism.gui.baidu_guide import BaiduGuideDialog
from cloudprism.gui.dir_tree_model import DirTreeModel
from cloudprism.gui.file_tree_view import FileTreeView
from cloudprism.gui.vault_info_page import VaultInfoPage
from cloudprism.storage.baidu_backend import BaiduCredentialStore


# ---------------------------------------------------------------------------
# 侧面板容器
# ---------------------------------------------------------------------------


class SidePanel(QStackedWidget):
    """侧面板：四页切换（文件树 / 传输队列 / 密库信息 / 设置）。"""

    # 页面索引
    PAGE_FILES = 0
    PAGE_TRANSFERS = 1
    PAGE_VAULTS = 2
    PAGE_SETTINGS = 3

    def __init__(self, parent=None) -> None:
        super().__init__(parent)

        # 文件树页
        self.files_page = FilesPage()
        self.addWidget(self.files_page)

        # 传输队列页
        self.transfers_page = TransfersPage()
        self.addWidget(self.transfers_page)

        # 密库信息页
        self.vault_info_page = VaultInfoPage()
        self.addWidget(self.vault_info_page)

        # 设置页
        self.settings_page = SettingsPage()
        self.addWidget(self.settings_page)

        # 默认显示文件页
        self.setCurrentIndex(self.PAGE_FILES)

    def show_page(self, page_id: str) -> None:
        """按活动栏页面标识切换。"""
        mapping = {
            "files": self.PAGE_FILES,
            "transfers": self.PAGE_TRANSFERS,
            "vaults": self.PAGE_VAULTS,
            "settings": self.PAGE_SETTINGS,
        }
        self.setCurrentIndex(mapping.get(page_id, self.PAGE_FILES))


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
        header.setStyleSheet("font-weight: bold; font-size: 16px; color: #1a1a1a;")
        lay.addWidget(header)

        desc = QLabel("上传和下载任务将显示在此处", self)
        desc.setStyleSheet("color: #5c5c5c; font-size: 13px;")
        lay.addWidget(desc)

        self.task_list = QListWidget(self)
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

    # 默认缓存配置
    DEFAULT_CACHE_LIMIT_MB = 512
    DEFAULT_CACHE_SUBDIR = "cloudprism_cache"

    def __init__(self, parent=None, baidu_store=None) -> None:
        super().__init__(parent)

        # 百度凭证存储（DPAPI 加密落盘）；测试可注入假存储
        self._baidu_store = baidu_store or BaiduCredentialStore()

        # 可滚动区域
        from PySide6.QtWidgets import QScrollArea
        scroll = QScrollArea(self)
        scroll.setWidgetResizable(True)
        scroll.setFrameShape(QScrollArea.NoFrame)

        content = QWidget()
        lay = QVBoxLayout(content)
        lay.setContentsMargins(12, 12, 12, 12)
        lay.setSpacing(16)

        # ---- 外观设置 ----
        appearance_group = QGroupBox("外观", content)
        appearance_form = QFormLayout(appearance_group)

        self._theme_combo = QComboBox(self)
        self._theme_combo.addItems(["跟随系统", "深色", "浅色"])
        self._theme_combo.currentIndexChanged.connect(self._on_theme_changed)
        appearance_form.addRow("主题：", self._theme_combo)

        self._font_size_combo = QComboBox(self)
        self._font_size_combo.addItems(["小 (12px)", "中 (14px)", "大 (16px)", "特大 (18px)"])
        self._font_size_combo.setCurrentIndex(1)  # 默认中
        self._font_size_combo.currentIndexChanged.connect(self._on_font_size_changed)
        appearance_form.addRow("字体大小：", self._font_size_combo)

        lay.addWidget(appearance_group)

        # ---- 连接信息 ----
        conn_group = QGroupBox("连接信息", content)
        conn_form = QFormLayout(conn_group)

        self._backend_type_label = QLabel("未连接", self)
        conn_form.addRow("后端类型：", self._backend_type_label)

        self._backend_path_label = QLabel("-", self)
        conn_form.addRow("路径/URL：", self._backend_path_label)

        self._filename_enc_label = QLabel("-", self)
        conn_form.addRow("文件名加密：", self._filename_enc_label)

        btn_row = QHBoxLayout()
        self._reconnect_btn = QPushButton("切换密库（打开向导）…", self)
        self._reconnect_btn.setToolTip(
            "打开初始化向导新建或连接密库；快速重连请用密库页的最近记录"
        )
        self._reconnect_btn.clicked.connect(self.reconnectRequested.emit)
        btn_row.addWidget(self._reconnect_btn)
        btn_row.addStretch()
        conn_form.addRow(btn_row)

        lay.addWidget(conn_group)

        # ---- 缓存设置 ----
        cache_group = QGroupBox("缓存设置", content)
        cache_form = QFormLayout(cache_group)

        self._cache_limit_spin = QSpinBox(self)
        self._cache_limit_spin.setRange(64, 4096)
        self._cache_limit_spin.setValue(self.DEFAULT_CACHE_LIMIT_MB)
        self._cache_limit_spin.setSuffix(" MB")
        self._cache_limit_spin.setToolTip("流式代理与传输管线的内存/磁盘缓冲上限")
        self._cache_limit_spin.valueChanged.connect(self._emit_cache_settings)
        cache_form.addRow("缓存大小限制：", self._cache_limit_spin)

        cache_path_row = QHBoxLayout()
        self._cache_path_edit = QLineEdit(self)
        self._cache_path_edit.setText(self._default_cache_path())
        self._cache_path_edit.setPlaceholderText("缓存文件存放路径")
        self._cache_path_edit.textChanged.connect(self._emit_cache_settings)
        cache_path_row.addWidget(self._cache_path_edit)

        browse_btn = QPushButton("浏览…", self)
        browse_btn.clicked.connect(self._browse_cache_path)
        cache_path_row.addWidget(browse_btn)
        cache_form.addRow("缓存位置：", cache_path_row)

        self._cache_usage_label = QLabel("计算中…", self)
        cache_form.addRow("当前缓存占用：", self._cache_usage_label)

        clear_btn = QPushButton("清除缓存", self)
        clear_btn.clicked.connect(self._on_clear_cache)
        clear_row = QHBoxLayout()
        clear_row.addStretch()
        clear_row.addWidget(clear_btn)
        cache_form.addRow(clear_row)

        lay.addWidget(cache_group)

        # ---- 传输设置 ----
        transfer_group = QGroupBox("传输", content)
        transfer_form = QFormLayout(transfer_group)

        self._chunk_size_combo = QComboBox(self)
        self._chunk_size_combo.addItems(["256 KB", "512 KB", "1 MB", "4 MB"])
        self._chunk_size_combo.setCurrentIndex(1)  # 默认 512KB
        transfer_form.addRow("分块大小：", self._chunk_size_combo)

        self._concurrent_spin = QSpinBox(self)
        self._concurrent_spin.setRange(1, 4)
        self._concurrent_spin.setValue(1)
        # 并发传输为预留功能：当前传输队列为串行（单任务内多核加密已可充分利用 CPU）
        self._concurrent_spin.setEnabled(False)
        self._concurrent_spin.setToolTip("预留功能：当前版本传输任务串行执行")
        transfer_form.addRow("并发传输数（预留）：", self._concurrent_spin)

        lay.addWidget(transfer_group)

        # ---- 安全设置 ----
        security_group = QGroupBox("安全", content)
        security_form = QFormLayout(security_group)

        self._auto_lock_combo = QComboBox(self)
        self._auto_lock_combo.addItems(["从不", "5 分钟", "15 分钟", "30 分钟"])
        self._auto_lock_combo.setToolTip("无操作后自动锁定密库的时间")
        security_form.addRow("自动锁定：", self._auto_lock_combo)

        lay.addWidget(security_group)

        # ---- 百度网盘 ----
        baidu_group = QGroupBox("百度网盘", content)
        baidu_form = QFormLayout(baidu_group)

        self._baidu_appid_edit = QLineEdit(baidu_group)
        self._baidu_appkey_edit = QLineEdit(baidu_group)
        self._baidu_secret_edit = QLineEdit(baidu_group)
        self._baidu_secret_edit.setEchoMode(QLineEdit.EchoMode.Password)
        self._baidu_signkey_edit = QLineEdit(baidu_group)
        self._baidu_signkey_edit.setEchoMode(QLineEdit.EchoMode.Password)
        baidu_form.addRow("Appid：", self._baidu_appid_edit)
        baidu_form.addRow("AppKey：", self._baidu_appkey_edit)
        baidu_form.addRow("SecretKey：", self._baidu_secret_edit)
        baidu_form.addRow("SignKey（可选）：", self._baidu_signkey_edit)

        # 申请教程：按需查看，不主动弹出
        guide_row = QHBoxLayout()
        self._baidu_guide_btn = QPushButton("如何申请凭证…", baidu_group)
        self._baidu_guide_btn.setFlat(True)
        self._baidu_guide_btn.setStyleSheet(
            "color: #06c; text-align: left; border: none;"
        )
        self._baidu_guide_btn.setCursor(Qt.CursorShape.PointingHandCursor)
        self._baidu_guide_btn.clicked.connect(self._show_baidu_guide)
        guide_row.addWidget(self._baidu_guide_btn)
        guide_row.addStretch()
        baidu_form.addRow(guide_row)

        # 检查 / 登录 / 清除 + 状态显示
        action_row = QHBoxLayout()
        self._baidu_check_btn = QPushButton("检查", baidu_group)
        self._baidu_check_btn.setToolTip(
            "校验格式与网络连通性；凭证最终有效性由登录授权时百度服务器验证"
        )
        self._baidu_check_btn.clicked.connect(self._check_baidu)
        self._baidu_login_btn = QPushButton("登录百度账号…", baidu_group)
        self._baidu_login_btn.clicked.connect(self._login_baidu)
        self._baidu_clear_btn = QPushButton("清除", baidu_group)
        self._baidu_clear_btn.clicked.connect(self._clear_baidu)
        self._baidu_status = QLabel("", baidu_group)
        self._baidu_status.setWordWrap(True)
        action_row.addWidget(self._baidu_check_btn)
        action_row.addWidget(self._baidu_login_btn)
        action_row.addWidget(self._baidu_clear_btn)
        action_row.addWidget(self._baidu_status, stretch=1)
        baidu_form.addRow(action_row)

        lay.addWidget(baidu_group)

        # ---- 性能设置 ----
        perf_group = QGroupBox("性能", content)
        perf_form = QFormLayout(perf_group)

        total_cores = os.cpu_count() or 4
        self._max_cores_spin = QSpinBox(self)
        self._max_cores_spin.setRange(1, total_cores)
        self._max_cores_spin.setValue(max(1, total_cores - 2))  # 默认留 2 核给系统
        self._max_cores_spin.setToolTip(f"系统共 {total_cores} 个逻辑核心")
        perf_form.addRow("加密最大内核数：", self._max_cores_spin)

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
            self._set_baidu_status("已授权 ✓", "#0a0")
        elif saved.get("app_key"):
            self._set_baidu_status("已配置，未登录", "#c80")
        else:
            self._set_baidu_status("未配置", "#5c5c5c")

    def _set_baidu_status(self, text: str, color: str) -> None:
        """更新百度分组状态标签。"""
        self._baidu_status.setText(text)
        self._baidu_status.setStyleSheet(f"color: {color};")

    def _collect_baidu_credentials(self) -> dict | None:
        """收集并做格式检查；不合法返回 None（状态栏已提示）。"""
        creds = {
            "app_id": self._baidu_appid_edit.text().strip(),
            "app_key": self._baidu_appkey_edit.text().strip(),
            "secret_key": self._baidu_secret_edit.text().strip(),
            "sign_key": self._baidu_signkey_edit.text().strip(),
        }
        if not creds["app_key"] or not creds["secret_key"]:
            self._set_baidu_status("请先填写 AppKey 与 SecretKey", "#c00")
            return None
        labels = {"app_id": "Appid", "app_key": "AppKey",
                  "secret_key": "SecretKey", "sign_key": "SignKey"}
        for key, value in creds.items():
            if value and any(ch.isspace() for ch in value):
                self._set_baidu_status(f"{labels[key]} 不能包含空白字符", "#c00")
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
        self._set_baidu_status("检查中…", "#5c5c5c")
        try:
            # 连通性探测（不致命：允许离线填表，稍后再试）
            try:
                requests.head("https://openapi.baidu.com", timeout=4)
            except Exception as e:  # noqa: BLE001
                self._save_baidu_credentials(creds)
                self._set_baidu_status(
                    f"网络不可达：{e}（格式检查已通过，凭证已保存）", "#c80"
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
                        f"已授权 ✓，账号：{data.get('uname', '-')}", "#0a0"
                    )
                    return True
                self._set_baidu_status("token 已失效，请重新登录", "#c80")

            self._save_baidu_credentials(creds)
            if token:
                return True  # token 失效提示已在上方显示，格式仍算通过
            self._set_baidu_status(
                "✓ 格式检查通过，可点击「登录百度账号…」（最终有效性由授权时"
                "百度服务器验证）",
                "#0a0",
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
            self._set_baidu_status("已授权 ✓", "#0a0")

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
        self._set_baidu_status("未配置", "#5c5c5c")

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
    ) -> None:
        """更新连接信息显示。"""
        self._backend_type_label.setText(backend_type)
        self._backend_path_label.setText(backend_path)
        self._filename_enc_label.setText("开" if filename_enc else "关")
        self._backend_path_label.setWordWrap(True)

    def revert_theme(self) -> None:
        """回退主题下拉框到「浅色」（当前版本唯一可用主题）。

        延迟到下一事件循环执行：本次信号发射中持久化插槽会在本方法
        返回后再次写入「深色」索引，延迟回退可保证最终状态正确。
        """
        from PySide6.QtCore import QTimer

        def _do() -> None:
            self._theme_combo.blockSignals(True)
            self._theme_combo.setCurrentIndex(2)
            self._theme_combo.blockSignals(False)
            if getattr(self, "_store", None) is not None:
                self._store.set_theme_index(2)

        QTimer.singleShot(0, self, _do)

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
