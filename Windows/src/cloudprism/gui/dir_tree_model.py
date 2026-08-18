"""目录树模型。

基于 StorageBackend 的 QAbstractItemModel，支持懒加载（canFetchMore /
fetchMore）：目录节点首次展开时才调用后端 list_dir。

显示名规则：
  - 名称以 .cpenc 结尾 -> 去掉扩展名后展示原始名；
  - 若提供了 name_decryptor（文件名加密开启时），先解密再展示；
  - 解密失败时回退显示原名（可能是未知密钥或未加密名）。

注意：当前骨架 fetchMore 内同步调用后端；本地后端足够快，远程后端
（WebDAV）的异步加载在传输 worker 步骤引入后优化。
"""

from __future__ import annotations

from typing import Callable

from PySide6.QtCore import QAbstractItemModel, QModelIndex, Qt

from cloudprism import constants
from cloudprism.storage.backend import RemoteEntry, StorageBackend


class DirNode:
    """目录树节点（纯 Python，不依赖 Qt）。"""

    __slots__ = ("name", "is_dir", "size", "parent", "children", "loaded")

    def __init__(
        self,
        name: str,
        is_dir: bool = False,
        size: int = 0,
        parent: "DirNode | None" = None,
    ) -> None:
        self.name = name              # 后端显示名（可能已加密）
        self.is_dir = is_dir
        self.size = size
        self.parent = parent
        self.children: list[DirNode] | None = None   # None = 未加载
        self.loaded = False

    def row(self) -> int:
        """本节点在父节点中的行号。"""
        if self.parent is None:
            return 0
        return self.parent.children.index(self)  # type: ignore[union-attr]


class DirTreeModel(QAbstractItemModel):
    """存储后端目录树模型。

    列：0 名称 / 1 类型 / 2 大小。
    """

    # 列标题
    COLUMNS = ["名称", "类型", "大小"]

    def __init__(
        self,
        backend: StorageBackend,
        name_decryptor: Callable[[str], str] | None = None,
        parent=None,
    ) -> None:
        super().__init__(parent)
        self.backend = backend
        # 文件名解密回调（文件名加密开启时由上层注入）
        self._decryptor = name_decryptor
        # 根节点（不可见），对应后端根目录
        self._root = DirNode("", is_dir=True)

    # ------------------------------------------------------------------
    # 只读模型接口
    # ------------------------------------------------------------------

    def index(
        self, row: int, column: int, parent: QModelIndex = QModelIndex()
    ) -> QModelIndex:
        if not self.hasIndex(row, column, parent):
            return QModelIndex()
        parent_node = parent.internalPointer() if parent.isValid() else self._root
        child = parent_node.children[row]  # type: ignore[index]
        return self.createIndex(row, column, child)

    def parent(self, index: QModelIndex) -> QModelIndex:  # noqa: A003
        if not index.isValid():
            return QModelIndex()
        node = index.internalPointer()
        parent_node = node.parent
        if parent_node is None or parent_node is self._root:
            return QModelIndex()
        return self.createIndex(parent_node.row(), 0, parent_node)

    def rowCount(self, parent: QModelIndex = QModelIndex()) -> int:  # noqa: N802
        node = parent.internalPointer() if parent.isValid() else self._root
        if node is None or node.children is None:
            return 0
        return len(node.children)

    def columnCount(self, parent: QModelIndex = QModelIndex()) -> int:  # noqa: N802
        return len(self.COLUMNS)

    def data(self, index: QModelIndex, role: int = Qt.DisplayRole):  # noqa: A003
        if not index.isValid():
            return None
        node: DirNode = index.internalPointer()
        if role == Qt.DisplayRole:
            col = index.column()
            if col == 0:
                return self.display_name(node.name)
            if col == 1:
                return "目录" if node.is_dir else "文件"
            if col == 2:
                return "" if node.is_dir else _human_size(node.size)
        elif role == Qt.UserRole:
            # 原始后端名（供下载/播放等操作使用）
            return node.name
        return None

    def headerData(self, section, orientation, role=Qt.DisplayRole):  # noqa: N803
        if (
            role == Qt.DisplayRole
            and orientation == Qt.Horizontal
            and 0 <= section < len(self.COLUMNS)
        ):
            return self.COLUMNS[section]
        return None

    # ------------------------------------------------------------------
    # 懒加载
    # ------------------------------------------------------------------

    def hasChildren(self, parent: QModelIndex = QModelIndex()) -> bool:  # noqa: N802
        node = parent.internalPointer() if parent.isValid() else self._root
        # 目录可能有子项（未加载前先允许展开）
        return bool(node and node.is_dir)

    def canFetchMore(self, parent: QModelIndex) -> bool:  # noqa: N802
        node = parent.internalPointer() if parent.isValid() else self._root
        return bool(node and node.is_dir and not node.loaded)

    def fetchMore(self, parent: QModelIndex) -> None:  # noqa: N802
        """加载目录子项（首次展开时调用）。"""
        node = parent.internalPointer() if parent.isValid() else self._root
        if node is None or node.loaded:
            return
        entries = self.backend.list_dir(self._remote_path(node))
        children = [
            DirNode(e.name, e.is_dir, e.size, parent=node) for e in entries
        ]
        # 排序：目录在前，名称升序
        children.sort(key=lambda n: (not n.is_dir, n.name))
        self.beginInsertRows(parent, 0, max(len(children) - 1, 0))
        node.children = children
        node.loaded = True
        self.endInsertRows()

    # ------------------------------------------------------------------
    # 辅助方法
    # ------------------------------------------------------------------

    def _remote_path(self, node: DirNode) -> str:
        """节点对应的后端相对路径。"""
        parts: list[str] = []
        cur = node
        while cur is not None and cur is not self._root:
            parts.append(cur.name)
            cur = cur.parent
        return "/".join(reversed(parts))

    def display_name(self, backend_name: str) -> str:
        """后端名 -> 展示名：去 .cpenc，必要时解密。"""
        name = backend_name
        if name.endswith(constants.FILE_EXTENSION):
            base = name[: -len(constants.FILE_EXTENSION)]
            if self._decryptor is not None:
                try:
                    base = self._decryptor(base)
                except Exception:
                    # 解密失败（密钥不符或未加密名），回退原名
                    pass
            return base
        return name

    def node_for_index(self, index: QModelIndex) -> DirNode | None:
        """QModelIndex -> DirNode（供上层操作取路径）。"""
        return index.internalPointer() if index.isValid() else None

    def reload(self) -> None:
        """整树重载（刷新）。"""
        self.beginResetModel()
        self._root = DirNode("", is_dir=True)
        self.endResetModel()


def _human_size(n: int) -> str:
    """字节数 -> 人类可读大小。"""
    size = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if size < 1024 or unit == "TB":
            return f"{int(size)} {unit}" if unit == "B" else f"{size:.1f} {unit}"
        size /= 1024
    return f"{n} B"
