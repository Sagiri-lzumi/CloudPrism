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
        """无后端构造：菜单齐备（Mi库/文件/播放）。

        FluentWindow 无真实菜单栏，经 menuBar() 兼容层查询菜单结构。
        """
        win = MainWindow()
        qtbot.addWidget(win)
        titles = [m.title() for m in win.menuBar().actions()]
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
        # 侧面板存在且可切换（含密库页）
        win.side_panel.show_page("transfers")
        assert win.side_panel.currentIndex() == win.side_panel.PAGE_TRANSFERS
        win.side_panel.show_page("vaults")
        assert win.side_panel.currentIndex() == win.side_panel.PAGE_VAULTS
        win.side_panel.show_page("settings")
        assert win.side_panel.currentIndex() == win.side_panel.PAGE_SETTINGS

    def test_dynamic_layout_file_mode(self, qtbot):
        """文件页显示侧面板 + 预览区。"""
        win = MainWindow()
        qtbot.addWidget(win)
        # 默认文件页：预览面板可见
        assert win.preview_panel.isVisibleTo(win._content_widget)

    def test_dynamic_layout_fullwidth_mode(self, qtbot):
        """传输/密库/设置页隐藏预览区。"""
        win = MainWindow()
        qtbot.addWidget(win)
        win.activity_bar.currentChanged.emit("transfers")
        assert not win.preview_panel.isVisibleTo(win._content_widget)
        win.activity_bar.currentChanged.emit("vaults")
        assert not win.preview_panel.isVisibleTo(win._content_widget)
        win.activity_bar.currentChanged.emit("settings")
        assert not win.preview_panel.isVisibleTo(win._content_widget)

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

    def test_vault_info_page_exists(self, qtbot):
        """密库信息页存在且可访问。"""
        win = MainWindow()
        qtbot.addWidget(win)
        assert win.vault_info_page is not None
        # 默认显示引导页（未连接）——检查内部状态而非 isVisible
        assert win.vault_info_page._guide_widget.isVisibleTo(win.vault_info_page)
        assert not win.vault_info_page._info_widget.isVisibleTo(win.vault_info_page)

    def test_vault_info_page_connected(self, qtbot):
        """密库信息页切换到已连接状态。"""
        win = MainWindow()
        qtbot.addWidget(win)
        win.vault_info_page.show_connected()
        assert not win.vault_info_page._guide_widget.isVisibleTo(win.vault_info_page)
        assert win.vault_info_page._info_widget.isVisibleTo(win.vault_info_page)

    def test_settings_page_has_new_sections(self, qtbot):
        """设置页包含外观/传输/安全设置。"""
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        # 外观设置
        assert sp._theme_combo is not None
        assert sp._font_size_combo is not None
        # 传输设置
        assert sp._chunk_size_combo is not None
        assert sp._concurrent_spin is not None
        # 安全设置
        assert sp._auto_lock_combo is not None

    def test_settings_page_vault_name_card(self, qtbot):
        """设置页连接信息含密库名称卡，可经 update_connection_info 更新。"""
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        assert sp._vault_name_card is not None
        assert sp._vault_rename_btn is not None
        sp.update_connection_info(
            "本地文件夹", "D:/vault", False, vault_name="我的库"
        )
        assert sp._vault_name_label.text() == "我的库"
        # 默认参数兼容：不传名称时回退占位符，既有签名不破坏
        sp.update_connection_info("本地文件夹", "D:/vault", False)
        assert sp._vault_name_label.text() == "-"

    def test_settings_page_expand_font_unified(self, qtbot):
        """二级展开区控件字体统一为 14px，与一级卡片观感对齐。"""
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        # 展开区内嵌控件（均为展开卡子控件）经字体继承链应为 14px
        for w in (sp._cache_limit_spin, sp._concurrent_spin, sp._max_cores_spin,
                  sp._cache_path_edit, sp._baidu_appid_edit):
            assert w.font().pixelSize() == 14, f"{type(w).__name__} 字体未统一"

    def test_vault_info_page_rename_entry(self, qtbot):
        """密库信息页库名称行含编辑按钮，update_info 更新名称显示。"""
        win = MainWindow()
        qtbot.addWidget(win)
        vip = win.vault_info_page
        assert vip._rename_btn is not None
        vip.update_info(vault_name="自定义名")
        assert vip._vault_name_label.text() == "自定义名"
