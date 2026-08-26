"""预览面板：根据文件类型切换预览方式。

支持的文件类型：
  - 视频/音频：内嵌 PlayerWidget 流式播放
  - 图片：QLabel 显示
  - 文本：QTextEdit 显示
  - 其他：文件元信息

文件类型通过扩展名判断，未知类型显示文件信息页。
"""

from __future__ import annotations

import os
from typing import TYPE_CHECKING

from PySide6.QtCore import Qt
from PySide6.QtGui import QPixmap
from PySide6.QtWidgets import (
    QFormLayout,
    QLabel,
    QStackedWidget,
    QTextEdit,
    QVBoxLayout,
    QWidget,
)

from cloudprism.gui.theme import semantic_color

if TYPE_CHECKING:
    from cloudprism.core.session import Session
    from cloudprism.storage.backend import StorageBackend

# ---------------------------------------------------------------------------
# 扩展名分类
# ---------------------------------------------------------------------------

VIDEO_EXTS = {".mp4", ".mkv", ".avi", ".mov", ".webm", ".flv"}
AUDIO_EXTS = {".mp3", ".flac", ".wav", ".aac", ".ogg", ".m4a"}
IMAGE_EXTS = {".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp"}
TEXT_EXTS = {".txt", ".md", ".log", ".json", ".xml", ".csv"}

MEDIA_EXTS = VIDEO_EXTS | AUDIO_EXTS


def classify_file(filename: str) -> str:
    """根据扩展名分类文件类型。

    返回: "media" / "image" / "text" / "info"
    """
    ext = os.path.splitext(filename)[1].lower()
    if ext in MEDIA_EXTS:
        return "media"
    if ext in IMAGE_EXTS:
        return "image"
    if ext in TEXT_EXTS:
        return "text"
    return "info"


# ---------------------------------------------------------------------------
# 预览面板
# ---------------------------------------------------------------------------


class PreviewPanel(QStackedWidget):
    """预览面板：根据文件类型切换预览方式。"""

    # 页面索引
    PAGE_WELCOME = 0
    PAGE_MEDIA = 1
    PAGE_IMAGE = 2
    PAGE_TEXT = 3
    PAGE_INFO = 4

    def __init__(self, parent=None) -> None:
        super().__init__(parent)

        # 欢迎页
        self._welcome = QWidget(self)
        wl = QVBoxLayout(self._welcome)
        welcome_label = QLabel("选择文件以预览", self._welcome)
        welcome_label.setAlignment(Qt.AlignCenter)
        welcome_label.setStyleSheet(f"font-size: 18px; color: {semantic_color('muted')};")
        wl.addWidget(welcome_label)
        self.addWidget(self._welcome)

        # 媒体页（内嵌 PlayerWidget）
        self._media_page = QWidget(self)
        self._media_lay = QVBoxLayout(self._media_page)
        self._media_lay.setContentsMargins(0, 0, 0, 0)
        self._current_player = None  # 当前 PlayerWidget 实例
        self.addWidget(self._media_page)

        # 图片页
        self._image_label = QLabel(self)
        self._image_label.setAlignment(Qt.AlignCenter)
        self._image_label.setScaledContents(True)
        self.addWidget(self._image_label)

        # 文本页
        self._text_edit = QTextEdit(self)
        self._text_edit.setReadOnly(True)
        self._text_edit.setStyleSheet("font-family: Consolas, monospace;")
        self.addWidget(self._text_edit)

        # 文件信息页
        self._info_page = QWidget(self)
        self._info_form = QFormLayout(self._info_page)
        self._info_name = QLabel("-", self._info_page)
        self._info_size = QLabel("-", self._info_page)
        self._info_type = QLabel("-", self._info_page)
        self._info_form.addRow("文件名：", self._info_name)
        self._info_form.addRow("大小：", self._info_size)
        self._info_form.addRow("类型：", self._info_type)
        self.addWidget(self._info_page)

        # 默认显示欢迎页
        self.setCurrentIndex(self.PAGE_WELCOME)

    # ------------------------------------------------------------------
    # 公开方法
    # ------------------------------------------------------------------

    def show_media(
        self,
        session: "Session",
        backend: "StorageBackend",
        remote_path: str,
    ) -> None:
        """在媒体页内嵌播放器。"""
        from cloudprism.gui.player_view import PlayerWidget

        # 清理旧播放器
        self._stop_current_player()

        player = PlayerWidget(session, backend, remote_path, self._media_page)
        self._media_lay.addWidget(player)
        self._current_player = player
        self.setCurrentIndex(self.PAGE_MEDIA)

    def show_image(self, data: bytes) -> None:
        """显示图片预览。"""
        pixmap = QPixmap()
        pixmap.loadFromData(data)
        self._image_label.setPixmap(pixmap)
        self.setCurrentIndex(self.PAGE_IMAGE)

    def show_text(self, text: str) -> None:
        """显示文本预览。"""
        self._text_edit.setPlainText(text)
        self.setCurrentIndex(self.PAGE_TEXT)

    def show_info(
        self,
        filename: str,
        size: str = "-",
        file_type: str = "未知",
    ) -> None:
        """显示文件元信息。"""
        self._info_name.setText(filename)
        self._info_size.setText(size)
        self._info_type.setText(file_type)
        self.setCurrentIndex(self.PAGE_INFO)

    def show_welcome(self) -> None:
        """显示欢迎页（未选中文件时）。"""
        self._stop_current_player()
        self._image_label.clear()
        self._text_edit.clear()
        self.setCurrentIndex(self.PAGE_WELCOME)

    def show_file(
        self,
        session: "Session",
        backend: "StorageBackend",
        remote_path: str,
    ) -> None:
        """根据文件类型自动选择预览方式。

        对于媒体文件直接启动播放器；
        对于图片/文本，从后端下载内容后展示；
        其他类型显示文件信息。
        """
        filename = remote_path.rsplit("/", 1)[-1]
        file_type = classify_file(filename)

        if file_type == "media":
            self.show_media(session, backend, remote_path)
        elif file_type == "image":
            data = backend.download(remote_path)
            if data:
                self.show_image(data)
            else:
                self.show_info(filename, "-", "图片（下载失败）")
        elif file_type == "text":
            data = backend.download(remote_path)
            if data:
                try:
                    text = data.decode("utf-8", errors="replace")
                    self.show_text(text)
                except Exception:
                    self.show_info(filename, _size_str(len(data)), "文本（解码失败）")
            else:
                self.show_info(filename, "-", "文本（下载失败）")
        else:
            # 其他类型：显示文件信息
            size_str = "-"
            try:
                data = backend.download(remote_path)
                if data:
                    size_str = _size_str(len(data))
            except Exception:
                pass
            self.show_info(filename, size_str, _ext_type(filename))

    def _stop_current_player(self) -> None:
        """停止并移除当前播放器组件。"""
        if self._current_player is not None:
            self._current_player.stop_proxy()
            self._current_player.setParent(None)
            self._current_player.deleteLater()
            self._current_player = None


# ---------------------------------------------------------------------------
# 辅助函数
# ---------------------------------------------------------------------------


def _size_str(n: int) -> str:
    """字节数 -> 人类可读大小。"""
    size = float(n)
    for unit in ("B", "KB", "MB", "GB"):
        if size < 1024 or unit == "GB":
            return f"{int(size)} {unit}" if unit == "B" else f"{size:.1f} {unit}"
        size /= 1024
    return f"{n} B"


def _ext_type(filename: str) -> str:
    """扩展名 -> 类型描述。"""
    ext = os.path.splitext(filename)[1].lower()
    return f"文件 ({ext})" if ext else "文件"
