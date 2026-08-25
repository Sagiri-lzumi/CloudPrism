"""密库信息页：已连接时显示库信息，未连接时显示引导与最近密库记录。

已连接状态：
  - 库名称（vault_id 截短显示）
  - 后端类型 / 路径
  - 文件名加密状态
  - 云端占用大小
  - 本地缓存大小
  - 操作按钮：刷新 / 锁定

未连接状态：
  - 最近密库记录列表（双击/「连接所选」= 快速重连，可移除记录）
  - 「新建连接 / 初始化」按钮
"""

from __future__ import annotations

from datetime import datetime

from PySide6.QtCore import Signal, Qt
from PySide6.QtWidgets import (
    QFormLayout,
    QGroupBox,
    QHBoxLayout,
    QLabel,
    QListWidget,
    QListWidgetItem,
    QMenu,
    QPushButton,
    QVBoxLayout,
    QWidget,
)


class VaultInfoPage(QWidget):
    """密库信息页。"""

    # 请求连接/初始化信号（打开完整向导）
    connectRequested = Signal()
    # 请求快速连接某条最近密库记录（参数为记录 dict）
    quickConnectRequested = Signal(dict)
    # 请求移除某条最近密库记录
    removeVaultRequested = Signal(dict)
    # 请求刷新信号
    refreshRequested = Signal()
    # 请求锁定密库信号
    lockRequested = Signal()

    def __init__(self, parent=None) -> None:
        super().__init__(parent)

        # 主布局：居中卡片
        outer = QVBoxLayout(self)
        outer.setContentsMargins(24, 24, 24, 24)

        # ---- 未连接引导页（含最近密库记录） ----
        self._guide_widget = QWidget(self)
        guide_lay = QVBoxLayout(self._guide_widget)
        guide_lay.setContentsMargins(0, 0, 0, 0)
        guide_lay.setSpacing(12)

        self._guide_title = QLabel("尚未连接密库", self._guide_widget)
        self._guide_title.setStyleSheet(
            "font-size: 22px; font-weight: bold; color: #1a1a1a;"
        )
        self._guide_title.setAlignment(Qt.AlignCenter)
        guide_lay.addWidget(self._guide_title)

        self._guide_desc = QLabel(
            "密库是您的端到端加密存储空间。\n"
            "连接已有密库或创建新密库以开始使用。",
            self._guide_widget,
        )
        self._guide_desc.setStyleSheet("font-size: 14px; color: #5c5c5c;")
        self._guide_desc.setAlignment(Qt.AlignCenter)
        self._guide_desc.setWordWrap(True)
        guide_lay.addWidget(self._guide_desc)

        guide_lay.addSpacing(8)

        # 最近密库记录列表（双击行 = 快速连接）
        self._recent_list = QListWidget(self._guide_widget)
        self._recent_list.setAlternatingRowColors(True)
        self._recent_list.itemDoubleClicked.connect(self._on_item_double_clicked)
        self._recent_list.setContextMenuPolicy(Qt.CustomContextMenu)
        self._recent_list.customContextMenuRequested.connect(self._on_list_context_menu)
        guide_lay.addWidget(self._recent_list)

        # 操作按钮行：连接所选 / 移除记录 … 新建连接（主按钮）
        btn_row = QHBoxLayout()
        self._connect_sel_btn = QPushButton("连接所选", self._guide_widget)
        self._connect_sel_btn.clicked.connect(self._connect_selected)
        btn_row.addWidget(self._connect_sel_btn)

        self._remove_sel_btn = QPushButton("移除记录", self._guide_widget)
        self._remove_sel_btn.clicked.connect(self._remove_selected)
        btn_row.addWidget(self._remove_sel_btn)

        btn_row.addStretch()

        connect_btn = QPushButton("新建连接 / 初始化", self._guide_widget)
        connect_btn.setStyleSheet(
            "QPushButton {"
            "  background-color: #0067b8; color: white;"
            "  border: none; border-radius: 6px;"
            "  padding: 8px 20px; font-size: 14px; font-weight: 600;"
            "}"
            "QPushButton:hover { background-color: #106ebe; }"
            "QPushButton:pressed { background-color: #005a9e; }"
        )
        connect_btn.setCursor(Qt.PointingHandCursor)
        connect_btn.clicked.connect(self.connectRequested.emit)
        btn_row.addWidget(connect_btn)
        guide_lay.addLayout(btn_row)

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

        # 默认显示引导页（空记录态）
        self.set_recent_vaults([])
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
    # 最近密库记录
    # ------------------------------------------------------------------

    def set_recent_vaults(self, items: list[dict]) -> None:
        """填充最近密库记录（为空时回退纯引导文案）。"""
        self._recent_list.clear()
        for rec in items:
            name = rec.get("vault_name", "-")
            text = (
                f"{rec.get('label', rec.get('backend_type', '?'))} · "
                f"{rec.get('path', '-')} — 库 {name} · "
                f"上次 {rec.get('last_used', '-')}"
            )
            item = QListWidgetItem(text)
            item.setData(Qt.UserRole, rec)
            self._recent_list.addItem(item)

        has = bool(items)
        self._recent_list.setVisible(has)
        self._connect_sel_btn.setVisible(has)
        self._remove_sel_btn.setVisible(has)
        self._guide_title.setText("最近连接的密库" if has else "尚未连接密库")
        self._guide_desc.setText(
            "双击记录或点「连接所选」快速重连（仅需输入主密码）。"
            if has
            else "密库是您的端到端加密存储空间。\n"
            "连接已有密库或创建新密库以开始使用。"
        )
        if has:
            self._recent_list.setCurrentRow(0)

    def _selected_record(self) -> dict | None:
        """当前选中行对应的记录。"""
        item = self._recent_list.currentItem()
        return item.data(Qt.UserRole) if item else None

    def _connect_selected(self) -> None:
        """连接所选记录。"""
        rec = self._selected_record()
        if rec:
            self.quickConnectRequested.emit(rec)

    def _remove_selected(self) -> None:
        """移除所选记录。"""
        rec = self._selected_record()
        if rec:
            self.removeVaultRequested.emit(rec)

    def _on_item_double_clicked(self, item: QListWidgetItem) -> None:
        """双击行 = 快速连接。"""
        rec = item.data(Qt.UserRole)
        if rec:
            self.quickConnectRequested.emit(rec)

    def _on_list_context_menu(self, pos) -> None:
        """右键菜单：连接 / 移除记录。"""
        item = self._recent_list.itemAt(pos)
        if item is None:
            return
        self._recent_list.setCurrentItem(item)
        rec = item.data(Qt.UserRole)
        menu = QMenu(self)
        act_connect = menu.addAction("连接")
        act_connect.triggered.connect(
            lambda: self.quickConnectRequested.emit(rec)
        )
        act_remove = menu.addAction("移除记录")
        act_remove.triggered.connect(
            lambda: self.removeVaultRequested.emit(rec)
        )
        menu.exec(self._recent_list.mapToGlobal(pos))

    # ------------------------------------------------------------------
    # 内部方法
    # ------------------------------------------------------------------

    def _show_guide(self, show: bool) -> None:
        """切换引导页/信息页。"""
        self._guide_widget.setVisible(show)
        self._info_widget.setVisible(not show)
