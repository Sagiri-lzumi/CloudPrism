"""支持拖放与右键菜单的目录树视图。

FileTreeView 继承 QTreeView，增加：
  - 拖入上传：从外部文件管理器拖入文件，发射 filesDropped 信号
  - 拖出下载：将选中文件拖出到外部，发射 filesDraggedOut 信号
  - 右键菜单：新建文件夹/上传到此目录/下载/重命名/删除/刷新

拖入仅接受 text/uri-list 格式（文件路径列表）；
拖出使用自定义 MIME 类型，由 AppController 处理实际解密下载。
右键菜单信号参数均为后端原始名拼接的远端路径。
"""

from __future__ import annotations

from PySide6.QtCore import QMimeData, Qt, Signal, QUrl
from PySide6.QtWidgets import QAbstractItemView, QMenu, QTreeView


class FileTreeView(QTreeView):
    """支持拖放操作的目录树视图。"""

    # 外部文件拖入信号：本地文件路径列表
    filesDropped = Signal(list)
    # 文件拖出信号：远端路径列表（由 AppController 执行解密下载）
    filesDraggedOut = Signal(list)

    # ---- 右键菜单信号（参数：远端路径，目录类操作为目录路径） ----
    downloadRequested = Signal(str)    # 下载选中文件
    uploadHereRequested = Signal(str)  # 上传到选中目录（根目录为空串）
    newFolderRequested = Signal(str)   # 在此目录新建文件夹
    renameRequested = Signal(str)      # 重命名选中项
    deleteRequested = Signal(str)      # 删除选中项（弹确认框）
    refreshRequested = Signal()        # 刷新文件树

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
        # 多选（Ctrl/Shift）：批量下载/删除等操作的入口
        self.setSelectionMode(QAbstractItemView.SelectionMode.ExtendedSelection)

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
    # 右键上下文菜单
    # ------------------------------------------------------------------

    def contextMenuEvent(self, event) -> None:
        """右键菜单：根据命中节点类型提供对应操作。"""
        index = self.indexAt(event.pos())
        node = index.internalPointer() if index.isValid() else None

        menu = QMenu(self)

        if node is not None:
            remote = self._remote_path(node)
            if node.is_dir:
                act_new = menu.addAction("新建文件夹…")
                act_new.triggered.connect(lambda: self.newFolderRequested.emit(remote))
                act_upload = menu.addAction("上传到此目录…")
                act_upload.triggered.connect(
                    lambda: self.uploadHereRequested.emit(remote)
                )
                act_dl_dir = menu.addAction("下载整个文件夹…")
                act_dl_dir.triggered.connect(
                    lambda: self.downloadRequested.emit(remote)
                )
            else:
                act_download = menu.addAction("下载…")
                act_download.triggered.connect(
                    lambda: self.downloadRequested.emit(remote)
                )
            menu.addSeparator()
            act_rename = menu.addAction("重命名…")
            act_rename.triggered.connect(lambda: self.renameRequested.emit(remote))
            act_delete = menu.addAction("删除")
            act_delete.triggered.connect(lambda: self.deleteRequested.emit(remote))
        else:
            # 空白处：根目录操作（newFolder/upload 传空串表示根）
            act_new = menu.addAction("新建文件夹…")
            act_new.triggered.connect(lambda: self.newFolderRequested.emit(""))
            act_upload = menu.addAction("上传到根目录…")
            act_upload.triggered.connect(lambda: self.uploadHereRequested.emit(""))

        menu.addSeparator()
        act_refresh = menu.addAction("刷新")
        act_refresh.triggered.connect(self.refreshRequested.emit)

        menu.exec(event.globalPos())

    def _remote_path(self, node) -> str:
        """节点远端路径：委托目录树模型（含子目录密库根前缀）。"""
        model = self.model()
        if model is not None and hasattr(model, "remote_path"):
            return model.remote_path(node)
        # 兜底：沿父链拼接后端原始名（模型未装载阶段）
        parts: list[str] = []
        cur = node
        while cur is not None and cur.name:
            parts.append(cur.name)
            cur = cur.parent
        parts.reverse()
        return "/".join(parts)

    # ------------------------------------------------------------------
    # 拖出：将选中文件拖到外部
    # ------------------------------------------------------------------

    def startDrag(self, supportedActions: Qt.DropActions) -> None:
        """重写拖拽启动：构造自定义 MIME 数据。"""
        indexes = self.selectedIndexes()
        if not indexes:
            return

        # 收集选中节点信息（路径复用 _remote_path；目录也允许拖出）
        remote_paths: list[str] = []
        display_names: list[str] = []
        seen: set[int] = set()
        for idx in indexes:
            if idx.column() != 0:
                continue
            node = idx.internalPointer()
            if node is None or id(node) in seen:
                continue  # 多选时同一节点可能重复出现，去重（目录拖出含子项）
            seen.add(id(node))
            remote_paths.append(self._remote_path(node))
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
