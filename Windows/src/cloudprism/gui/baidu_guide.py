"""百度网盘凭证申请教程对话框。

按需展示（不主动弹出）：用户点击设置页「如何申请凭证…」时打开。
教程正文来自打包资源 ``assets/baidu_guide.md``；加载失败时显示兜底
文本，不抛异常。
"""

from __future__ import annotations

import os
import sys

from PySide6.QtCore import Qt
from PySide6.QtWidgets import (
    QDialog,
    QPushButton,
    QTextBrowser,
    QVBoxLayout,
)

# 资源缺失时的兜底文案
_FALLBACK_TEXT = (
    "# 教程文件缺失\n\n"
    "未能读取内置教程文件，请参阅仓库中的 "
    "《Plan/百度网盘开放平台申请指南》。\n"
)


def _find_guide_path() -> str | None:
    """定位教程文件：打包内嵌资源优先，其次源码树。"""
    base = getattr(sys, "_MEIPASS", None)
    candidates = []
    if base:
        candidates.append(os.path.join(base, "assets", "baidu_guide.md"))
    here = os.path.dirname(os.path.abspath(__file__))
    # gui/ -> 包根 -> src/ -> Windows/
    candidates.append(
        os.path.join(os.path.dirname(os.path.dirname(here)), "..", "assets", "baidu_guide.md")
    )
    for cand in candidates:
        if os.path.exists(cand):
            return cand
    return None


def load_guide_text(guide_path: str | None = None) -> str:
    """读取教程正文；失败返回兜底文本。"""
    path = guide_path or _find_guide_path()
    if path:
        try:
            with open(path, encoding="utf-8") as f:
                return f.read()
        except OSError:
            pass
    return _FALLBACK_TEXT


class BaiduGuideDialog(QDialog):
    """只读展示百度网盘凭证申请教程。"""

    def __init__(self, parent=None, guide_path: str | None = None) -> None:
        super().__init__(parent)
        self.setWindowTitle("百度网盘凭证申请教程")
        self.resize(640, 520)

        lay = QVBoxLayout(self)

        self._browser = QTextBrowser(self)
        self._browser.setOpenExternalLinks(True)
        self._browser.setMarkdown(load_guide_text(guide_path))
        lay.addWidget(self._browser)

        close_btn = QPushButton("关闭", self)
        close_btn.clicked.connect(self.accept)
        lay.addWidget(close_btn, alignment=Qt.AlignmentFlag.AlignRight)

    @property
    def text(self) -> str:
        """教程正文（供测试断言）。"""
        return self._browser.toMarkdown()
