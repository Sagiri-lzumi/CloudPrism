"""主题系统测试（qt-material 包装层 + 语义色表）。

全部离屏运行；用 fixture 保存/恢复全局样式表与模块状态，
避免影响同会话内的其他测试文件。
"""

import pytest
from PySide6.QtWidgets import QApplication

from cloudprism.gui import theme as theme_mod
from cloudprism.gui.theme import (
    apply_fluent_style,
    apply_theme,
    current_mode,
    resolve_mode,
    semantic_color,
    system_prefers_dark,
)


@pytest.fixture(autouse=True)
def _restore_theme():
    """保存/恢复全局样式表与模块级主题状态。"""
    app = QApplication.instance()
    saved_qss = app.styleSheet() if app is not None else ""
    saved_mode = theme_mod._current_mode
    saved_size = theme_mod._current_font_size
    yield
    theme_mod._current_mode = saved_mode
    theme_mod._current_font_size = saved_size
    app = QApplication.instance()
    if app is not None:
        app.setStyleSheet(saved_qss)


def test_apply_light_and_dark_differ(qtbot):
    """明暗主题应用后样式表均非空且互不相同。"""
    app = QApplication.instance()
    apply_theme(app, "light")
    light_qss = app.styleSheet()
    assert current_mode() == "light"
    apply_theme(app, "dark")
    dark_qss = app.styleSheet()
    assert current_mode() == "dark"
    assert light_qss and dark_qss
    assert light_qss != dark_qss


def test_system_mode_resolves_and_applies(qtbot):
    """mode=system 不抛异常，且解析为具体的明/暗。"""
    app = QApplication.instance()
    apply_theme(app, "system")
    assert current_mode() in ("light", "dark")
    assert app.styleSheet() != ""


def test_resolve_mode_unknown_falls_back_to_light():
    assert resolve_mode("light") == "light"
    assert resolve_mode("dark") == "dark"
    assert resolve_mode("system") in ("light", "dark")
    assert resolve_mode("no-such-mode") == "light"


def test_semantic_color_varies_by_mode(qtbot):
    """同一语义键在明暗两态取值不同，且均为合法十六进制。"""
    app = QApplication.instance()
    apply_theme(app, "light")
    light_err = semantic_color("err")
    apply_theme(app, "dark")
    dark_err = semantic_color("err")
    assert light_err != dark_err
    for color in (light_err, dark_err, semantic_color("ok"),
                  semantic_color("warn"), semantic_color("muted"),
                  semantic_color("link")):
        assert color.startswith("#") and len(color) == 7


def test_semantic_color_heading_and_unknown():
    """浅色标题色非空；深色标题色为空串（控件继承主题文字色）；未知键空串。"""
    theme_mod._current_mode = "light"
    assert semantic_color("heading") == "#1a1a1a"
    theme_mod._current_mode = "dark"
    assert semantic_color("heading") == ""
    assert semantic_color("no-such-key") == ""


def test_apply_fluent_style_alias(qtbot):
    """兼容别名：等同应用浅色主题。"""
    apply_fluent_style(QApplication.instance())
    assert current_mode() == "light"
    assert QApplication.instance().styleSheet() != ""


def test_system_prefers_dark_returns_bool():
    assert isinstance(system_prefers_dark(), bool)


def test_font_size_persists_across_reapply(qtbot):
    """font_size=None 时沿用上次字号（字号变更后重套不丢失）。"""
    app = QApplication.instance()
    apply_theme(app, "light", font_size=18)
    assert theme_mod._current_font_size == 18
    apply_theme(app, "dark")
    assert theme_mod._current_font_size == 18
    assert current_mode() == "dark"
