"""ProgressLogBox 实时进度日志框测试（pytest-qt）。

验证追加日志带时间戳、清空、超长淘汰行为。
"""

from __future__ import annotations

import re

import pytest

pytest.importorskip("PySide6")

from cloudprism.gui.progress_log import ProgressLogBox


# ---------------------------------------------------------------------------
# 测试
# ---------------------------------------------------------------------------


class TestProgressLogBox:
    """实时日志框基础行为。"""

    def test_append_log_with_timestamp(self, qtbot):
        """追加日志：自动带 [HH:MM:SS] 时间戳且包含原文案。"""
        box = ProgressLogBox()
        qtbot.addWidget(box)
        box.append_log("校验主密码（密钥派生，约需数秒）…")
        text = box.log_text()
        assert re.search(r"\[\d{2}:\d{2}:\d{2}\]", text)
        assert "校验主密码（密钥派生，约需数秒）…" in text

    def test_append_multiple_keeps_order(self, qtbot):
        """多条日志按追加顺序保留。"""
        box = ProgressLogBox()
        qtbot.addWidget(box)
        box.append_log("第一条")
        box.append_log("第二条")
        text = box.log_text()
        assert text.index("第一条") < text.index("第二条")

    def test_clear_log(self, qtbot):
        """清空后全文为空。"""
        box = ProgressLogBox()
        qtbot.addWidget(box)
        box.append_log("临时文案")
        assert "临时文案" in box.log_text()
        box.clear_log()
        assert box.log_text() == ""

    def test_max_lines_trims_oldest(self, qtbot):
        """超过行数上限时淘汰最旧行，总量不超上限。"""
        box = ProgressLogBox()
        qtbot.addWidget(box)
        for i in range(box._MAX_LINES + 5):
            box.append_log(f"line-{i}")
        lines = [ln for ln in box.log_text().splitlines() if ln.strip()]
        assert len(lines) <= box._MAX_LINES
        # 最旧几条已被淘汰，最新一条仍在
        assert "line-0" not in box.log_text()
        assert f"line-{box._MAX_LINES + 4}" in box.log_text()

    def test_html_escaped(self, qtbot):
        """日志文案按纯文本展示（HTML 特殊字符不被解析）。"""
        box = ProgressLogBox()
        qtbot.addWidget(box)
        box.append_log("<b>加粗</b> & 其他")
        # toPlainText 返回转义还原后的文本，标签不作为格式消耗
        assert "<b>加粗</b> & 其他" in box.log_text()
