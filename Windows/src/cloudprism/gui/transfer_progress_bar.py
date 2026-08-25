"""底部内嵌传输进度条。

TransferProgressBar 嵌入主窗口底部（状态栏上方），
在有传输任务时自动显示，全部完成后自动隐藏。

支持：
  - 显示当前传输文件名与进度
  - 多任务队列计数（"正在上传 2/5…"）
  - 取消按钮
"""

from __future__ import annotations

from PySide6.QtCore import Signal, Qt
from PySide6.QtWidgets import (
    QHBoxLayout,
    QLabel,
    QProgressBar,
    QPushButton,
    QVBoxLayout,
    QWidget,
)


class TransferProgressBar(QWidget):
    """底部内嵌传输进度条（默认隐藏）。"""

    # 用户点击取消
    cancelRequested = Signal()

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self.setVisible(False)  # 默认隐藏

        lay = QVBoxLayout(self)
        lay.setContentsMargins(8, 4, 8, 4)
        lay.setSpacing(2)

        # 上方：文件名 + 队列计数 + 取消按钮
        top_row = QHBoxLayout()
        self._name_label = QLabel("", self)
        self._name_label.setStyleSheet("font-size: 9pt; color: #1a1a1a;")
        top_row.addWidget(self._name_label)

        self._queue_label = QLabel("", self)
        self._queue_label.setStyleSheet("font-size: 9pt; color: #5c5c5c;")
        top_row.addWidget(self._queue_label)

        top_row.addStretch()

        self._cancel_btn = QPushButton("取消", self)
        self._cancel_btn.setFixedHeight(24)
        self._cancel_btn.setStyleSheet(
            "QPushButton { padding: 2px 12px; font-size: 9pt; }"
        )
        self._cancel_btn.clicked.connect(self.cancelRequested.emit)
        top_row.addWidget(self._cancel_btn)

        lay.addLayout(top_row)

        # 下方：进度条
        self._bar = QProgressBar(self)
        self._bar.setRange(0, 100)
        self._bar.setValue(0)
        self._bar.setTextVisible(False)
        self._bar.setFixedHeight(6)
        lay.addWidget(self._bar)

        # 内部状态
        self._current_index = 0
        self._total_count = 0
        self._current_cancel_fn = None  # 当前任务的取消函数
        self._last_name = ""            # 最近一次任务名（完成后展示用）
        self._hide_timer = None         # 完成后延迟隐藏的定时器

    # ------------------------------------------------------------------
    # 公开方法
    # ------------------------------------------------------------------

    def show_task(
        self,
        file_name: str,
        direction: str = "upload",
        current: int = 1,
        total: int = 1,
        cancel_fn=None,
    ) -> None:
        """显示一个传输任务。

        参数:
            file_name: 正在传输的文件名
            direction: "upload" 或 "download"
            current: 当前任务序号（从 1 开始）
            total: 总任务数
            cancel_fn: 取消回调函数
        """
        self._current_index = current
        self._total_count = total
        self._current_cancel_fn = cancel_fn
        self._last_name = file_name

        action = "加密上传" if direction == "upload" else "下载解密"
        self._name_label.setText(f"{action}：{file_name}")

        if total > 1:
            self._queue_label.setText(f"({current}/{total})")
        else:
            self._queue_label.setText("")

        # 新任务开始：取消待定的隐藏
        if self._hide_timer is not None:
            self._hide_timer.stop()
        self._bar.setValue(0)
        self._cancel_btn.setEnabled(True)
        self._cancel_btn.setText("取消")
        self.setVisible(True)

    def update_progress(self, progress: float) -> None:
        """更新进度（0.0~1.0）。"""
        self._bar.setValue(int(progress * 100))

    def task_finished(self, success: bool = True) -> None:
        """当前任务完成或失败；全部结束时短暂停留展示结果后隐藏。"""
        if success:
            self._bar.setValue(100)
        if self._current_index >= self._total_count:
            # 本批全部结束：展示结果并停留 3 秒后隐藏（避免瞬间消失）
            suffix = f"：{self._last_name}" if self._last_name else ""
            self._name_label.setText(
                ("✓ 传输完成" if success else "✗ 传输失败") + suffix
            )
            self._queue_label.setText("")
            self._cancel_btn.setEnabled(False)
            from PySide6.QtCore import QTimer

            if self._hide_timer is None:
                self._hide_timer = QTimer(self)
                self._hide_timer.setSingleShot(True)
                self._hide_timer.timeout.connect(self._finish_all)
            self._hide_timer.start(3000)

    def hide_bar(self) -> None:
        """强制隐藏进度条。"""
        self._finish_all()

    # ------------------------------------------------------------------
    # 内部方法
    # ------------------------------------------------------------------

    def _finish_all(self) -> None:
        """所有任务完成，隐藏进度条并重置状态。"""
        self.setVisible(False)
        self._current_cancel_fn = None
        self._last_name = ""
        self._queue_label.setText("")
        self._bar.setValue(0)

    def _on_cancel(self) -> None:
        """取消按钮内部处理（由信号转发）。"""
        self._cancel_btn.setEnabled(False)
        self._cancel_btn.setText("正在取消…")
        if self._current_cancel_fn:
            self._current_cancel_fn()
