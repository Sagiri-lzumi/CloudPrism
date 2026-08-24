"""活动栏：IDE 风格左侧图标导航栏。

窄垂直栏（约 56px 宽），包含四个互斥按钮切换侧面板内容：
  - 文件（文件夹图标）-> 显示文件树
  - 传输（上下箭头图标）-> 显示传输队列
  - 密库（保险箱图标）-> 显示密库信息
  - 设置（齿轮图标）-> 显示设置（推到底部）

图标优先使用 QStyle 标准图标，fallback 到 Unicode 符号。
"""

from __future__ import annotations

from PySide6.QtCore import Signal, Qt, QSize
from PySide6.QtGui import QIcon
from PySide6.QtWidgets import (
    QButtonGroup,
    QLabel,
    QToolButton,
    QVBoxLayout,
    QWidget,
)


class ActivityBar(QWidget):
    """活动栏：切换侧面板内容。"""

    # 当前选中项变更信号，参数为页面标识
    currentChanged = Signal(str)

    # 页面标识常量
    PAGE_FILES = "files"
    PAGE_TRANSFERS = "transfers"
    PAGE_VAULTS = "vaults"
    PAGE_SETTINGS = "settings"

    # 按钮固定宽度
    BAR_WIDTH = 56

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self.setFixedWidth(self.BAR_WIDTH)
        # 深色背景
        self.setStyleSheet(
            "ActivityBar { background-color: #252526; }"
        )

        lay = QVBoxLayout(self)
        lay.setContentsMargins(0, 8, 0, 8)
        lay.setSpacing(2)

        # 互斥按钮组
        self._group = QButtonGroup(self)
        self._group.setExclusive(True)
        self._group.buttonClicked.connect(self._on_clicked)

        # 顶部按钮：文件、传输、密库
        self._btn_files = self._create_button(
            "文件", "\U0001F4C1", self.PAGE_FILES, "文件浏览器"
        )
        lay.addWidget(self._btn_files)
        self._group.addButton(self._btn_files)

        self._btn_transfers = self._create_button(
            "传输", "\u21C5", self.PAGE_TRANSFERS, "传输队列"
        )
        lay.addWidget(self._btn_transfers)
        self._group.addButton(self._btn_transfers)

        self._btn_vaults = self._create_button(
            "密库", "\U0001F512", self.PAGE_VAULTS, "密库信息"
        )
        lay.addWidget(self._btn_vaults)
        self._group.addButton(self._btn_vaults)

        # 弹性空间
        lay.addStretch()

        # 底部按钮：设置
        self._btn_settings = self._create_button(
            "设置", "\u2699", self.PAGE_SETTINGS, "设置"
        )
        lay.addWidget(self._btn_settings)
        self._group.addButton(self._btn_settings)

        # 默认选中文件页
        self._btn_files.setChecked(True)

    def _create_button(
        self, text: str, fallback_char: str, page_id: str, tooltip: str
    ) -> QToolButton:
        """创建活动栏按钮（图标 + 文字竖排）。"""
        btn = QToolButton(self)
        btn.setCheckable(True)
        btn.setToolTip(tooltip)
        btn.setProperty("page_id", page_id)
        btn.setSizePolicy(btn.sizePolicy())

        # 尝试使用 QStyle 标准图标
        icon = self._get_standard_icon(page_id)
        if icon is not None and not icon.isNull():
            btn.setIcon(icon)
            btn.setIconSize(QSize(20, 20))
            btn.setToolButtonStyle(Qt.ToolButtonTextUnderIcon)
        else:
            # fallback: 使用 Unicode 符号作为文本
            btn.setText(f"{fallback_char}\n{text}")

        # 统一样式
        btn.setStyleSheet(
            "QToolButton {"
            "  color: #cccccc; background-color: transparent;"
            "  border: none; padding: 6px 4px;"
            "  font-size: 11px; min-width: 52px; min-height: 52px;"
            "  border-left: 3px solid transparent;"
            "}"
            "QToolButton:checked {"
            "  color: #ffffff; background-color: #37373d;"
            "  border-left: 3px solid #0078d4;"
            "}"
            "QToolButton:hover {"
            "  color: #ffffff; background-color: #2a2d2e;"
            "}"
        )

        # 如果用了标准图标，再叠加文字标签
        if icon is not None and not icon.isNull():
            btn.setText(text)

        return btn

    def _get_standard_icon(self, page_id: str) -> QIcon | None:
        """获取 QStyle 标准图标；不可用时返回 None。"""
        from PySide6.QtWidgets import QStyle

        style = self.style()
        if style is None:
            return None

        mapping = {
            self.PAGE_FILES: QStyle.SP_DirIcon,
            self.PAGE_TRANSFERS: QStyle.SP_ArrowUp,
            self.PAGE_VAULTS: QStyle.SP_DriveNetIcon,
            self.PAGE_SETTINGS: QStyle.SP_FileDialogDetailedView,
        }
        sp = mapping.get(page_id)
        if sp is not None:
            icon = style.standardIcon(sp)
            if not icon.isNull():
                return icon
        return None

    def _on_clicked(self, button: QToolButton) -> None:
        """按钮点击：发射 currentChanged 信号。"""
        page_id = button.property("page_id")
        if page_id:
            self.currentChanged.emit(page_id)

    @property
    def current_page(self) -> str:
        """当前选中的页面标识。"""
        btn = self._group.checkedButton()
        if btn:
            return btn.property("page_id") or self.PAGE_FILES
        return self.PAGE_FILES
