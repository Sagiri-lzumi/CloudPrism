"""密库信息页：已连接时显示库信息，未连接时显示引导。

已连接状态：
  - 库名称（vault_id 截短显示）
  - 后端类型 / 路径
  - 文件名加密状态
  - 云端占用大小
  - 本地缓存大小
  - 操作按钮：刷新

未连接状态：
  - 引导文案 + 大按钮「初始化/连接密库」
"""

from __future__ import annotations

from datetime import datetime

from PySide6.QtCore import Signal, Qt
from PySide6.QtWidgets import (
    QFormLayout,
    QGroupBox,
    QHBoxLayout,
    QLabel,
    QPushButton,
    QVBoxLayout,
    QWidget,
)


class VaultInfoPage(QWidget):
    """密库信息页。"""

    # 请求连接/初始化信号
    connectRequested = Signal()
    # 请求刷新信号
    refreshRequested = Signal()
    # 请求锁定密库信号
    lockRequested = Signal()

    def __init__(self, parent=None) -> None:
        super().__init__(parent)

        # 主布局：居中卡片
        outer = QVBoxLayout(self)
        outer.setContentsMargins(24, 24, 24, 24)

        # ---- 未连接引导页 ----
        self._guide_widget = QWidget(self)
        guide_lay = QVBoxLayout(self._guide_widget)
        guide_lay.setAlignment(Qt.AlignCenter)

        guide_title = QLabel("尚未连接密库", self._guide_widget)
        guide_title.setStyleSheet(
            "font-size: 22px; font-weight: bold; color: #1a1a1a;"
        )
        guide_title.setAlignment(Qt.AlignCenter)
        guide_lay.addWidget(guide_title)

        guide_desc = QLabel(
            "密库是您的端到端加密存储空间。\n"
            "连接已有密库或创建新密库以开始使用。",
            self._guide_widget,
        )
        guide_desc.setStyleSheet("font-size: 14px; color: #5c5c5c;")
        guide_desc.setAlignment(Qt.AlignCenter)
        guide_desc.setWordWrap(True)
        guide_lay.addWidget(guide_desc)

        guide_lay.addSpacing(16)

        connect_btn = QPushButton("初始化 / 连接密库", self._guide_widget)
        connect_btn.setStyleSheet(
            "QPushButton {"
            "  background-color: #0067b8; color: white;"
            "  border: none; border-radius: 6px;"
            "  padding: 12px 32px; font-size: 16px; font-weight: 600;"
            "}"
            "QPushButton:hover { background-color: #106ebe; }"
            "QPushButton:pressed { background-color: #005a9e; }"
        )
        connect_btn.setCursor(Qt.PointingHandCursor)
        connect_btn.clicked.connect(self.connectRequested.emit)
        guide_lay.addWidget(connect_btn, alignment=Qt.AlignCenter)

        outer.addWidget(self._guide_widget)

        # ---- 已连接信息页 ----
        self._info_widget = QWidget(self)
        info_lay = QVBoxLayout(self._info_widget)
        info_lay.setContentsMargins(8, 8, 8, 8)
        info_lay.setSpacing(12)

        # 标题
        title = QLabel("密库信息", self._info_widget)
        title.setStyleSheet("font-size: 18px; font-weight: bold; color: #1a1a1a;")
        info_lay.addWidget(title)

        # 基本信息组
        basic_group = QGroupBox("基本信息", self._info_widget)
        basic_form = QFormLayout(basic_group)

        self._vault_name_label = QLabel("-", self._info_widget)
        basic_form.addRow("库名称：", self._vault_name_label)

        self._backend_type_label = QLabel("-", self._info_widget)
        basic_form.addRow("后端类型：", self._backend_type_label)

        self._backend_path_label = QLabel("-", self._info_widget)
        self._backend_path_label.setWordWrap(True)
        basic_form.addRow("后端路径：", self._backend_path_label)

        self._filename_enc_label = QLabel("-", self._info_widget)
        basic_form.addRow("文件名加密：", self._filename_enc_label)

        self._connect_time_label = QLabel("-", self._info_widget)
        basic_form.addRow("连接时间：", self._connect_time_label)

        info_lay.addWidget(basic_group)

        # 存储信息组
        storage_group = QGroupBox("存储信息", self._info_widget)
        storage_form = QFormLayout(storage_group)

        self._cloud_size_label = QLabel("-", self._info_widget)
        storage_form.addRow("云端占用：", self._cloud_size_label)

        self._cache_size_label = QLabel("-", self._info_widget)
        storage_form.addRow("本地缓存：", self._cache_size_label)

        self._file_count_label = QLabel("-", self._info_widget)
        storage_form.addRow("文件数量：", self._file_count_label)

        info_lay.addWidget(storage_group)

        # 操作按钮
        btn_row = QHBoxLayout()
        refresh_btn = QPushButton("刷新信息", self._info_widget)
        refresh_btn.clicked.connect(self.refreshRequested.emit)
        btn_row.addWidget(refresh_btn)

        lock_btn = QPushButton("锁定密库", self._info_widget)
        lock_btn.clicked.connect(self.lockRequested.emit)
        btn_row.addWidget(lock_btn)

        btn_row.addStretch()
        info_lay.addLayout(btn_row)

        info_lay.addStretch()
        outer.addWidget(self._info_widget)

        # 默认显示引导页
        self._show_guide(True)

    # ------------------------------------------------------------------
    # 公开方法
    # ------------------------------------------------------------------

    def show_connected(self) -> None:
        """切换到已连接信息页。"""
        self._show_guide(False)

    def show_disconnected(self) -> None:
        """切换到未连接引导页。"""
        self._show_guide(True)

    def update_info(
        self,
        vault_name: str = "-",
        backend_type: str = "-",
        backend_path: str = "-",
        filename_enc: bool = False,
        connect_time: str | None = None,
        cloud_size: str = "-",
        cache_size: str = "-",
        file_count: str = "-",
    ) -> None:
        """更新密库信息显示。"""
        self._vault_name_label.setText(vault_name)
        self._backend_type_label.setText(backend_type)
        self._backend_path_label.setText(backend_path)
        self._filename_enc_label.setText("开" if filename_enc else "关")
        self._connect_time_label.setText(
            connect_time or datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        )
        self._cloud_size_label.setText(cloud_size)
        self._cache_size_label.setText(cache_size)
        self._file_count_label.setText(file_count)

    # ------------------------------------------------------------------
    # 内部方法
    # ------------------------------------------------------------------

    def _show_guide(self, show: bool) -> None:
        """切换引导页/信息页。"""
        self._guide_widget.setVisible(show)
        self._info_widget.setVisible(not show)
