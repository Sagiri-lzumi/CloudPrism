"""侧面板容器：文件树 / 传输队列 / 设置。

QStackedWidget 容纳三个子面板，由活动栏（ActivityBar）切换。
设置页包含连接信息与缓存管理（大小限制、路径配置、清除缓存）。
"""

from __future__ import annotations

import os
import shutil
import tempfile
from typing import Callable

from PySide6.QtCore import Signal, Qt
from PySide6.QtWidgets import (
    QAbstractItemView,
    QFormLayout,
    QGroupBox,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QListWidget,
    QListWidgetItem,
    QPushButton,
    QSpinBox,
    QStackedWidget,
    QTreeView,
    QVBoxLayout,
    QWidget,
)

from cloudprism.gui.dir_tree_model import DirTreeModel
from cloudprism.storage.backend import StorageBackend


# ---------------------------------------------------------------------------
# 侧面板容器
# ---------------------------------------------------------------------------


class SidePanel(QStackedWidget):
    """侧面板：三页切换（文件树 / 传输队列 / 设置）。"""

    # 页面索引
    PAGE_FILES = 0
    PAGE_TRANSFERS = 1
    PAGE_SETTINGS = 2

    def __init__(self, parent=None) -> None:
        super().__init__(parent)

        # 文件树页
        self.files_page = FilesPage()
        self.addWidget(self.files_page)

        # 传输队列页
        self.transfers_page = TransfersPage()
        self.addWidget(self.transfers_page)

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
            "settings": self.PAGE_SETTINGS,
        }
        self.setCurrentIndex(mapping.get(page_id, self.PAGE_FILES))


# ---------------------------------------------------------------------------
# 文件树页
# ---------------------------------------------------------------------------


class FilesPage(QWidget):
    """文件浏览器页：目录树视图。"""

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        lay = QVBoxLayout(self)
        lay.setContentsMargins(0, 0, 0, 0)

        self.tree = QTreeView(self)
        self.tree.setSelectionBehavior(QAbstractItemView.SelectRows)
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
        lay.setContentsMargins(4, 4, 4, 4)

        header = QLabel("传输队列", self)
        header.setStyleSheet("font-weight: bold; font-size: 14px;")
        lay.addWidget(header)

        self.task_list = QListWidget(self)
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
    """设置面板：连接信息 + 缓存管理。"""

    # 缓存设置变更信号
    cacheSettingsChanged = Signal(int, str)  # (cache_limit_mb, cache_path)
    # 清除缓存请求信号
    clearCacheRequested = Signal()

    # 默认缓存配置
    DEFAULT_CACHE_LIMIT_MB = 512
    DEFAULT_CACHE_SUBDIR = "cloudprism_cache"

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        lay = QVBoxLayout(self)
        lay.setContentsMargins(8, 8, 8, 8)
        lay.setSpacing(12)

        # ---- 连接信息区 ----
        conn_group = QGroupBox("连接信息", self)
        conn_form = QFormLayout(conn_group)

        self._backend_type_label = QLabel("未连接", self)
        conn_form.addRow("后端类型：", self._backend_type_label)

        self._backend_path_label = QLabel("-", self)
        conn_form.addRow("路径/URL：", self._backend_path_label)

        self._filename_enc_label = QLabel("-", self)
        conn_form.addRow("文件名加密：", self._filename_enc_label)

        # 断开/重连按钮
        btn_row = QHBoxLayout()
        self._reconnect_btn = QPushButton("重新连接", self)
        self._reconnect_btn.clicked.connect(lambda: self._reconnect_requested.emit())
        btn_row.addWidget(self._reconnect_btn)
        btn_row.addStretch()
        conn_form.addRow(btn_row)

        lay.addWidget(conn_group)

        # ---- 缓存设置区 ----
        cache_group = QGroupBox("缓存设置", self)
        cache_form = QFormLayout(cache_group)

        # 缓存大小限制
        self._cache_limit_spin = QSpinBox(self)
        self._cache_limit_spin.setRange(64, 4096)
        self._cache_limit_spin.setValue(self.DEFAULT_CACHE_LIMIT_MB)
        self._cache_limit_spin.setSuffix(" MB")
        self._cache_limit_spin.setToolTip("流式代理与传输管线的内存/磁盘缓冲上限")
        self._cache_limit_spin.valueChanged.connect(self._emit_cache_settings)
        cache_form.addRow("缓存大小限制：", self._cache_limit_spin)

        # 缓存位置
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

        # 当前缓存占用
        self._cache_usage_label = QLabel("计算中…", self)
        cache_form.addRow("当前缓存占用：", self._cache_usage_label)

        # 清除缓存按钮
        clear_btn = QPushButton("清除缓存", self)
        clear_btn.clicked.connect(self._on_clear_cache)
        cache_form.addRow(clear_btn)

        lay.addWidget(cache_group)
        lay.addStretch()

        # 刷新缓存占用显示
        self._refresh_cache_usage()

    # 内部信号（供 MainWindow 连接）
    _reconnect_requested = Signal()

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

    @property
    def cache_limit_mb(self) -> int:
        """当前缓存大小限制（MB）。"""
        return self._cache_limit_spin.value()

    @property
    def cache_path(self) -> str:
        """当前缓存路径。"""
        return self._cache_path_edit.text().strip()

    # ------------------------------------------------------------------
    # 内部方法
    # ------------------------------------------------------------------

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
