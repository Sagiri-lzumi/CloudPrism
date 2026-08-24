"""GUI 单元测试（pytest-qt）。

验证 DirTreeModel 懒加载与显示名（含文件名解密），以及 MainWindow 骨架。
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6")

from PySide6.QtCore import QModelIndex, Qt

from cloudprism.core.session import Session
from cloudprism.crypto.filename import FilenameCipher
from cloudprism.gui.dir_tree_model import DirTreeModel
from cloudprism.gui.main_window import MainWindow
from cloudprism.storage.local_backend import LocalFolderBackend


# ---------------------------------------------------------------------------
# fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def local_backend(tmp_path):
    """带预置内容的本地后端。"""
    root = tmp_path / "backend"
    root.mkdir()
    (root / "readme.txt.cpenc").write_bytes(b"r" * 100)
    (root / "movie.mp4.cpenc").write_bytes(b"m" * 5000)
    sub = root / "docs"
    sub.mkdir()
    (sub / "note.md.cpenc").write_bytes(b"n" * 42)
    return LocalFolderBackend(root)


# ---------------------------------------------------------------------------
# DirTreeModel
# ---------------------------------------------------------------------------


class TestDirTreeModel:
    """目录树模型。"""

    def test_root_listing(self, qtbot, local_backend):
        """根目录懒加载出条目（目录在前）。"""
        model = DirTreeModel(local_backend)
        root = QModelIndex()             # 无效索引即模型根
        assert model.canFetchMore(root)
        model.fetchMore(root)
        assert model.rowCount() == 3          # docs + readme + movie
        first = model.index(0, 0)
        # 目录排前
        assert model.data(first, Qt.DisplayRole) == "docs"
        # .cpenc 去扩展名显示
        assert model.data(model.index(1, 0), Qt.DisplayRole) == "movie.mp4"
        assert model.data(model.index(2, 0), Qt.DisplayRole) == "readme.txt"

    def test_type_and_size_columns(self, qtbot, local_backend):
        """类型/大小列正确。"""
        model = DirTreeModel(local_backend)
        model.fetchMore(QModelIndex())
        # docs 目录：类型列「目录」，大小列为空
        assert model.data(model.index(0, 1), Qt.DisplayRole) == "目录"
        assert model.data(model.index(0, 2), Qt.DisplayRole) == ""
        # readme 文件
        assert model.data(model.index(2, 1), Qt.DisplayRole) == "文件"
        assert model.data(model.index(2, 2), Qt.DisplayRole) == "100 B"

    def test_subdir_lazy_load(self, qtbot, local_backend):
        """子目录懒加载。"""
        model = DirTreeModel(local_backend)
        model.fetchMore(QModelIndex())
        docs_idx = model.index(0, 0)
        assert model.canFetchMore(docs_idx)
        model.fetchMore(docs_idx)
        assert model.rowCount(docs_idx) == 1
        assert model.data(model.index(0, 0, docs_idx), Qt.DisplayRole) == "note.md"

    def test_user_role_returns_backend_name(self, qtbot, local_backend):
        """UserRole 返回后端原始名（含 .cpenc）。"""
        model = DirTreeModel(local_backend)
        model.fetchMore(QModelIndex())
        assert model.data(model.index(2, 0), Qt.UserRole) == "readme.txt.cpenc"

    def test_name_decryptor_shows_original(self, qtbot, tmp_path):
        """文件名加密开启时展示解密后的原始名。"""
        # 准备：用 FilenameCipher 加密文件名存入后端
        session = Session("pw")
        key = session.derive_key(b"\x00" * 16)
        stored_name = FilenameCipher.encrypt("机密文档.pdf", key) + ".cpenc"

        root = tmp_path / "enc_backend"
        root.mkdir()
        (root / stored_name).write_bytes(b"x" * 10)
        backend = LocalFolderBackend(root)

        model = DirTreeModel(
            backend,
            name_decryptor=lambda s: FilenameCipher.decrypt(s, key),
        )
        model.fetchMore(QModelIndex())
        # 展示名应为解密后的原始名
        assert model.data(model.index(0, 0), Qt.DisplayRole) == "机密文档.pdf"

    def test_reload_resets(self, qtbot, local_backend):
        """reload() 清空后重新懒加载。"""
        model = DirTreeModel(local_backend)
        model.fetchMore(QModelIndex())
        assert model.rowCount() == 3
        model.reload()
        assert model.rowCount() == 0          # 重置后未加载
        model.fetchMore(QModelIndex())
        assert model.rowCount() == 3


# ---------------------------------------------------------------------------
# MainWindow
# ---------------------------------------------------------------------------


class TestMainWindow:
    """主窗口骨架（IDE 风格）。"""

    def test_construct_menus(self, qtbot):
        """无后端构造：菜单齐备（Mi库/文件/播放）。"""
        win = MainWindow()
        qtbot.addWidget(win)
        titles = [m.text() for m in win.menuBar().actions()]
        assert any("Mi库" in t for t in titles)
        assert any("文件" in t for t in titles)
        assert any("播放" in t for t in titles)

    def test_construct_status_unconnected(self, qtbot):
        """无后端构造：状态栏显示未连接。"""
        win = MainWindow()
        qtbot.addWidget(win)
        assert win._status_conn.text() == "未连接"

    def test_set_connected(self, qtbot):
        """set_connected 更新连接状态。"""
        win = MainWindow()
        qtbot.addWidget(win)
        win.set_connected(True)
        assert win._status_conn.text() == "已连接"
        win.set_connected(False)
        assert win._status_conn.text() == "未连接"

    def test_signals_emitted(self, qtbot):
        """占位信号可发射。"""
        win = MainWindow()
        qtbot.addWidget(win)
        with qtbot.waitSignal(win.initRequested, timeout=1000):
            win.initRequested.emit()

    def test_activity_bar_and_side_panel(self, qtbot):
        """活动栏与侧面板存在且可切换。"""
        win = MainWindow()
        qtbot.addWidget(win)
        # 活动栏存在
        assert win.activity_bar is not None
        # 侧面板存在且可切换
        win.side_panel.show_page("transfers")
        assert win.side_panel.currentIndex() == win.side_panel.PAGE_TRANSFERS
        win.side_panel.show_page("settings")
        assert win.side_panel.currentIndex() == win.side_panel.PAGE_SETTINGS

    def test_preview_panel_welcome(self, qtbot):
        """预览面板初始显示欢迎页。"""
        win = MainWindow()
        qtbot.addWidget(win)
        assert win.preview_panel.currentIndex() == win.preview_panel.PAGE_WELCOME

    def test_update_perf_stats(self, qtbot):
        """状态栏性能指标更新。"""
        win = MainWindow()
        qtbot.addWidget(win)
        win.update_perf_stats(1.5, 1024 * 1024, 25.0)
        assert "1.5" in win._status_speed.text()
        assert "1.0" in win._status_cache.text()
        assert "25" in win._status_cpu.text()
