"""CloudPrism 主窗口（IDE 风格三栏布局）。

布局：
  +------+-----------------+------------------------------------------+
  |      |                 |                                          |
  | 活动 |   侧面板        |           预览区                          |
  | 栏   |                 |                                          |
  |      | [文件] [传输]    |  - 视频/音频: 内嵌播放器                  |
  | [文件]| 文件树          |  - 图片: 图片查看器                       |
  | [传输]| 上传/下载进度   |  - 文本: 文本查看器                       |
  | [设置]| 连接/缓存设置   |  - 其他: 文件信息                         |
  |      |                 |                                          |
  +------+-----------------+------------------------------------------+
  | 连接状态 | 传输速度 | 缓存占用 | CPU 占用                         |
  +-------------------------------------------------------------------+
"""

from __future__ import annotations

from PySide6.QtCore import Signal, Qt
from PySide6.QtWidgets import (
    QHBoxLayout,
    QLabel,
    QMainWindow,
    QMenu,
    QMenuBar,
    QMessageBox,
    QSplitter,
    QStatusBar,
    QWidget,
)

from cloudprism.gui.activity_bar import ActivityBar
from cloudprism.gui.perf_monitor import format_cache, format_cpu, format_speed
from cloudprism.gui.preview_panel import PreviewPanel
from cloudprism.gui.side_panel import SidePanel


class MainWindow(QMainWindow):
    """CloudPrism 主窗口。"""

    # ---- 占位信号：后续步骤连接 ----
    # 初始化/连接Mi库请求（InitWizard 接管）
    initRequested = Signal()
    # 上传请求（参数：选中的后端路径，None = 未选中）
    uploadRequested = Signal(str)
    # 下载请求（参数：选中的后端路径）
    downloadRequested = Signal(str)
    # 播放请求（参数：选中的后端路径）
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

        # Mi库菜单：初始化/连接、刷新
        vault_menu = QMenu("Mi库(&M)", self)
        init_action = vault_menu.addAction("初始化/连接(&I)...")
        init_action.triggered.connect(self.initRequested.emit)
        vault_menu.addSeparator()
        refresh_action = vault_menu.addAction("刷新(&R)")
        refresh_action.triggered.connect(self._refresh_tree)
        menu_bar.addMenu(vault_menu)

        # 文件菜单：上传、下载
        file_menu = QMenu("文件(&F)", self)
        upload_action = file_menu.addAction("上传(&U)...")
        upload_action.triggered.connect(lambda: self.uploadRequested.emit(self._selected_path()))
        download_action = file_menu.addAction("下载(&D)...")
        download_action.triggered.connect(lambda: self.downloadRequested.emit(self._selected_path()))
        menu_bar.addMenu(file_menu)

        # 播放菜单
        play_menu = QMenu("播放(&P)", self)
        play_action = play_menu.addAction("播放当前(&P)")
        play_action.triggered.connect(lambda: self.playRequested.emit(self._selected_path()))
        menu_bar.addMenu(play_menu)

    # ==================================================================
    # 中央区域：活动栏 + 侧面板 + 预览面板
    # ==================================================================

    def _build_central(self) -> None:
        """构建三栏 IDE 布局。"""
        central = QWidget(self)
        main_layout = QHBoxLayout(central)
        main_layout.setContentsMargins(0, 0, 0, 0)
        main_layout.setSpacing(0)

        # 活动栏（固定宽度）
        self.activity_bar = ActivityBar(central)
        main_layout.addWidget(self.activity_bar)

        # 分隔器：侧面板 | 预览面板
        self.splitter = QSplitter(Qt.Horizontal, central)
        self.side_panel = SidePanel(central)
        self.preview_panel = PreviewPanel(central)
        self.splitter.addWidget(self.side_panel)
        self.splitter.addWidget(self.preview_panel)
        # 初始比例：侧面板 1/3，预览 2/3
        self.splitter.setStretchFactor(0, 1)
        self.splitter.setStretchFactor(1, 2)
        main_layout.addWidget(self.splitter, stretch=1)

        self.setCentralWidget(central)

        # 活动栏切换 -> 侧面板页面切换
        self.activity_bar.currentChanged.connect(self.side_panel.show_page)

    # ==================================================================
    # 状态栏：连接状态 + 传输速度 + 缓存占用 + CPU 占用
    # ==================================================================

    def _build_status_bar(self) -> None:
        """构建多段状态栏。"""
        sb = QStatusBar(self)
        self.setStatusBar(sb)

        # 左侧：连接状态
        self._status_conn = QLabel("未连接")
        sb.addWidget(self._status_conn)

        # 右侧：性能指标
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
        """更新状态栏性能指标（由 PerfMonitor 信号触发）。"""
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
        """获取当前文件树选中路径（占位，后续步骤完善）。"""
        return ""

    def _refresh_tree(self) -> None:
        """刷新文件树（占位，后续步骤完善）。"""
        pass

    # ==================================================================
    # 便捷属性（供 AppController 访问）
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
    def settings_page(self):
        """设置页。"""
        return self.side_panel.settings_page
