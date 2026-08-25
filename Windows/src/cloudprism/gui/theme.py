"""Windows 11 原生风格全局主题。

提供 apply_windows11_style() 函数，为 QApplication 设置统一 QSS 样式表，
模拟 Windows 11 系统控件外观：浅色背景、Segoe UI 字体、圆角按钮、
蓝色焦点边框等。
"""

from __future__ import annotations

from PySide6.QtWidgets import QApplication


# Windows 11 风格全局样式表
WINDOWS11_QSS = """
/* ---- 全局 ---- */
QWidget {
    font-family: "Segoe UI", "Microsoft YaHei UI", sans-serif;
    font-size: 10pt;
    color: #1a1a1a;
}

QMainWindow {
    background-color: #f3f3f3;
}

/* ---- 按钮 ---- */
QPushButton {
    background-color: #ffffff;
    border: 1px solid #d1d1d1;
    border-radius: 4px;
    padding: 5px 16px;
    min-height: 24px;
    color: #1a1a1a;
}
QPushButton:hover {
    background-color: #f0f0f0;
    border-color: #c8c8c8;
}
QPushButton:pressed {
    background-color: #ececec;
}
QPushButton:disabled {
    color: #a0a0a0;
    background-color: #f8f8f8;
    border-color: #e0e0e0;
}
QPushButton:default {
    border: 1px solid #0078d4;
}

/* ---- 输入框 ---- */
QLineEdit {
    background-color: #ffffff;
    border: 1px solid #d1d1d1;
    border-radius: 4px;
    padding: 4px 8px;
    min-height: 24px;
    selection-background-color: #0078d4;
    selection-color: white;
}
QLineEdit:focus {
    border: 1px solid #0078d4;
    border-bottom: 2px solid #0078d4;
}
QLineEdit:disabled {
    background-color: #f8f8f8;
    color: #a0a0a0;
}

/* ---- 下拉框 ---- */
QComboBox {
    background-color: #ffffff;
    border: 1px solid #d1d1d1;
    border-radius: 4px;
    padding: 4px 8px;
    min-height: 24px;
}
QComboBox:hover {
    border-color: #0078d4;
}
QComboBox::drop-down {
    border: none;
    width: 24px;
}
QComboBox::down-arrow {
    image: none;
    border-left: 4px solid transparent;
    border-right: 4px solid transparent;
    border-top: 5px solid #606060;
    margin-right: 6px;
}
QComboBox QAbstractItemView {
    background-color: #ffffff;
    border: 1px solid #d1d1d1;
    selection-background-color: #e5f1fb;
    selection-color: #1a1a1a;
    outline: none;
}

/* ---- 数字输入 ---- */
QSpinBox {
    background-color: #ffffff;
    border: 1px solid #d1d1d1;
    border-radius: 4px;
    padding: 4px 8px;
    min-height: 24px;
}
QSpinBox:focus {
    border: 1px solid #0078d4;
}

/* ---- 分组框 ---- */
QGroupBox {
    border: none;
    margin-top: 12px;
    padding-top: 16px;
    font-weight: bold;
    font-size: 10pt;
}
QGroupBox::title {
    subcontrol-origin: margin;
    subcontrol-position: top left;
    padding: 0 4px;
    color: #1a1a1a;
}

/* ---- 树视图 ---- */
QTreeView {
    background-color: #ffffff;
    border: 1px solid #e0e0e0;
    border-radius: 4px;
    alternate-background-color: #f9f9f9;
    outline: none;
    selection-background-color: #e5f1fb;
    selection-color: #1a1a1a;
}
QTreeView::item {
    padding: 3px 4px;
    border: none;
}
QTreeView::item:hover {
    background-color: #f0f0f0;
}
QTreeView::item:selected {
    background-color: #e5f1fb;
    color: #1a1a1a;
}
QTreeView::branch {
    background-color: transparent;
}

/* ---- 列表视图 ---- */
QListView {
    background-color: #ffffff;
    border: 1px solid #e0e0e0;
    border-radius: 4px;
    alternate-background-color: #f9f9f9;
    outline: none;
    selection-background-color: #e5f1fb;
    selection-color: #1a1a1a;
}
QListWidget {
    background-color: #ffffff;
    border: 1px solid #e0e0e0;
    border-radius: 4px;
    alternate-background-color: #f9f9f9;
    outline: none;
}

/* ---- 标签 ---- */
QLabel {
    background-color: transparent;
    border: none;
    padding: 0px;
}

/* ---- 滚动条 ---- */
QScrollBar:vertical {
    background-color: #f3f3f3;
    width: 10px;
    border: none;
}
QScrollBar::handle:vertical {
    background-color: #c0c0c0;
    border-radius: 5px;
    min-height: 20px;
    margin: 2px;
}
QScrollBar::handle:vertical:hover {
    background-color: #a0a0a0;
}
QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical {
    height: 0px;
}
QScrollBar:horizontal {
    background-color: #f3f3f3;
    height: 10px;
    border: none;
}
QScrollBar::handle:horizontal {
    background-color: #c0c0c0;
    border-radius: 5px;
    min-width: 20px;
    margin: 2px;
}
QScrollBar::handle:horizontal:hover {
    background-color: #a0a0a0;
}
QScrollBar::add-line:horizontal, QScrollBar::sub-line:horizontal {
    width: 0px;
}

/* ---- 选项卡 ---- */
QTabWidget::pane {
    border: 1px solid #e0e0e0;
    border-radius: 4px;
    background-color: #ffffff;
}
QTabBar::tab {
    background-color: #f0f0f0;
    border: 1px solid #d1d1d1;
    border-bottom: none;
    padding: 6px 16px;
    margin-right: 2px;
    border-top-left-radius: 4px;
    border-top-right-radius: 4px;
}
QTabBar::tab:selected {
    background-color: #ffffff;
    border-bottom: 2px solid #0078d4;
}
QTabBar::tab:hover:!selected {
    background-color: #e8e8e8;
}

/* ---- 进度条 ---- */
QProgressBar {
    background-color: #e0e0e0;
    border: none;
    border-radius: 4px;
    text-align: center;
    min-height: 8px;
    max-height: 8px;
}
QProgressBar::chunk {
    background-color: #0078d4;
    border-radius: 4px;
}

/* ---- 菜单 ---- */
QMenuBar {
    background-color: #f3f3f3;
    border-bottom: 1px solid #e0e0e0;
    padding: 2px 0px;
}
QMenuBar::item {
    background-color: transparent;
    padding: 4px 12px;
    border-radius: 4px;
}
QMenuBar::item:selected {
    background-color: #e5e5e5;
}
QMenu {
    background-color: #ffffff;
    border: 1px solid #d1d1d1;
    border-radius: 8px;
    padding: 4px;
}
QMenu::item {
    padding: 6px 24px;
    border-radius: 4px;
}
QMenu::item:selected {
    background-color: #e5f1fb;
}
QMenu::separator {
    height: 1px;
    background-color: #e0e0e0;
    margin: 4px 8px;
}

/* ---- 状态栏 ---- */
QStatusBar {
    background-color: #f0f0f0;
    border-top: 1px solid #e0e0e0;
    color: #606060;
    font-size: 9pt;
    padding: 2px 8px;
}
QStatusBar::item {
    border: none;
}
QStatusBar QLabel {
    padding: 0 8px;
    font-size: 9pt;
}

/* ---- 工具按钮 ---- */
QToolButton {
    border: none;
    border-radius: 4px;
    padding: 4px;
}
QToolButton:hover {
    background-color: #e8e8e8;
}
QToolButton:pressed {
    background-color: #d0d0d0;
}

/* ---- 分隔线 ---- */
QSplitter::handle {
    background-color: #e0e0e0;
}
QSplitter::handle:horizontal {
    width: 1px;
}
QSplitter::handle:vertical {
    height: 1px;
}

/* ---- 表单布局 ---- */
QFormLayout {
    spacing: 6px;
}

/* ---- 文本编辑 ---- */
QTextEdit {
    background-color: #ffffff;
    border: 1px solid #e0e0e0;
    border-radius: 4px;
    padding: 8px;
    font-family: "Consolas", "Cascadia Code", monospace;
    selection-background-color: #0078d4;
    selection-color: white;
}
"""


def apply_windows11_style(app: QApplication) -> None:
    """为应用设置 Windows 11 原生风格全局样式。"""
    app.setStyleSheet(WINDOWS11_QSS)
