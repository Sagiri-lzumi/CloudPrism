"""支持拖放的目录树视图。

FileTreeView 继承 QTreeView，增加：
  - 拖入上传：从外部文件管理器拖入文件，发射 filesDropped 信号
  - 拖出下载：将选中文件拖出到外部，发射 filesDraggedOut 信号

拖入仅接受 text/uri-list 格式（文件路径列表）；
拖出使用自定义 MIME 类型，由 AppController 处理实际解密下载。
"""

from __future__ import annotations

from PySide6.QtCore import QMimeData, Qt, Signal, QUrl
from PySide6.QtWidgets import QAbstractItemView, QTreeView


class FileTreeView(QTreeView):
    """支持拖放操作的目录树视图。"""

    # 外部文件拖入信号：本地文件路径列表
    filesDropped = Signal(list)
    # 文件拖出信号：远端路径列表（由 AppController 执行解密下载）
    filesDraggedOut = Signal(list)

    # 自定义 MIME 类型（标识远端文件路径）
    MIME_TYPE = "application/x-cloudprism-remote-paths"

    def __init__(self, parent=None) -> None:
        super().__init__(parent)
        self.setAcceptDrops(True)
        # 允许拖入和拖出
        self.setDragEnabled(True)
        self.setDragDropMode(QAbstractItemView.DragDrop)
        self.setDropIndicatorShown(True)
        self.setSelectionBehavior(QAbstractItemView.SelectRows)

    # ------------------------------------------------------------------
    # 拖入：接受外部文件
    # ------------------------------------------------------------------

    def dragEnterEvent(self, event) -> None:
        """接受来自外部文件管理器的拖入。"""
        mime = event.mimeData()
        # 只接受外部文件（含 URL 列表）
        if mime.hasUrls():
            event.acceptProposedAction()
        else:
            super().dragEnterEvent(event)

    def dragMoveEvent(self, event) -> None:
        """拖动过程中持续接受。"""
        mime = event.mimeData()
        if mime.hasUrls():
            event.acceptProposedAction()
        else:
            super().dragMoveEvent(event)

    def dropEvent(self, event) -> None:
        """放下时提取文件路径并发射信号。"""
        mime = event.mimeData()
        if mime.hasUrls():
            # 提取本地文件路径（过滤掉非本地 URL）
            local_files = []
            for url in mime.urls():
                if url.isLocalFile():
                    local_files.append(url.toLocalFile())
            if local_files:
                self.filesDropped.emit(local_files)
                event.acceptProposedAction()
                return
        super().dropEvent(event)

    # ------------------------------------------------------------------
    # 拖出：将选中文件拖到外部
    # ------------------------------------------------------------------

    def startDrag(self, supportedActions: Qt.DropActions) -> None:
        """重写拖拽启动：构造自定义 MIME 数据。"""
        indexes = self.selectedIndexes()
        if not indexes:
            return

        # 收集选中节点信息
        remote_paths: list[str] = []
        display_names: list[str] = []
        for idx in indexes:
            if idx.column() != 0:
                continue
            node = idx.internalPointer()
            if node is None or node.is_dir:
                continue  # 暂不支持拖出目录
            # 构建远端路径
            parts: list[str] = []
            cur = node
            while cur is not None and cur.name:
                parts.append(cur.name)
                cur = cur.parent
            parts.reverse()
            remote_paths.append("/".join(parts))
            display_names.append(node.name)

        if not remote_paths:
            return

        # 构造 MIME 数据
        mime = QMimeData()
        # 自定义类型：存储远端路径（JSON 格式，每行一个路径）
        mime.setData(self.MIME_TYPE, "\n".join(remote_paths).encode("utf-8"))
        # 同时设置 text/uri-list 以便外部识别（使用占位临时路径）
        urls = [QUrl.fromLocalFile(f"C:\\PLACEHOLDER\\{n}") for n in display_names]
        mime.setUrls(urls)

        # 执行拖拽操作
        from PySide6.QtGui import QDrag
        drag = QDrag(self)
        drag.setMimeData(mime)
        action = drag.exec(supportedActions)

        # 拖出完成后发射信号（由 AppController 处理解密下载）
        if action in (Qt.CopyAction, Qt.MoveAction):
            self.filesDraggedOut.emit(remote_paths)
