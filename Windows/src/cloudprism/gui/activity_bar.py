"""活动栏：IDE 风格左侧图标导航栏。

窄垂直栏（约 48px 宽），包含三个互斥按钮切换侧面板内容：
  - 文件（文件夹图标）-> 显示文件树
  - 传输（上下箭头图标）-> 显示传输队列
  - 设置（齿轮图标）-> 显示连接与缓存设置

图标使用 PySide6 内置的 QStyle.StandardPixmap，无需外部资源文件。
"""

from __future__ import annotations

from PySide6.QtCore import Signal, Qt
from PySide6.QtWidgets import QButtonGroup, QToolButton, QVBoxLayout, QWidget


class ActivityBar(QWidget):
    """活动栏：切换侧面板内容。"""

    # 当前选中项变更信号，参数为页面标识
    currentChanged = Signal(str)

    # 页面标识常量
    PAGE_FILES = "files"
    PAGE_TRANSFERS = "transfers"
    PAGE_SETTINGS = "settings"

    # 按钮固定宽度
    BAR_WIDTH = 48

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self.setFixedWidth(self.BAR_WIDTH)
        # 深色背景，与 IDE 活动栏风格一致
        self.setStyleSheet(
            "ActivityBar { background-color: #333; }"
            "QToolButton {"
            "  color: #aaa; border: none; padding: 8px;"
            "  font-size: 20px; min-width: 40px; min-height: 40px;"
            "}"
            "QToolButton:checked { color: #fff; background-color: #555; }"
            "QToolButton:hover { color: #fff; background-color: #444; }"
        )

        lay = QVBoxLayout(self)
        lay.setContentsMargins(2, 8, 2, 8)
        lay.setSpacing(4)

        # 互斥按钮组
        self._group = QButtonGroup(self)
        self._group.setExclusive(True)
        self._group.buttonClicked.connect(self._on_clicked)

        # 文件按钮
        self._btn_files = QToolButton(self)
        self._btn_files.setText("\U0001F4C1")  # 📁 文件夹 emoji
        self._btn_files.setCheckable(True)
        self._btn_files.setToolTip("文件浏览器")
        self._btn_files.setProperty("page_id", self.PAGE_FILES)
        lay.addWidget(self._btn_files)
        self._group.addButton(self._btn_files)

        # 传输按钮
        self._btn_transfers = QToolButton(self)
        self._btn_transfers.setText("\u2B06")  # ⬆ 上箭头
        self._btn_transfers.setCheckable(True)
        self._btn_transfers.setToolTip("传输队列")
        self._btn_transfers.setProperty("page_id", self.PAGE_TRANSFERS)
        lay.addWidget(self._btn_transfers)
        self._group.addButton(self._btn_transfers)

        # 设置按钮
        self._btn_settings = QToolButton(self)
        self._btn_settings.setText("\u2699")  # ⚙ 齿轮 emoji
        self._btn_settings.setCheckable(True)
        self._btn_settings.setToolTip("设置")
        self._btn_settings.setProperty("page_id", self.PAGE_SETTINGS)
        lay.addWidget(self._btn_settings)
        self._group.addButton(self._btn_settings)

        lay.addStretch()

        # 默认选中文件页
        self._btn_files.setChecked(True)

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
