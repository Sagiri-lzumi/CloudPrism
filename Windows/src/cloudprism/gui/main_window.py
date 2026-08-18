"""主窗口。

菜单 + 目录树视图 + 状态栏的骨架。上传/下载/播放/初始化向导等动作用
信号占位（后续步骤连接 TransferWorker / PlayerView / InitWizard）。
"""

from __future__ import annotations

from PySide6.QtCore import Signal
from PySide6.QtGui import QAction
from PySide6.QtWidgets import (
    QAbstractItemView,
    QLabel,
    QMainWindow,
    QMenu,
    QStatusBar,
    QTreeView,
)

from cloudprism.gui.dir_tree_model import DirTreeModel
from cloudprism.storage.backend import StorageBackend


class MainWindow(QMainWindow):
    """CloudPrism 主窗口。"""

    # ---- 占位信号：后续步骤连接 ----
    # 初始化/连接金库请求（InitWizard 接管）
    initRequested = Signal()
    # 上传请求（参数：选中的后端路径，None = 未选中）
    uploadRequested = Signal(str)
    # 下载请求（参数：选中的后端路径）
    downloadRequested = Signal(str)
    # 流式播放请求（参数：选中的后端路径）
    playRequested = Signal(str)

    def __init__(
        self,
        backend: StorageBackend | None = None,
        name_decryptor=None,
        parent=None,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle("CloudPrism - 端到端加密云盘")
        self.resize(900, 600)

        self._backend = backend
        self._model: DirTreeModel | None = None

        # ---- 中央目录树 ----
        self.tree = QTreeView(self)
        self.tree.setSelectionBehavior(QAbstractItemView.SelectRows)
        self.tree.setEditTriggers(QAbstractItemView.NoEditTriggers)
        # 懒加载由视图自动触发 canFetchMore/fetchMore
        self.tree.setUniformRowHeights(True)
        self.setCentralWidget(self.tree)

        # ---- 状态栏 ----
        self.status = QStatusBar(self)
        self.setStatusBar(self.status)
        self._status_label = QLabel("未连接", self)
        self.status.addWidget(self._status_label)

        # ---- 菜单 ----
        self._build_menus()

        # 有后端则立即装载数据
        if backend is not None:
            self.set_backend(backend, name_decryptor)

    # ------------------------------------------------------------------
    # 构建
    # ------------------------------------------------------------------

    def _build_menus(self) -> None:
        """构建菜单栏。"""
        # 金库菜单：初始化/连接、刷新
        vault_menu = QMenu("金库(&V)", self)
        init_action = QAction("初始化/连接(&I)...", self)
        init_action.triggered.connect(self.initRequested.emit)
        vault_menu.addAction(init_action)
        vault_menu.addSeparator()
        refresh_action = QAction("刷新(&R)", self)
        refresh_action.triggered.connect(self.refresh)
        vault_menu.addAction(refresh_action)
        self.menuBar().addMenu(vault_menu)

        # 文件菜单：上传、下载（占位）
        file_menu = QMenu("文件(&F)", self)
        upload_action = QAction("上传(&U)...", self)
        upload_action.triggered.connect(
            lambda: self.uploadRequested.emit(self._selected_remote_path() or "")
        )
        file_menu.addAction(upload_action)
        download_action = QAction("下载(&D)...", self)
        download_action.triggered.connect(
            lambda: self.downloadRequested.emit(self._selected_remote_path() or "")
        )
        file_menu.addAction(download_action)
        file_menu.addSeparator()
        quit_action = QAction("退出(&Q)", self)
        quit_action.triggered.connect(self.close)
        file_menu.addAction(quit_action)
        self.menuBar().addMenu(file_menu)

        # 播放菜单：流式播放（占位）
        play_menu = QMenu("播放(&P)", self)
        play_action = QAction("流式播放(&M)", self)
        play_action.triggered.connect(
            lambda: self.playRequested.emit(self._selected_remote_path() or "")
        )
        play_menu.addAction(play_action)
        self.menuBar().addMenu(play_menu)

    # ------------------------------------------------------------------
    # 后端与数据
    # ------------------------------------------------------------------

    def set_backend(
        self, backend: StorageBackend, name_decryptor=None
    ) -> None:
        """（重新）设置后端并装载目录树。"""
        self._backend = backend
        self._model = DirTreeModel(backend, name_decryptor=name_decryptor)
        self.tree.setModel(self._model)
        self._status_label.setText("已连接")

    def refresh(self) -> None:
        """刷新目录树。"""
        if self._model is not None:
            self._model.reload()
            # 重新装载触发根目录懒加载
            self.tree.expand(self.tree.model().index(0, 0))

    # ------------------------------------------------------------------
    # 选中与状态
    # ------------------------------------------------------------------

    def _selected_remote_path(self) -> str | None:
        """当前选中条目的后端相对路径（未选中返回 None）。"""
        if self._model is None:
            return None
        idx = self.tree.currentIndex()
        node = self._model.node_for_index(idx)
        if node is None:
            return None
        return self._model._remote_path(node)

    def set_status(self, text: str) -> None:
        """更新状态栏文本。"""
        self._status_label.setText(text)
