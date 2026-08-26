"""CloudPrism 主窗口（Fluent 现代化骨架）。

基于 QFluentWidgets 的 FluentWindow：
  - 左侧导航栏（文件 / 传输 / 密库 / 设置）替代旧版自绘活动栏
  - 文件页 = 文件树 + 预览面板（QSplitter 组合体）
  - 传输 / 密库 / 设置页全宽
  - 底部：传输进度条 + 状态条（连接状态 | 速度 | 缓存 | CPU）

为兼容既有测试与 AppController 接线，保留 ``activity_bar`` /
``side_panel`` / ``menuBar()`` / ``_content_widget`` 等旧接口的
轻量适配层（详见各适配类注释）。

  [导航结构]
  +------+-----------------+------------------------------------------+
  |      |                 |                                          |
  | 导航 |   文件树        |           预览区（仅文件页）             |
  | 栏   |                 |                                          |
  +------+-----------------+------------------------------------------+
  [ 传输进度条（传输时显示） ]
  [ 状态条：连接状态 | 速度 | 缓存 | CPU ]
"""

from __future__ import annotations

from PySide6.QtCore import QObject, Qt, Signal
from PySide6.QtGui import QAction, QKeySequence, QShortcut
from PySide6.QtWidgets import (
    QFrame,
    QHBoxLayout,
    QMenu,
    QSplitter,
    QVBoxLayout,
    QWidget,
)
from qfluentwidgets import (
    BodyLabel,
    CaptionLabel,
    FluentIcon,
    FluentWindow,
    NavigationItemPosition,
    NavigationPushButton,
    RoundMenu,
)

from cloudprism.gui.perf_monitor import format_cache, format_cpu, format_speed
from cloudprism.gui.preview_panel import PreviewPanel
from cloudprism.gui.side_panel import FilesPage, SettingsPage, TransfersPage
from cloudprism.gui.theme import semantic_color
from cloudprism.gui.transfer_progress_bar import TransferProgressBar
from cloudprism.gui.vault_info_page import VaultInfoPage


# ---------------------------------------------------------------------------
# 兼容适配层（旧接口 -> FluentWindow 结构）
# ---------------------------------------------------------------------------


class _ActivityBarAdapter(QObject):
    """活动栏适配对象：把导航栏切换转发为旧版 ``currentChanged(page_id)`` 语义。

    FluentWindow 的导航栏取代了自绘活动栏；既有测试与代码仍通过
    ``activity_bar.currentChanged`` 信号与页面常量工作，此适配层桥接两者。
    """

    # 当前选中项变更信号，参数为页面标识
    currentChanged = Signal(str)

    # 页面标识常量（与旧 ActivityBar 一致）
    PAGE_FILES = "files"
    PAGE_TRANSFERS = "transfers"
    PAGE_VAULTS = "vaults"
    PAGE_SETTINGS = "settings"

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self._current = self.PAGE_FILES

    @property
    def current_page(self) -> str:
        """当前选中的页面标识。"""
        return self._current

    def _set_current(self, page_id: str) -> None:
        """由主窗口在导航切换时调用（避免信号回环）。"""
        if page_id != self._current:
            self._current = page_id
            self.currentChanged.emit(page_id)


class _SidePanelAdapter:
    """侧面板适配对象：暴露旧版 ``side_panel`` 的页面访问与切换接口。

    FluentWindow 的 stackedWidget 承载四个页面；此对象仅做转发，
    不是 QWidget（页面真实父级为主窗口的堆栈容器）。
    """

    # 页面索引（与旧 SidePanel 常量一致，等于堆栈注册顺序）
    PAGE_FILES = 0
    PAGE_TRANSFERS = 1
    PAGE_VAULTS = 2
    PAGE_SETTINGS = 3

    def __init__(self, window: "MainWindow") -> None:
        self._window = window
        # 页面引用（供 AppController 与测试直接访问）
        self.files_page = window._files_page
        self.transfers_page = window.transfers_page
        self.vault_info_page = window.vault_info_page
        self.settings_page = window.settings_page

    def show_page(self, page_id: str) -> None:
        """按页面标识切换（转发为导航切换）。"""
        mapping = {
            "files": self.files_page._fluent_container,  # 文件页为组合容器
            "transfers": self.transfers_page,
            "vaults": self.vault_info_page,
            "settings": self.settings_page,
        }
        target = mapping.get(page_id)
        if target is not None:
            self._window.switchTo(target)

    def currentIndex(self) -> int:
        return self._window.stackedWidget.currentIndex()

    def setCurrentIndex(self, index: int) -> None:
        self._window.stackedWidget.setCurrentIndex(index)


class _MenuBarShim:
    """菜单栏兼容对象：FluentWindow 无菜单栏，仅保留菜单结构供查询/测试。"""

    def __init__(self, menus: list[QMenu]) -> None:
        self._menus = menus

    def actions(self) -> list[QMenu]:
        return list(self._menus)


class MainWindow(FluentWindow):
    """CloudPrism 主窗口（Fluent 风格）。"""

    # ---- 信号 ----
    initRequested = Signal()
    uploadRequested = Signal(str)     # 参数：目标目录（选中文件时为其所在目录）
    downloadRequested = Signal(str)   # 参数：选中文件路径（空串表示未选中）
    playRequested = Signal(str)
    refreshRequested = Signal()       # 刷新文件树（菜单/快捷键）
    lockRequested = Signal()          # 锁定密库（菜单/快捷键）

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self.setWindowTitle("CloudPrism")
        self.resize(1200, 700)

        self._build_pages()
        self._build_navigation()
        self._build_bottom_bars()
        self._build_menu_actions()

        # 兼容适配层（旧接口桥接）
        self.activity_bar = _ActivityBarAdapter(self)
        self.activity_bar.currentChanged.connect(self._on_activity_page_changed)
        self.stackedWidget.currentChanged.connect(self._on_stacked_changed)
        self.side_panel = _SidePanelAdapter(self)

    # ==================================================================
    # 页面构建与导航注册
    # ==================================================================

    def _build_pages(self) -> None:
        """构建四个页面；文件页为「文件树 + 预览」组合容器。"""
        # 文件树页与预览面板
        self._files_page = FilesPage(self)
        self.preview_panel = PreviewPanel(self)

        # 文件页组合容器：QSplitter（文件树 : 预览 = 1 : 2）
        self._files_container = QWidget(self)
        self._files_container.setObjectName("filesInterface")
        # 供适配层映射页面标识
        self._files_page._fluent_container = self._files_container
        container_lay = QHBoxLayout(self._files_container)
        container_lay.setContentsMargins(0, 0, 0, 0)
        container_lay.setSpacing(0)
        splitter = QSplitter(Qt.Orientation.Horizontal, self._files_container)
        splitter.addWidget(self._files_page)
        splitter.addWidget(self.preview_panel)
        splitter.setStretchFactor(0, 1)
        splitter.setStretchFactor(1, 2)
        splitter.setSizes([400, 800])
        container_lay.addWidget(splitter)

        # 其他三页（直接作为导航界面）
        self.transfers_page = TransfersPage(self)
        self.transfers_page.setObjectName("transfersInterface")
        self.vault_info_page = VaultInfoPage(self)
        self.vault_info_page.setObjectName("vaultsInterface")
        self.settings_page = SettingsPage(self)
        self.settings_page.setObjectName("settingsInterface")

    def _build_navigation(self) -> None:
        """注册四个导航界面（设置置于底部）。"""
        self.addSubInterface(self._files_container, FluentIcon.FOLDER, "文件")
        self.addSubInterface(self.transfers_page, FluentIcon.DOWNLOAD, "传输")
        self.addSubInterface(self.vault_info_page, FluentIcon.LIBRARY, "密库")
        self.addSubInterface(
            self.settings_page,
            FluentIcon.SETTING,
            "设置",
            position=NavigationItemPosition.BOTTOM,
        )

    # ==================================================================
    # 底部：传输进度条 + 状态条
    # ==================================================================

    def _build_bottom_bars(self) -> None:
        """把进度条与状态条挂到右侧内容列底部。

        FluentWindow 的 widgetLayout 为水平布局，需把堆栈容器包进
        垂直容器后再挂底部条，避免水平排布错位。
        """
        # 传输进度条（默认隐藏，传输时显示）
        self.transfer_progress_inline = TransferProgressBar(self)

        # 状态条（连接状态 | 速度 | 缓存 | CPU）
        # 字体：连接状态用 BodyLabel（主信息），性能指标用 CaptionLabel
        # （Fluent 辅助信息字号更小、颜色更淡，避免与主界面文字同权）
        self._status_bar = QWidget(self)
        status_lay = QHBoxLayout(self._status_bar)
        status_lay.setContentsMargins(12, 4, 12, 4)
        status_lay.setSpacing(16)

        self._status_conn = BodyLabel("未连接")
        status_lay.addWidget(self._status_conn)
        status_lay.addStretch()

        self._status_speed = CaptionLabel("速度: --")
        status_lay.addWidget(self._status_speed)
        self._status_cache = CaptionLabel("缓存: --")
        status_lay.addWidget(self._status_cache)
        self._status_cpu = CaptionLabel("CPU: --")
        status_lay.addWidget(self._status_cpu)

        # 顶部分隔线（细边框代替 QStatusBar 的视觉边界）
        self._status_bar_top_line = QFrame(self._status_bar)

        # 重组右侧内容列：堆栈 + 进度条 + 状态条（垂直堆叠）
        self.widgetLayout.removeWidget(self.stackedWidget)
        right = QWidget(self)
        right_lay = QVBoxLayout(right)
        right_lay.setContentsMargins(0, 0, 0, 0)
        right_lay.setSpacing(0)
        right_lay.addWidget(self.stackedWidget, 1)
        right_lay.addWidget(self.transfer_progress_inline)
        right_lay.addWidget(self._status_bar)
        self.widgetLayout.addWidget(right, 1)

    def update_perf_stats(
        self,
        speed_mb: float,
        cache_bytes: int,
        cpu_pct: float,
    ) -> None:
        """更新状态条性能指标。"""
        self._status_speed.setText(format_speed(speed_mb))
        self._status_cache.setText(format_cache(cache_bytes))
        self._status_cpu.setText(format_cpu(cpu_pct))

    def set_connected(self, connected: bool) -> None:
        """更新连接状态显示（文本不变，仅配色区分状态）。"""
        self._status_conn.setText("已连接" if connected else "未连接")
        # 已连接用成功色 + 加粗突出，未连接用淡灰弱化；空串（深色主题）时不覆盖
        color = semantic_color("ok" if connected else "muted")
        if color:
            weight = "font-weight: 600;" if connected else ""
            self._status_conn.setStyleSheet(f"color: {color}; {weight}")
        else:
            self._status_conn.setStyleSheet("")

    # ==================================================================
    # 菜单（RoundMenu 承载）与快捷键
    # ==================================================================

    def _build_menu_actions(self) -> None:
        """构建动作与圆菜单；快捷键经窗口级 QShortcut 保证全局生效。

        FluentWindow 无菜单栏：动作收纳进导航栏底部的圆菜单，
        快捷键改为窗口级 QShortcut（F5 / Ctrl+L / Ctrl+U / Ctrl+D）。
        """
        # ---- Mi库菜单 ----
        self._vault_menu = QMenu("Mi库(&M)", self)
        init_action = self._vault_menu.addAction("初始化/连接(&I)...")
        init_action.triggered.connect(self.initRequested.emit)
        self._vault_menu.addSeparator()
        refresh_action = self._vault_menu.addAction("刷新(&R)")
        refresh_action.triggered.connect(self.refreshRequested.emit)
        lock_action = self._vault_menu.addAction("锁定密库(&L)")
        lock_action.triggered.connect(self.lockRequested.emit)

        # ---- 文件菜单 ----
        self._file_menu = QMenu("文件(&F)", self)
        upload_action = self._file_menu.addAction("上传(&U)...")
        upload_action.triggered.connect(
            lambda: self.uploadRequested.emit(self._selected_remote_dir())
        )
        download_action = self._file_menu.addAction("下载(&D)...")
        download_action.triggered.connect(
            lambda: self.downloadRequested.emit(self._selected_path())
        )

        # ---- 播放菜单 ----
        self._play_menu = QMenu("播放(&P)", self)
        play_action = self._play_menu.addAction("播放当前(&P)")
        play_action.triggered.connect(
            lambda: self.playRequested.emit(self._selected_path())
        )

        # 窗口级快捷键（QAction 挂在弹出菜单中不参与快捷键分发）
        QShortcut(QKeySequence("F5"), self).activated.connect(
            self.refreshRequested.emit
        )
        QShortcut(QKeySequence("Ctrl+L"), self).activated.connect(
            self.lockRequested.emit
        )
        QShortcut(QKeySequence("Ctrl+U"), self).activated.connect(
            lambda: self.uploadRequested.emit(self._selected_remote_dir())
        )
        QShortcut(QKeySequence("Ctrl+D"), self).activated.connect(
            lambda: self.downloadRequested.emit(self._selected_path())
        )

        # 导航栏底部「菜单」按钮 -> 圆菜单
        self._round_menu = RoundMenu("菜单", self)
        for menu in (self._vault_menu, self._file_menu, self._play_menu):
            self._round_menu.addActions(menu.actions())
            self._round_menu.addSeparator()
        self._menu_btn = NavigationPushButton(
            FluentIcon.MENU, "菜单", False, self
        )
        self.navigationInterface.addWidget(
            "menuButton",
            self._menu_btn,
            onClick=self._show_round_menu,
            position=NavigationItemPosition.BOTTOM,
            tooltip="菜单",
        )

    def _show_round_menu(self) -> None:
        """在菜单按钮上方弹出圆菜单。"""
        pos = self._menu_btn.mapToGlobal(
            self._menu_btn.rect().topLeft()
        )
        self._round_menu.exec(pos, ani=True)

    def menuBar(self) -> _MenuBarShim:  # noqa: N802 - 兼容旧接口命名
        """兼容旧接口：返回菜单结构查询对象（FluentWindow 无真实菜单栏）。"""
        return _MenuBarShim([self._vault_menu, self._file_menu, self._play_menu])

    # ==================================================================
    # 导航切换桥接
    # ==================================================================

    def _on_activity_page_changed(self, page_id: str) -> None:
        """兼容入口：旧式页面标识切换 -> 导航切换。"""
        mapping = {
            _ActivityBarAdapter.PAGE_FILES: self._files_container,
            _ActivityBarAdapter.PAGE_TRANSFERS: self.transfers_page,
            _ActivityBarAdapter.PAGE_VAULTS: self.vault_info_page,
            _ActivityBarAdapter.PAGE_SETTINGS: self.settings_page,
        }
        target = mapping.get(page_id)
        if target is not None and self.stackedWidget.currentWidget() is not target:
            self.switchTo(target)

    def _on_stacked_changed(self, index: int) -> None:
        """导航切换 -> 同步活动栏适配器的当前页标识。"""
        widget = self.stackedWidget.widget(index)
        mapping = {
            self._files_container: _ActivityBarAdapter.PAGE_FILES,
            self.transfers_page: _ActivityBarAdapter.PAGE_TRANSFERS,
            self.vault_info_page: _ActivityBarAdapter.PAGE_VAULTS,
            self.settings_page: _ActivityBarAdapter.PAGE_SETTINGS,
        }
        page_id = mapping.get(widget)
        if page_id is not None:
            self.activity_bar._set_current(page_id)

    # ==================================================================
    # 辅助
    # ==================================================================

    def _selected_path(self) -> str:
        """获取当前文件树选中项的远端路径（未选中返回空串）。"""
        indexes = self.file_tree.selectedIndexes()
        if not indexes:
            return ""
        node = indexes[0].internalPointer()
        if node is None:
            return ""
        # 沿父链拼接后端原始名（文件名加密时为密文名）
        parts: list[str] = []
        cur = node
        while cur is not None and cur.name:
            parts.append(cur.name)
            cur = cur.parent
        parts.reverse()
        return "/".join(parts)

    def _selected_remote_dir(self) -> str:
        """上传目标目录：选中目录本身，选中文件则为其所在目录。"""
        indexes = self.file_tree.selectedIndexes()
        if not indexes:
            return ""
        node = indexes[0].internalPointer()
        if node is None:
            return ""
        if not node.is_dir:
            node = node.parent
        if node is None:
            return ""
        parts: list[str] = []
        cur = node
        while cur is not None and cur.name:
            parts.append(cur.name)
            cur = cur.parent
        parts.reverse()
        return "/".join(parts)

    # ==================================================================
    # 便捷属性
    # ==================================================================

    @property
    def _content_widget(self) -> QWidget:
        """兼容旧接口：内容容器（现为导航堆栈容器）。"""
        return self.stackedWidget

    @property
    def file_tree(self):
        """文件树视图。"""
        return self._files_page.tree

    @property
    def transfer_progress(self):
        """内嵌传输进度条。"""
        return self.transfer_progress_inline
