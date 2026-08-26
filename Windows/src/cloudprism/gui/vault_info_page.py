"""密库信息页：已连接时显示库信息，未连接时显示引导与最近密库记录。

已连接状态：
  - 库名称（vault_id 截短显示）
  - 后端类型 / 路径
  - 文件名加密状态
  - 云端占用大小
  - 本地缓存大小
  - 操作按钮：刷新 / 锁定

未连接状态（Fluent 引导页）：
  - 标题与说明文案（TitleLabel / BodyLabel）
  - 最近密库记录卡片列表：每张卡片自带动作（「连接」按钮、
    右上角移除图标按钮、双击快速连接），出现/移除均带淡入淡出动画
  - 「新建连接 / 初始化」主按钮居中
"""

from __future__ import annotations

from datetime import datetime

from PySide6.QtCore import (
    QAbstractAnimation,
    QEasingCurve,
    QPropertyAnimation,
    QSize,
    Qt,
    Signal,
)
from PySide6.QtWidgets import (
    QFormLayout,
    QGraphicsOpacityEffect,
    QHBoxLayout,
    QInputDialog,
    QLabel,
    QListWidgetItem,
    QVBoxLayout,
    QWidget,
)

# Fluent 组件（均继承自对应 Qt 原生控件，标准 API 全兼容）
from qfluentwidgets import (
    BodyLabel,
    CaptionLabel,
    CardWidget,
    FluentIcon,
    IconWidget,
    ListWidget,
    PrimaryPushButton,
    PushButton,
    SimpleCardWidget,
    StrongBodyLabel,
    SubtitleLabel,
    TitleLabel,
    ToolButton,
    TransparentToolButton,
)

from cloudprism.gui.theme import semantic_color

# 记录卡片固定高度（比旧版列表行略高，容纳两行信息）
CARD_HEIGHT = 84

# 其他密库卡片固定高度（单行信息，比记录卡片矮）
OTHER_CARD_HEIGHT = 56


class RecentVaultCard(SimpleCardWidget):
    """最近密库记录卡片：自带动作（连接 / 移除），双击快速连接。

    - 左侧图标 + 中间两行信息（库名称 / 后端·路径·上次使用时间）
    - 右侧「连接」按钮；右上角绝对定位移除图标按钮
    - 出现时淡入（play_in），移除时淡出（play_out）
    """

    # 请求连接本条记录（参数为记录 dict）
    connectRequested = Signal(dict)
    # 请求移除本条记录（参数为记录 dict）
    removeClicked = Signal(dict)

    def __init__(self, record: dict, parent=None) -> None:
        super().__init__(parent)
        self.record = record
        self.setFixedHeight(CARD_HEIGHT)

        # 淡入淡出动画载体（默认不透明，避免未调用 play_in 时不可见）
        self._opacity_fx = QGraphicsOpacityEffect(self)
        self.setGraphicsEffect(self._opacity_fx)

        lay = QHBoxLayout(self)
        # 右侧留出空间，避免与绝对定位的移除按钮重叠
        lay.setContentsMargins(16, 12, 56, 12)
        lay.setSpacing(12)

        # 左侧类型图标
        self._icon = IconWidget(self)
        self._icon.setIcon(FluentIcon.LIBRARY)
        self._icon.setFixedSize(30, 30)
        lay.addWidget(self._icon, 0, Qt.AlignmentFlag.AlignVCenter)

        # 中间两行信息：库名称（主）+ 后端·路径·上次使用（辅）
        mid = QVBoxLayout()
        mid.setSpacing(2)
        self.name_label = StrongBodyLabel(record.get("vault_name", "-"), self)
        detail = (
            f"{record.get('label', record.get('backend_type', '?'))} · "
            f"{record.get('path', '-')} · 上次 {record.get('last_used', '-')}"
        )
        self.detail_label = CaptionLabel(detail, self)
        self.detail_label.setStyleSheet(f"color: {semantic_color('muted')};")
        mid.addWidget(self.name_label)
        mid.addWidget(self.detail_label)
        lay.addLayout(mid, 1)

        # 右侧连接按钮
        self.connect_btn = PushButton("连接", self)
        self.connect_btn.setFixedWidth(76)
        self.connect_btn.setCursor(Qt.CursorShape.PointingHandCursor)
        self.connect_btn.clicked.connect(
            lambda: self.connectRequested.emit(self.record)
        )
        lay.addWidget(self.connect_btn, 0, Qt.AlignmentFlag.AlignVCenter)

        # 移除按钮：绝对定位右上角（精简、不占布局宽度）
        self.remove_btn = TransparentToolButton(self)
        self.remove_btn.setIcon(FluentIcon.DELETE)
        self.remove_btn.setToolTip("移除记录")
        self.remove_btn.setFixedSize(28, 28)
        self.remove_btn.setIconSize(QSize(14, 14))
        self.remove_btn.setCursor(Qt.CursorShape.PointingHandCursor)
        self.remove_btn.clicked.connect(
            lambda: self.removeClicked.emit(self.record)
        )
        self._move_remove_btn()

    # ------------------------------------------------------------------
    # 布局与事件
    # ------------------------------------------------------------------

    def _move_remove_btn(self) -> None:
        """把移除按钮钉在卡片右上角。"""
        self.remove_btn.move(self.width() - self.remove_btn.width() - 8, 6)

    def resizeEvent(self, e) -> None:  # noqa: N802 - Qt 事件命名
        super().resizeEvent(e)
        self._move_remove_btn()

    def mouseDoubleClickEvent(self, e) -> None:  # noqa: N802
        """双击卡片 = 快速连接。"""
        self.connectRequested.emit(self.record)

    # ------------------------------------------------------------------
    # 动画
    # ------------------------------------------------------------------

    def play_in(self) -> QPropertyAnimation:
        """出现动画：淡入（0 → 1，200ms）。"""
        self._opacity_fx.setOpacity(0.0)
        anim = QPropertyAnimation(self._opacity_fx, b"opacity", self)
        anim.setDuration(200)
        anim.setStartValue(0.0)
        anim.setEndValue(1.0)
        anim.setEasingCurve(QEasingCurve.Type.OutCubic)
        anim.start(QAbstractAnimation.DeletionPolicy.DeleteWhenStopped)
        return anim

    def play_out(self) -> QPropertyAnimation:
        """移除动画：淡出（当前透明度 → 0，150ms）。"""
        anim = QPropertyAnimation(self._opacity_fx, b"opacity", self)
        anim.setDuration(150)
        anim.setStartValue(self._opacity_fx.opacity())
        anim.setEndValue(0.0)
        anim.setEasingCurve(QEasingCurve.Type.InCubic)
        anim.start(QAbstractAnimation.DeletionPolicy.DeleteWhenStopped)
        return anim


class OtherVaultCard(CardWidget):
    """同后端其他密库卡片：显示密库位置（路径），提供连接入口。

    连接时复用当前后端，仅需目标密库的主密码（控制器侧弹输入框）。
    """

    # 请求连接该密库（参数为密库位置路径，根密库为 ""）
    connectClicked = Signal(str)

    def __init__(self, vault_path: str, parent=None) -> None:
        super().__init__(parent)
        self.vault_path = vault_path
        self.setFixedHeight(OTHER_CARD_HEIGHT)

        lay = QHBoxLayout(self)
        lay.setContentsMargins(16, 10, 16, 10)
        lay.setSpacing(12)

        icon = IconWidget(self)
        icon.setIcon(FluentIcon.LIBRARY)
        icon.setFixedSize(24, 24)
        lay.addWidget(icon, 0, Qt.AlignmentFlag.AlignVCenter)

        label = BodyLabel(vault_path or "根目录", self)
        lay.addWidget(label, 1)

        btn = PushButton("连接", self)
        btn.setFixedWidth(76)
        btn.setCursor(Qt.CursorShape.PointingHandCursor)
        btn.clicked.connect(lambda: self.connectClicked.emit(self.vault_path))
        lay.addWidget(btn, 0, Qt.AlignmentFlag.AlignVCenter)
        self.connect_btn = btn  # 测试辅助入口


class VaultInfoPage(QWidget):
    """密库信息页。"""

    # 请求连接/初始化信号（打开完整向导）
    connectRequested = Signal()
    # 请求快速连接某条最近密库记录（参数为记录 dict）
    quickConnectRequested = Signal(dict)
    # 请求移除某条最近密库记录
    removeVaultRequested = Signal(dict)
    # 请求修改当前密库名称（参数为新名称）
    renameRequested = Signal(str)
    # 请求连接本后端的其他密库（参数为密库位置路径）
    connectOtherVaultRequested = Signal(str)
    # 请求刷新信号
    refreshRequested = Signal()
    # 请求锁定密库信号
    lockRequested = Signal()

    def __init__(self, parent=None) -> None:
        super().__init__(parent)

        # 记录卡片 -> 列表项映射（移除定位用）
        self._cards: dict = {}

        # 主布局
        outer = QVBoxLayout(self)
        outer.setContentsMargins(24, 24, 24, 24)

        # ---- 未连接引导页（含最近密库记录卡片） ----
        self._guide_widget = QWidget(self)
        guide_lay = QVBoxLayout(self._guide_widget)
        guide_lay.setContentsMargins(0, 0, 0, 0)
        guide_lay.setSpacing(10)

        self._guide_title = TitleLabel("尚未连接密库", self._guide_widget)
        self._guide_title.setAlignment(Qt.AlignmentFlag.AlignCenter)
        guide_lay.addWidget(self._guide_title)

        self._guide_desc = BodyLabel(
            "密库是您的端到端加密存储空间。\n"
            "连接已有密库或创建新密库以开始使用。",
            self._guide_widget,
        )
        self._guide_desc.setStyleSheet(f"color: {semantic_color('muted')};")
        self._guide_desc.setAlignment(Qt.AlignmentFlag.AlignCenter)
        self._guide_desc.setWordWrap(True)
        guide_lay.addWidget(self._guide_desc)

        guide_lay.addSpacing(6)

        # 主按钮居中（新建连接 / 初始化）
        connect_wrap = QHBoxLayout()
        connect_wrap.addStretch(1)
        self._connect_btn = PrimaryPushButton(
            "新建连接 / 初始化", self._guide_widget
        )
        self._connect_btn.setCursor(Qt.CursorShape.PointingHandCursor)
        self._connect_btn.clicked.connect(self.connectRequested.emit)
        connect_wrap.addWidget(self._connect_btn)
        connect_wrap.addStretch(1)
        guide_lay.addLayout(connect_wrap)

        guide_lay.addSpacing(10)

        # 最近密库记录卡片列表（卡片自带动作；双击卡片 = 快速连接）
        self._recent_list = ListWidget(self._guide_widget)
        self._recent_list.setStyleSheet("background: transparent; border: none;")
        guide_lay.addWidget(self._recent_list)

        outer.addWidget(self._guide_widget)

        # ---- 已连接信息页 ----
        self._info_widget = QWidget(self)
        info_lay = QVBoxLayout(self._info_widget)
        info_lay.setContentsMargins(8, 8, 8, 8)
        info_lay.setSpacing(10)

        # 标题
        title = TitleLabel("密库信息", self._info_widget)
        info_lay.addWidget(title)

        # 基本信息卡片（圆角卡片容器代替 QGroupBox）
        info_lay.addWidget(SubtitleLabel("基本信息", self._info_widget))
        basic_card = CardWidget(self._info_widget)
        basic_form = QFormLayout(basic_card)
        basic_form.setContentsMargins(20, 16, 20, 16)
        basic_form.setHorizontalSpacing(16)
        basic_form.setVerticalSpacing(8)

        self._vault_name_label = QLabel("-", basic_card)
        # 名称行：显示标签 + 编辑小按钮（修改后随 Vault Marker 加密保存）
        name_row = QHBoxLayout()
        name_row.setSpacing(6)
        name_row.addWidget(self._vault_name_label)
        self._rename_btn = ToolButton(FluentIcon.EDIT, basic_card)
        self._rename_btn.setToolTip("修改密库名称")
        self._rename_btn.setFixedSize(26, 26)
        self._rename_btn.setIconSize(QSize(14, 14))
        self._rename_btn.setCursor(Qt.CursorShape.PointingHandCursor)
        self._rename_btn.clicked.connect(self._on_rename_clicked)
        name_row.addWidget(self._rename_btn)
        name_row.addStretch()
        basic_form.addRow("库名称：", name_row)

        self._backend_type_label = QLabel("-", basic_card)
        basic_form.addRow("后端类型：", self._backend_type_label)

        self._backend_path_label = QLabel("-", basic_card)
        self._backend_path_label.setWordWrap(True)
        basic_form.addRow("后端路径：", self._backend_path_label)

        self._filename_enc_label = QLabel("-", basic_card)
        basic_form.addRow("文件名加密：", self._filename_enc_label)

        self._connect_time_label = QLabel("-", basic_card)
        basic_form.addRow("连接时间：", self._connect_time_label)

        info_lay.addWidget(basic_card)

        # 存储信息卡片
        info_lay.addWidget(SubtitleLabel("存储信息", self._info_widget))
        storage_card = CardWidget(self._info_widget)
        storage_form = QFormLayout(storage_card)
        storage_form.setContentsMargins(20, 16, 20, 16)
        storage_form.setHorizontalSpacing(16)
        storage_form.setVerticalSpacing(8)

        self._cloud_size_label = QLabel("-", storage_card)
        storage_form.addRow("云端占用：", self._cloud_size_label)

        self._cache_size_label = QLabel("-", storage_card)
        storage_form.addRow("本地缓存：", self._cache_size_label)

        self._file_count_label = QLabel("-", storage_card)
        storage_form.addRow("文件数量：", self._file_count_label)

        info_lay.addWidget(storage_card)

        # 本后端的其他密库（子目录 Marker 扫描；无其他密库时整块隐藏）
        self._other_title = SubtitleLabel("本后端的其他密库", self._info_widget)
        self._other_container = QWidget(self._info_widget)
        self._other_lay = QVBoxLayout(self._other_container)
        self._other_lay.setContentsMargins(0, 0, 0, 0)
        self._other_lay.setSpacing(8)
        info_lay.addWidget(self._other_title)
        info_lay.addWidget(self._other_container)
        self._other_title.setVisible(False)
        self._other_container.setVisible(False)

        # 操作按钮
        btn_row = QHBoxLayout()
        refresh_btn = PushButton("刷新信息", self._info_widget)
        refresh_btn.clicked.connect(self.refreshRequested.emit)
        btn_row.addWidget(refresh_btn)

        lock_btn = PushButton("锁定密库", self._info_widget)
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

    def set_other_vaults(self, vaults: list[dict]) -> None:
        """填充本后端的其他密库列表（调用方应已排除当前连接的密库）。

        每项为 {path, vault_id}；名称需打开后才能解密，卡片先显示路径。
        """
        while self._other_lay.count():
            item = self._other_lay.takeAt(0)
            w = item.widget()
            if w is not None:
                w.deleteLater()
        for v in vaults:
            card = OtherVaultCard(v.get("path", ""), self._other_container)
            card.connectClicked.connect(self.connectOtherVaultRequested.emit)
            self._other_lay.addWidget(card)
        has = bool(vaults)
        self._other_title.setVisible(has)
        self._other_container.setVisible(has)

    def other_vault_cards(self) -> list[OtherVaultCard]:
        """当前展示的其他密库卡片列表（测试辅助）。"""
        cards: list[OtherVaultCard] = []
        for i in range(self._other_lay.count()):
            w = self._other_lay.itemAt(i).widget()
            if isinstance(w, OtherVaultCard):
                cards.append(w)
        return cards

    def _on_rename_clicked(self) -> None:
        """库名称编辑按钮：弹输入框收集新名称，非空且变化时发射信号。"""
        current = self._vault_name_label.text()
        # 去掉回退名的省略号作为输入初值，避免用户沿用
        new_name, ok = QInputDialog.getText(
            self,
            "修改密库名称",
            "新名称（随密库文件保存，最多 32 字符）：",
            text=current.rstrip(".") if current.endswith("...") else current,
        )
        new_name = (new_name or "").strip()
        if ok and new_name and new_name != current:
            self.renameRequested.emit(new_name[:32])

    # ------------------------------------------------------------------
    # 最近密库记录卡片
    # ------------------------------------------------------------------

    def set_recent_vaults(self, items: list[dict]) -> None:
        """填充最近密库记录卡片（为空时回退纯引导文案）。

        重建前先 ``clear()`` 销毁旧卡片；每张卡片自带动作，
        信号经实例闭包绑定记录，无选中行依赖。
        """
        self._recent_list.clear()
        self._cards.clear()
        for rec in items:
            item = QListWidgetItem()
            item.setSizeHint(QSize(0, CARD_HEIGHT + 8))
            card = RecentVaultCard(rec)
            card.connectRequested.connect(self.quickConnectRequested.emit)
            card.removeClicked.connect(self._on_card_remove_clicked)
            # 记录卡片与 item 的对应关系，供移除时定位（信号只携带记录）
            self._cards[card] = item
            self._recent_list.addItem(item)
            self._recent_list.setItemWidget(item, card)
            card.play_in()

        has = bool(items)
        self._recent_list.setVisible(has)
        self._guide_title.setText("最近连接的密库" if has else "尚未连接密库")
        self._guide_desc.setText(
            "点击卡片上的「连接」或双击卡片快速重连（仅需输入主密码）。"
            if has
            else "密库是您的端到端加密存储空间。\n"
            "连接已有密库或创建新密库以开始使用。"
        )

    def card_at(self, row: int) -> RecentVaultCard | None:
        """取指定行的记录卡片（测试辅助）。"""
        item = self._recent_list.item(row)
        if item is None:
            return None
        widget = self._recent_list.itemWidget(item)
        return widget if isinstance(widget, RecentVaultCard) else None

    def _on_card_remove_clicked(self, rec: dict) -> None:
        """卡片移除按钮：立即发射移除信号，同时播淡出动画。

        信号先行保证删除确认流程不被动画阻塞；若用户取消确认，
        控制器重载记录会重建卡片，视觉状态自然恢复。
        """
        card = next((c for c in self._cards if c.record is rec), None)
        if card is not None:
            card.setEnabled(False)  # 防止动画期间重复触发
            card.play_out()
        self.removeVaultRequested.emit(rec)

    # ------------------------------------------------------------------
    # 内部方法
    # ------------------------------------------------------------------

    def _show_guide(self, show: bool) -> None:
        """切换引导页/信息页。"""
        self._guide_widget.setVisible(show)
        self._info_widget.setVisible(not show)
