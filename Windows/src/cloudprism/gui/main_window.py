"""CloudPrism 主窗口（IDE 风格动态布局）。

文件页：活动栏 + 侧面板（文件树）+ 预览面板
其他页：活动栏 + 全宽内容（传输/密库/设置）

  [文件页]
  +------+-----------------+------------------------------------------+
  |      |                 |                                          |
  | 活动 |   文件树        |           预览区                          |
  | 栏   |                 |                                          |
  +------+-----------------+------------------------------------------+

  [传输/密库/设置页]
  +------+-----------------------------------------------------------+
  |      |                                                           |
  | 活动 |   全宽内容区                                               |
  | 栏   |                                                           |
  +------+-----------------------------------------------------------+

  底部状态栏：连接状态 | 传输速度 | 缓存占用 | CPU
"""

from __future__ import annotations

from PySide6.QtCore import Signal, Qt
from PySide6.QtWidgets import (
    QHBoxLayout,
    QLabel,
    QMainWindow,
    QMenu,
    QMenuBar,
    QSplitter,
    QStackedWidget,
    QStatusBar,
    QVBoxLayout,
    QWidget,
)

from cloudprism.gui.activity_bar import ActivityBar
from cloudprism.gui.perf_monitor import format_cache, format_cpu, format_speed
from cloudprism.gui.preview_panel import PreviewPanel
from cloudprism.gui.side_panel import SidePanel


class MainWindow(QMainWindow):
    """CloudPrism 主窗口。"""

    # ---- 信号 ----
    initRequested = Signal()
    uploadRequested = Signal(str)
    downloadRequested = Signal(str)
    playRequested = Signal(str)

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self.setWindowTitle("CloudPrism")
        self.resize(1200, 700)

        self._build_menus()
        self._build_central()
        self._build_status_bar()

    # ==================================================================
    # 菜单
    # ==================================================================

    def _build_menus(self) -> None:
        """构建菜单栏。"""
        menu_bar: QMenuBar = self.menuBar()

        # Mi库菜单
        vault_menu = QMenu("Mi库(&M)", self)
        init_action = vault_menu.addAction("初始化/连接(&I)...")
        init_action.triggered.connect(self.initRequested.emit)
        vault_menu.addSeparator()
        refresh_action = vault_menu.addAction("刷新(&R)")
        refresh_action.triggered.connect(self._refresh_tree)
        menu_bar.addMenu(vault_menu)

        # 文件菜单
        file_menu = QMenu("文件(&F)", self)
        upload_action = file_menu.addAction("上传(&U)...")
        upload_action.triggered.connect(
            lambda: self.uploadRequested.emit(self._selected_path())
        )
        download_action = file_menu.addAction("下载(&D)...")
        download_action.triggered.connect(
            lambda: self.downloadRequested.emit(self._selected_path())
        )
        menu_bar.addMenu(file_menu)

        # 播放菜单
        play_menu = QMenu("播放(&P)", self)
        play_action = play_menu.addAction("播放当前(&P)")
        play_action.triggered.connect(
            lambda: self.playRequested.emit(self._selected_path())
        )
        menu_bar.addMenu(play_menu)

    # ==================================================================
    # 中央区域：动态布局
    # ==================================================================

    def _build_central(self) -> None:
        """构建动态 IDE 布局。

        文件页 -> 显示 side_panel（文件树）+ 预览面板
        其他页 -> 显示 side_panel 全宽（传输/密库/设置，无预览）
        """
        central = QWidget(self)
        main_layout = QHBoxLayout(central)
        main_layout.setContentsMargins(0, 0, 0, 0)
        main_layout.setSpacing(0)

        # 活动栏（固定宽度）
        self.activity_bar = ActivityBar(central)
        main_layout.addWidget(self.activity_bar)

        # 侧面板（始终存在，包含所有四个页面）
        self.side_panel = SidePanel(central)

        # 预览面板
        self.preview_panel = PreviewPanel(central)

        # 内容容器：动态切换
        # - 文件模式：side_panel(文件页) + preview_panel
        # - 全宽模式：side_panel(其他页) 占满
        self._content_widget = QWidget(central)
        self._content_layout = QHBoxLayout(self._content_widget)
        self._content_layout.setContentsMargins(0, 0, 0, 0)
        self._content_layout.setSpacing(0)
        self._content_layout.addWidget(self.side_panel, stretch=1)

        main_layout.addWidget(self._content_widget, stretch=1)
        self.setCentralWidget(central)

        # 活动栏切换 -> 动态布局
        self.activity_bar.currentChanged.connect(self._on_page_changed)
        # 默认文件模式
        self._show_file_mode()

    def _on_page_changed(self, page_id: str) -> None:
        """活动栏切换：动态调整布局。"""
        if page_id == ActivityBar.PAGE_FILES:
            self._show_file_mode()
        else:
            self._show_fullwidth_mode(page_id)

    def _show_file_mode(self) -> None:
        """文件模式：侧面板（文件树）+ 预览面板。"""
        # 切换到文件页
        self.side_panel.setCurrentIndex(self.side_panel.PAGE_FILES)
        # 添加预览面板（如果尚未添加）
        if self.preview_panel.parent() != self._content_widget:
            self._content_layout.addWidget(self.preview_panel, stretch=2)
        self.preview_panel.show()
        self.side_panel.setMaximumWidth(400)

    def _show_fullwidth_mode(self, page_id: str) -> None:
        """全宽模式：侧面板占满，无预览面板。"""
        # 移除预览面板
        self.preview_panel.hide()
        # 切换到对应页面
        self.side_panel.show_page(page_id)
        # 取消宽度限制
        self.side_panel.setMaximumWidth(16777215)

    # ==================================================================
    # 状态栏
    # ==================================================================

    def _build_status_bar(self) -> None:
        """构建多段状态栏。"""
        sb = QStatusBar(self)
        self.setStatusBar(sb)

        self._status_conn = QLabel("未连接")
        sb.addWidget(self._status_conn)

        self._status_speed = QLabel("速度: --")
        sb.addPermanentWidget(self._status_speed)

        self._status_cache = QLabel("缓存: --")
        sb.addPermanentWidget(self._status_cache)

        self._status_cpu = QLabel("CPU: --")
        sb.addPermanentWidget(self._status_cpu)

    def update_perf_stats(
        self,
        speed_mb: float,
        cache_bytes: int,
        cpu_pct: float,
    ) -> None:
        """更新状态栏性能指标。"""
        self._status_speed.setText(format_speed(speed_mb))
        self._status_cache.setText(format_cache(cache_bytes))
        self._status_cpu.setText(format_cpu(cpu_pct))

    def set_connected(self, connected: bool) -> None:
        """更新连接状态显示。"""
        self._status_conn.setText("已连接" if connected else "未连接")

    # ==================================================================
    # 辅助
    # ==================================================================

    def _selected_path(self) -> str:
        """获取当前文件树选中路径。"""
        return ""

    def _refresh_tree(self) -> None:
        """刷新文件树。"""
        pass

    # ==================================================================
    # 便捷属性
    # ==================================================================

    @property
    def file_tree(self):
        """文件树视图。"""
        return self.side_panel.files_page.tree

    @property
    def transfers_page(self):
        """传输队列页。"""
        return self.side_panel.transfers_page

    @property
    def vault_info_page(self):
        """密库信息页。"""
        return self.side_panel.vault_info_page

    @property
    def settings_page(self):
        """设置页。"""
        return self.side_panel.settings_page
