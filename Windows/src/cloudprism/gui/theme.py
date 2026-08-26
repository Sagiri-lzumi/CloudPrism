"""Fluent 全局主题（基于开源库 QFluentWidgets / PySide6-Fluent-Widgets）。

提供 apply_theme() 函数，把库的明暗主题应用到整个应用，
支持浅色 / 深色 / 跟随系统三种模式，运行时可随时切换：
  - 浅色 / 深色：Theme.LIGHT / Theme.DARK
  - 跟随系统：Theme.AUTO（库经 darkdetect 自动跟随系统配色）

强调色固定为品牌蓝 #0067b8。库自身负责全部控件样式与动画，
本模块不再下发全局 QSS。

另提供 semantic_color() 语义色表（明暗双套），供状态标签等控件级
setStyleSheet 覆盖取色，保证两种主题下文字均可读。

注意：qfluentwidgets 必须在 PySide6 之后导入（库要求）。
"""

from __future__ import annotations

import logging

from PySide6.QtCore import Qt
from PySide6.QtGui import QGuiApplication
from PySide6.QtWidgets import QApplication

try:
    from qfluentwidgets import Theme as _QfwTheme
    from qfluentwidgets import setTheme as _qfw_set_theme
    from qfluentwidgets import setThemeColor as _qfw_set_theme_color

    _QFW_AVAILABLE = True
except ImportError:  # 依赖缺失时降级为系统默认样式，不阻断启动
    _QFW_AVAILABLE = False

logger = logging.getLogger(__name__)

# 品牌强调色（与原 Fluent 蓝一致）
THEME_COLOR = "#0067b8"

# 字体族：保留中文友好栈
FONT_FAMILY = '"Segoe UI Variable", "Segoe UI", "Microsoft YaHei UI", sans-serif'

# 模块级状态：当前已应用的具体模式与字号（供字号变更时重套样式）
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
    if mode in ("light", "dark"):
        return mode
    return "light"


def current_mode() -> str:
    """当前已应用的具体模式（``light`` / ``dark``）。

    ``system`` 模式下返回按系统偏好解析后的具体明/暗，
    供语义色表等需要具体取值的场景使用。
    """
    return _current_mode


def apply_theme(app: QApplication, mode: str, font_size: int | None = None) -> None:
    """应用 Fluent 主题。

    :param app: QApplication 实例
    :param mode: ``system`` / ``dark`` / ``light``；``system`` 映射为
        库的 AUTO 主题（经 darkdetect 实时跟随系统配色）
    :param font_size: 字号（pt）；None 表示沿用上次值
    """
    global _current_mode, _current_font_size
    if mode not in ("system", "light", "dark"):
        mode = "light"
    # 语义色等需要具体明/暗：system 按系统偏好解析
    _current_mode = "dark" if (
        mode == "dark" or (mode == "system" and system_prefers_dark())
    ) else "light"
    if font_size is not None:
        _current_font_size = font_size
    # 字号与字体族（库控件同样继承应用级字体）
    font = app.font()
    font.setPointSize(_current_font_size)
    app.setFont(font)
    if not _QFW_AVAILABLE:
        logger.warning("qfluentwidgets 未安装，回退系统默认样式")
        app.setStyleSheet("")
        return
    # AUTO/DARK/LIGHT；切主题时重设强调色（库切主题可能复位）
    qfw_mode = {
        "system": _QfwTheme.AUTO,
        "dark": _QfwTheme.DARK,
        "light": _QfwTheme.LIGHT,
    }[mode]
    _qfw_set_theme(qfw_mode)
    _qfw_set_theme_color(THEME_COLOR)


def semantic_color(kind: str) -> str:
    """按当前主题返回语义色（十六进制字符串）；深色无对应色时返回空串。"""
    return _SEMANTIC_COLORS[_current_mode].get(kind, "")


# 向后兼容：旧名称（等同应用浅色主题）
def apply_fluent_style(app: QApplication) -> None:
    """向后兼容：等同于 ``apply_theme(app, "light")``。"""
    apply_theme(app, "light")
