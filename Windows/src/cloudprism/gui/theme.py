"""Fluent 风格全局主题。

提供 apply_fluent_style() 函数，为 QApplication 设置统一 QSS 样式表，
参考 Windows 11 Fluent Design System：
  - 微暖灰背景、纯白卡片
  - 沉稳蓝色强调、柔和边框
  - 清晰的交互反馈与视觉层次
  - 舒适的间距与排版
"""

from __future__ import annotations

from PySide6.QtWidgets import QApplication


# Fluent 风格全局样式表
FLUENT_QSS = """
/* ================================================================
   全局基础
   ================================================================ */
QWidget {
    font-family: "Segoe UI Variable", "Segoe UI", "Microsoft YaHei UI", sans-serif;
    font-size: 10pt;
    color: #1a1a1a;
}

QMainWindow {
    background-color: #f5f5f5;
}

/* ================================================================
   按钮
   ================================================================ */
QPushButton {
    background-color: #ffffff;
    border: 1px solid #d4d4d4;
    border-radius: 6px;
    padding: 6px 20px;
    min-height: 28px;
    color: #1a1a1a;
    font-weight: 500;
}
QPushButton:hover {
    background-color: #f0f0f0;
    border-color: #c4c4c4;
}
QPushButton:pressed {
    background-color: #e6e6e6;
    border-color: #b8b8b8;
}
QPushButton:disabled {
    color: #a0a0a0;
    background-color: #fafafa;
    border-color: #e8e8e8;
}
QPushButton:default {
    border: 1.5px solid #0067b8;
}
QPushButton:default:hover {
    background-color: #f0f6ff;
}

/* ================================================================
   输入框
   ================================================================ */
QLineEdit {
    background-color: #ffffff;
    border: 1px solid #d4d4d4;
    border-radius: 6px;
    padding: 5px 10px;
    min-height: 28px;
    selection-background-color: #0067b8;
    selection-color: white;
}
QLineEdit:focus {
    border: 1px solid #0067b8;
    border-bottom: 2px solid #0067b8;
}
QLineEdit:disabled {
    background-color: #f9f9f9;
    color: #a0a0a0;
    border-color: #e8e8e8;
}

/* ================================================================
   下拉框
   ================================================================ */
QComboBox {
    background-color: #ffffff;
    border: 1px solid #d4d4d4;
    border-radius: 6px;
    padding: 5px 10px;
    min-height: 28px;
}
QComboBox:hover {
    border-color: #0067b8;
}
QComboBox:focus {
    border: 1px solid #0067b8;
    border-bottom: 2px solid #0067b8;
}
QComboBox::drop-down {
    border: none;
    width: 28px;
}
QComboBox::down-arrow {
    image: none;
    border-left: 4px solid transparent;
    border-right: 4px solid transparent;
    border-top: 5px solid #5c5c5c;
    margin-right: 8px;
}
QComboBox QAbstractItemView {
    background-color: #ffffff;
    border: 1px solid #d4d4d4;
    border-radius: 6px;
    selection-background-color: #f0f6ff;
    selection-color: #1a1a1a;
    outline: none;
    padding: 4px;
}

/* ================================================================
   数字输入
   ================================================================ */
QSpinBox {
    background-color: #ffffff;
    border: 1px solid #d4d4d4;
    border-radius: 6px;
    padding: 5px 10px;
    min-height: 28px;
}
QSpinBox:focus {
    border: 1px solid #0067b8;
    border-bottom: 2px solid #0067b8;
}

/* ================================================================
   分组框
   ================================================================ */
QGroupBox {
    background-color: #fafafa;
    border: 1px solid #e8e8e8;
    border-radius: 8px;
    margin-top: 16px;
    padding: 16px 12px 12px 12px;
    font-weight: 600;
    font-size: 10pt;
}
QGroupBox::title {
    subcontrol-origin: margin;
    subcontrol-position: top left;
    padding: 0 8px;
    color: #1a1a1a;
    background-color: #fafafa;
}

/* ================================================================
   树视图
   ================================================================ */
QTreeView {
    background-color: #ffffff;
    border: 1px solid #e5e5e5;
    border-radius: 6px;
    alternate-background-color: #fafafa;
    outline: none;
    selection-background-color: #e8f0fe;
    selection-color: #1a1a1a;
}
QTreeView::item {
    padding: 4px 6px;
    border: none;
    border-radius: 4px;
    margin: 1px 2px;
}
QTreeView::item:hover {
    background-color: #f0f0f0;
}
QTreeView::item:selected {
    background-color: #e8f0fe;
    color: #1a1a1a;
}
QTreeView::branch {
    background-color: transparent;
}

/* ================================================================
   列表视图
   ================================================================ */
QListView {
    background-color: #ffffff;
    border: 1px solid #e5e5e5;
    border-radius: 6px;
    alternate-background-color: #fafafa;
    outline: none;
    selection-background-color: #e8f0fe;
    selection-color: #1a1a1a;
}
QListWidget {
    background-color: #ffffff;
    border: 1px solid #e5e5e5;
    border-radius: 6px;
    alternate-background-color: #fafafa;
    outline: none;
    padding: 4px;
}
QListWidget::item {
    padding: 4px 6px;
    border-radius: 4px;
    margin: 1px 0px;
}
QListWidget::item:hover {
    background-color: #f0f0f0;
}
QListWidget::item:selected {
    background-color: #e8f0fe;
    color: #1a1a1a;
}

/* ================================================================
   标签
   ================================================================ */
QLabel {
    background-color: transparent;
    border: none;
    padding: 0px;
}

/* ================================================================
   滚动条
   ================================================================ */
QScrollBar:vertical {
    background-color: transparent;
    width: 10px;
    border: none;
    margin: 4px 0px;
}
QScrollBar::handle:vertical {
    background-color: #c8c8c8;
    border-radius: 5px;
    min-height: 24px;
    margin: 2px;
}
QScrollBar::handle:vertical:hover {
    background-color: #a8a8a8;
}
QScrollBar::handle:vertical:pressed {
    background-color: #888888;
}
QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical {
    height: 0px;
}
QScrollBar::add-page:vertical, QScrollBar::sub-page:vertical {
    background: transparent;
}
QScrollBar:horizontal {
    background-color: transparent;
    height: 10px;
    border: none;
    margin: 0px 4px;
}
QScrollBar::handle:horizontal {
    background-color: #c8c8c8;
    border-radius: 5px;
    min-width: 24px;
    margin: 2px;
}
QScrollBar::handle:horizontal:hover {
    background-color: #a8a8a8;
}
QScrollBar::handle:horizontal:pressed {
    background-color: #888888;
}
QScrollBar::add-line:horizontal, QScrollBar::sub-line:horizontal {
    width: 0px;
}
QScrollBar::add-page:horizontal, QScrollBar::sub-page:horizontal {
    background: transparent;
}

/* ================================================================
   选项卡
   ================================================================ */
QTabWidget::pane {
    border: 1px solid #e5e5e5;
    border-radius: 6px;
    background-color: #ffffff;
    top: -1px;
}
QTabBar::tab {
    background-color: transparent;
    border: none;
    border-bottom: 2px solid transparent;
    padding: 8px 16px;
    margin-right: 4px;
    color: #5c5c5c;
    font-weight: 500;
}
QTabBar::tab:selected {
    color: #0067b8;
    border-bottom: 2px solid #0067b8;
}
QTabBar::tab:hover:!selected {
    background-color: #f0f0f0;
    border-radius: 4px 4px 0 0;
    color: #1a1a1a;
}

/* ================================================================
   进度条
   ================================================================ */
QProgressBar {
    background-color: #e5e5e5;
    border: none;
    border-radius: 4px;
    text-align: center;
    min-height: 6px;
    max-height: 6px;
}
QProgressBar::chunk {
    background-color: #0067b8;
    border-radius: 4px;
}

/* ================================================================
   菜单
   ================================================================ */
QMenuBar {
    background-color: #f5f5f5;
    border-bottom: 1px solid #e5e5e5;
    padding: 2px 0px;
}
QMenuBar::item {
    background-color: transparent;
    padding: 5px 12px;
    border-radius: 5px;
    margin: 2px 1px;
}
QMenuBar::item:selected {
    background-color: #e8e8e8;
}
QMenu {
    background-color: #ffffff;
    border: 1px solid #d4d4d4;
    border-radius: 8px;
    padding: 4px;
}
QMenu::item {
    padding: 7px 28px;
    border-radius: 5px;
    margin: 1px 0px;
}
QMenu::item:selected {
    background-color: #f0f6ff;
}
QMenu::item:disabled {
    color: #a0a0a0;
}
QMenu::separator {
    height: 1px;
    background-color: #e8e8e8;
    margin: 4px 12px;
}

/* ================================================================
   状态栏
   ================================================================ */
QStatusBar {
    background-color: #f0f0f0;
    border-top: 1px solid #e5e5e5;
    color: #5c5c5c;
    font-size: 9pt;
    padding: 3px 12px;
}
QStatusBar::item {
    border: none;
}
QStatusBar QLabel {
    padding: 0 10px;
    font-size: 9pt;
    color: #5c5c5c;
}

/* ================================================================
   工具按钮
   ================================================================ */
QToolButton {
    border: none;
    border-radius: 6px;
    padding: 6px;
}
QToolButton:hover {
    background-color: #e8e8e8;
}
QToolButton:pressed {
    background-color: #d8d8d8;
}
QToolButton:checked {
    background-color: #e0e0e0;
}

/* ================================================================
   分隔线
   ================================================================ */
QSplitter::handle {
    background-color: #e5e5e5;
}
QSplitter::handle:horizontal {
    width: 1px;
}
QSplitter::handle:vertical {
    height: 1px;
}

/* ================================================================
   表单布局
   ================================================================ */
QFormLayout {
    spacing: 8px;
}

/* ================================================================
   文本编辑
   ================================================================ */
QTextEdit {
    background-color: #ffffff;
    border: 1px solid #e5e5e5;
    border-radius: 6px;
    padding: 10px;
    font-family: "Cascadia Code", "Consolas", monospace;
    selection-background-color: #0067b8;
    selection-color: white;
}
QTextEdit:focus {
    border: 1px solid #0067b8;
}

/* ================================================================
   复选框 & 单选框
   ================================================================ */
QCheckBox {
    spacing: 8px;
    min-height: 24px;
}
QCheckBox::indicator {
    width: 18px;
    height: 18px;
    border: 1.5px solid #888888;
    border-radius: 4px;
    background-color: #ffffff;
}
QCheckBox::indicator:hover {
    border-color: #0067b8;
}
QCheckBox::indicator:checked {
    background-color: #0067b8;
    border-color: #0067b8;
}
QRadioButton {
    spacing: 8px;
    min-height: 24px;
}
QRadioButton::indicator {
    width: 18px;
    height: 18px;
    border: 1.5px solid #888888;
    border-radius: 10px;
    background-color: #ffffff;
}
QRadioButton::indicator:hover {
    border-color: #0067b8;
}
QRadioButton::indicator:checked {
    background-color: #0067b8;
    border-color: #0067b8;
}

/* ================================================================
   滑块
   ================================================================ */
QSlider::groove:horizontal {
    height: 4px;
    background-color: #e0e0e0;
    border-radius: 2px;
}
QSlider::handle:horizontal {
    background-color: #0067b8;
    width: 16px;
    height: 16px;
    margin: -6px 0px;
    border-radius: 8px;
}
QSlider::handle:horizontal:hover {
    background-color: #106ebe;
}
QSlider::sub-page:horizontal {
    background-color: #0067b8;
    border-radius: 2px;
}

/* ================================================================
   提示框
   ================================================================ */
QToolTip {
    background-color: #ffffff;
    border: 1px solid #d4d4d4;
    border-radius: 6px;
    padding: 6px 10px;
    color: #1a1a1a;
    font-size: 9pt;
}

/* ================================================================
   滚动区域
   ================================================================ */
QScrollArea {
    background-color: transparent;
    border: none;
}
"""


def apply_fluent_style(app: QApplication) -> None:
    """为应用设置 Fluent 风格全局主题。"""
    app.setStyleSheet(FLUENT_QSS)


# 向后兼容：旧名称
def apply_windows11_style(app: QApplication) -> None:
    """向后兼容：等同于 apply_fluent_style()。"""
    apply_fluent_style(app)
