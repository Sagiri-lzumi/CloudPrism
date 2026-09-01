"""流式预览播放器组件。

PlayerWidget（QWidget）：可嵌入的流式解密播放器，供预览面板使用。
PlayerView（QDialog）：独立窗口包装，向后兼容测试与旧调用方式。

工作原理：
  1. 启动 DecryptingProxyServer（本地 HTTP）
  2. 把代理 URL 喂给 QMediaPlayer（播放器按 Range 请求按需拉流）
  3. 播放器 Seek -> 新 Range 请求 -> 代理即时换算密文偏移解密

明文仅在内存中流动，不落盘。
"""

from __future__ import annotations

from urllib.parse import quote

from PySide6.QtCore import QUrl, Qt
from PySide6.QtMultimedia import QAudioOutput, QMediaPlayer
from PySide6.QtMultimediaWidgets import QVideoWidget
from PySide6.QtWidgets import (
    QDialog,
    QHBoxLayout,
    QLabel,
    QPushButton,
    QSlider,
    QVBoxLayout,
    QWidget,
)

from cloudprism.core.session import Session
from cloudprism.storage.backend import StorageBackend
from cloudprism.streaming.proxy_server import start_proxy, stop_proxy


class PlayerWidget(QWidget):
    """可嵌入的流式解密播放器组件。

    保留全部播放控制逻辑（代理启停、Range、Seek），
    代理生命周期由外部（PreviewPanel 或 PlayerView）管理。
    """

    def __init__(
        self,
        session: Session,
        backend: StorageBackend,
        remote_path: str,
        parent=None,
    ) -> None:
        super().__init__(parent)

        self._session = session
        self._backend = backend
        self._remote_path = remote_path

        # ---- 启动本地解密代理 ----
        self._server, self._port = start_proxy(session, backend)
        # 远端路径做 URL 编码（中文/空格等）
        self._media_url = (
            f"http://127.0.0.1:{self._port}/{quote(remote_path)}"
        )

        # ---- 播放器与输出 ----
        self.player = QMediaPlayer(self)
        self.audio_output = QAudioOutput(self)
        self.player.setAudioOutput(self.audio_output)
        self.video_widget = QVideoWidget(self)
        self.player.setVideoOutput(self.video_widget)

        # ---- 控件：播放/暂停 + 进度滑条 + 时长 ----
        controls = QHBoxLayout()
        self._play_btn = QPushButton("播放", self)
        self._play_btn.clicked.connect(self._on_toggle_play)
        self._slider = QSlider(Qt.Horizontal, self)
        self._slider.setRange(0, 0)
        self._slider.sliderMoved.connect(self._on_seek)
        self._time_label = QLabel("00:00 / 00:00", self)
        controls.addWidget(self._play_btn)
        controls.addWidget(self._slider, stretch=1)
        controls.addWidget(self._time_label)

        # 错误提示行（加载/拉流失败不再无声）
        self._error_label = QLabel("", self)
        self._error_label.setWordWrap(True)
        self._error_label.setStyleSheet("color: #d13438;")
        self._error_label.hide()

        lay = QVBoxLayout(self)
        lay.setContentsMargins(0, 0, 0, 0)
        lay.addWidget(self.video_widget, stretch=1)
        lay.addLayout(controls)
        lay.addWidget(self._error_label)

        # ---- 信号接线：进度/时长/状态/错误 ----
        self.player.positionChanged.connect(self._on_position)
        self.player.durationChanged.connect(self._on_duration)
        self.player.playbackStateChanged.connect(self._on_state)
        self.player.errorOccurred.connect(self._on_error)

        # 指定媒体源后自动播放（选中视频即开播；
        # 加载完成后状态信号自动同步按钮文字）
        self.player.setSource(QUrl(self._media_url))
        self.player.play()

    # ------------------------------------------------------------------
    # 属性
    # ------------------------------------------------------------------

    @property
    def media_url(self) -> str:
        """播放器实际请求的本地代理 URL（测试与调试用）。"""
        return self._media_url

    # ------------------------------------------------------------------
    # 播放控制
    # ------------------------------------------------------------------

    def _on_toggle_play(self) -> None:
        """播放/暂停切换。"""
        if self.player.playbackState() == QMediaPlayer.PlayingState:
            self.player.pause()
        else:
            self.player.play()

    def _on_seek(self, ms: int) -> None:
        """拖动进度条 Seek：播放器向代理发新 Range，代理即时解密。"""
        self.player.setPosition(ms)

    def _on_position(self, ms: int) -> None:
        """播放位置更新：滑条与时间标签跟随。"""
        if not self._slider.isSliderDown():
            self._slider.setValue(ms)
        total = self.player.duration()
        self._time_label.setText(f"{_fmt_ms(ms)} / {_fmt_ms(total)}")

    def _on_duration(self, ms: int) -> None:
        """媒体总时长就绪：设定滑条范围。"""
        self._slider.setRange(0, ms)

    def _on_state(self, state) -> None:
        """播放状态切换：更新按钮文字；开播后清除错误提示。"""
        if state == QMediaPlayer.PlayingState:
            self._play_btn.setText("暂停")
            self._error_label.hide()
        else:
            self._play_btn.setText("播放")

    def _on_error(self, error, error_string: str) -> None:
        """播放器错误（编解码不支持/代理拉流失败等）上屏提示。"""
        text = error_string or str(error)
        self._error_label.setText(f"播放失败：{text}")
        self._error_label.show()

    # ------------------------------------------------------------------
    # 代理管理（供外部调用）
    # ------------------------------------------------------------------

    def stop_proxy(self) -> None:
        """停止代理（释放端口）。"""
        self.player.stop()
        stop_proxy(self._server)


class PlayerView(QDialog):
    """独立播放窗口（向后兼容）。

    内部使用 PlayerWidget，保留原有 QDialog 行为。
    """

    def __init__(
        self,
        session: Session,
        backend: StorageBackend,
        remote_path: str,
        parent=None,
        display_name: str | None = None,
    ) -> None:
        super().__init__(parent)
        # 窗口标题优先用解密后的展示名（避免显示密文叶子名）
        title = display_name or remote_path.rsplit("/", 1)[-1]
        self.setWindowTitle(f"播放 - {title}")
        self.resize(720, 480)

        self._remote_path = remote_path

        # 内嵌 PlayerWidget
        self._widget = PlayerWidget(session, backend, remote_path, self)
        lay = QVBoxLayout(self)
        lay.setContentsMargins(0, 0, 0, 0)
        lay.addWidget(self._widget)

    @property
    def media_url(self) -> str:
        """播放器实际请求的本地代理 URL（测试与调试用）。"""
        return self._widget.media_url

    def closeEvent(self, event) -> None:  # noqa: N802
        """关闭：停止播放并停掉代理（释放端口）。"""
        self._widget.stop_proxy()
        super().closeEvent(event)


def _fmt_ms(ms: int) -> str:
    """毫秒 -> mm:ss 显示。"""
    s = ms // 1000
    return f"{s // 60:02d}:{s % 60:02d}"
