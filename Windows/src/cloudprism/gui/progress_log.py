"""实时进度日志框。

建库/开库/连接等耗时操作期间的阶段性日志（如"校验主密码（密钥派生，
约需数秒）…"）逐条带时间戳展示，让用户清楚知道系统正在做什么，
避免数秒级等待被误认为"程序卡死"。
"""

from __future__ import annotations

import html
import time

from PySide6.QtGui import QFont, QTextCursor
from PySide6.QtWidgets import QLabel, QTextEdit, QVBoxLayout, QWidget

from cloudprism.gui.theme import semantic_color


class ProgressLogBox(QWidget):
    """实时日志框：标题 + 只读日志区（追加后自动滚动到底部）。"""

    # 日志行数上限：超长任务时淘汰最旧行，防止文本无限增长
    _MAX_LINES = 200

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        lay = QVBoxLayout(self)
        lay.setContentsMargins(0, 0, 0, 0)
        lay.setSpacing(4)

        title = QLabel("处理日志", self)
        title.setStyleSheet(f"color: {semantic_color('muted')}; font-size: 12px;")
        lay.addWidget(title)

        self._view = QTextEdit(self)
        self._view.setReadOnly(True)
        # 等宽字体 + 固定高度：日志观感与布局稳定（明暗主题均用控件默认底色）
        self._view.setFont(QFont("Consolas", 9))
        self._view.setFixedHeight(110)
        self._view.setStyleSheet(
            "QTextEdit { border: 1px solid rgba(128,128,128,90); "
            "border-radius: 4px; padding: 4px; font-size: 12px; }"
        )
        lay.addWidget(self._view)
        self._line_count = 0

    def append_log(self, msg: str) -> None:
        """追加一条日志（自动加时间戳）并滚动到底部。"""
        stamp = time.strftime("%H:%M:%S")
        # 转义防 HTML 注入：append 按富文本解析，日志内容可能含特殊字符
        self._view.append(html.escape(f"[{stamp}]  {msg}"))
        self._line_count += 1
        if self._line_count > self._MAX_LINES:
            # 淘汰最旧一行（含行尾换行），保持总量可控
            cursor = self._view.textCursor()
            cursor.movePosition(QTextCursor.MoveOperation.Start)
            cursor.select(QTextCursor.SelectionType.LineUnderCursor)
            cursor.removeSelectedText()
            cursor.deleteChar()
            self._line_count -= 1
        bar = self._view.verticalScrollBar()
        bar.setValue(bar.maximum())

    def clear_log(self) -> None:
        """清空全部日志（新一轮操作开始前调用）。"""
        self._view.clear()
        self._line_count = 0

    def log_text(self) -> str:
        """当前日志全文（测试断言用）。"""
        return self._view.toPlainText()
