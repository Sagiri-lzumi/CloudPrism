"""Material Design 全局主题（基于开源库 qt-material）。

提供 apply_theme() 函数，为 QApplication 应用 Material Design 样式表，
支持浅色 / 深色 / 跟随系统三种模式，运行时可随时切换：
  - 浅色：light_blue.xml（蓝强调色延续品牌色 #0067b8）
  - 深色：dark_blue.xml
  - 跟随系统：经 styleHints().colorScheme()（Qt 6.5+）解析为明/暗

另提供 semantic_color() 语义色表（明暗双套），供状态标签等控件级
setStyleSheet 覆盖取色，保证两种主题下文字均可读。

注意：qt_material 必须在 PySide6 之后导入（库要求）。
"""

from __future__ import annotations

import logging

from PySide6.QtCore import Qt
from PySide6.QtGui import QGuiApplication
from PySide6.QtWidgets import QApplication

try:
    from qt_material import apply_stylesheet as _qtm_apply_stylesheet

    _QTM_AVAILABLE = True
except ImportError:  # 依赖缺失时降级为系统默认样式，不阻断启动
    _QTM_AVAILABLE = False

logger = logging.getLogger(__name__)

# 模式 -> qt-material 主题文件
THEMES = {
    "light": "light_blue.xml",
    "dark": "dark_blue.xml",
}

# 字体族：保留中文友好栈（qt-material 模板写入 QWidget 基础字体）
FONT_FAMILY = '"Segoe UI Variable", "Segoe UI", "Microsoft YaHei UI", sans-serif'

# 模块级状态：当前已应用的模式与字号（供字号变更时重套样式）
_current_mode = "light"
_current_font_size = 14

# 语义色表（控件级状态文字用，明暗两套保证可读性）
_SEMANTIC_COLORS = {
    "light": {
        "ok": "#2e7d32",
        "err": "#c62828",
        "warn": "#ef6c00",
        "muted": "#5c5c5c",
        "link": "#0067b8",
        "heading": "#1a1a1a",
    },
    "dark": {
        "ok": "#66bb6a",
        "err": "#ef5350",
        "warn": "#ffb74d",
        "muted": "#9e9e9e",
        "link": "#64b5f6",
        "heading": "",  # 深色下不覆盖，控件继承主题文字色
    },
}


def system_prefers_dark() -> bool:
    """系统当前是否处于深色模式（Qt 6.5+ styleHints.colorScheme）。"""
    hints = QGuiApplication.styleHints()
    if hints is None:
        return False
    return hints.colorScheme() == Qt.ColorScheme.Dark


def resolve_mode(mode: str) -> str:
    """把 ``system`` 解析为具体的 ``light`` / ``dark``；未知值回退浅色。"""
    if mode == "system":
        return "dark" if system_prefers_dark() else "light"
    if mode in THEMES:
        return mode
    return "light"


def current_mode() -> str:
    """当前已应用的具体模式（``light`` / ``dark``）。"""
    return _current_mode


def apply_theme(app: QApplication, mode: str, font_size: int | None = None) -> None:
    """应用主题样式表。

    :param app: QApplication 实例
    :param mode: ``system`` / ``dark`` / ``light``
    :param font_size: 字号（pt）；None 表示沿用上次值。设置页字号为
        pt 单位，qt-material 模板使用 px，此处按 96 DPI 换算。
    """
    global _current_mode, _current_font_size
    resolved = resolve_mode(mode)
    _current_mode = resolved
    if font_size is not None:
        _current_font_size = font_size
    if not _QTM_AVAILABLE:
        logger.warning("qt-material 未安装，回退系统默认样式")
        app.setStyleSheet("")
        return
    _qtm_apply_stylesheet(
        app,
        theme=THEMES[resolved],
        extra={
            "density_scale": 0,
            "font_family": FONT_FAMILY,
            # pt -> px（96 DPI：1pt = 4/3 px）
            "font_size": round(_current_font_size * 4 / 3),
        },
    )


def semantic_color(kind: str) -> str:
    """按当前主题返回语义色（十六进制字符串）；深色无对应色时返回空串。"""
    return _SEMANTIC_COLORS[_current_mode].get(kind, "")


# 向后兼容：旧名称（等同应用浅色主题）
def apply_fluent_style(app: QApplication) -> None:
    """向后兼容：等同于 ``apply_theme(app, "light")``。"""
    apply_theme(app, "light")
