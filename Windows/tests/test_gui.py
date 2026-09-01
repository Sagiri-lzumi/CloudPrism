"""GUI 单元测试（pytest-qt）。

验证 DirTreeModel 懒加载与显示名（含文件名解密），以及 MainWindow 骨架。
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6")

from PySide6.QtCore import QModelIndex, Qt

from cloudprism import __version__
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

    def test_root_listing(self, qtbot, fetch_wait, local_backend):
        """根目录懒加载出条目（目录在前）。"""
        model = DirTreeModel(local_backend)
        root = QModelIndex()             # 无效索引即模型根
        assert model.canFetchMore(root)
        fetch_wait(model, root)
        assert model.rowCount() == 3          # docs + readme + movie
        first = model.index(0, 0)
        # 目录排前
        assert model.data(first, Qt.DisplayRole) == "docs"
        # .cpenc 去扩展名显示
        assert model.data(model.index(1, 0), Qt.DisplayRole) == "movie.mp4"
        assert model.data(model.index(2, 0), Qt.DisplayRole) == "readme.txt"

    def test_type_and_size_columns(self, qtbot, fetch_wait, local_backend):
        """类型/大小列正确。"""
        model = DirTreeModel(local_backend)
        fetch_wait(model)
        # docs 目录：类型列「目录」，大小列为空
        assert model.data(model.index(0, 1), Qt.DisplayRole) == "目录"
        assert model.data(model.index(0, 2), Qt.DisplayRole) == ""
        # readme 文件
        assert model.data(model.index(2, 1), Qt.DisplayRole) == "文件"
        assert model.data(model.index(2, 2), Qt.DisplayRole) == "100 B"

    def test_subdir_lazy_load(self, qtbot, fetch_wait, local_backend):
        """子目录懒加载。"""
        model = DirTreeModel(local_backend)
        fetch_wait(model)
        docs_idx = model.index(0, 0)
        assert model.canFetchMore(docs_idx)
        fetch_wait(model, docs_idx)
        assert model.rowCount(docs_idx) == 1
        assert model.data(model.index(0, 0, docs_idx), Qt.DisplayRole) == "note.md"

    def test_user_role_returns_backend_name(self, qtbot, fetch_wait, local_backend):
        """UserRole 返回后端原始名（含 .cpenc）。"""
        model = DirTreeModel(local_backend)
        fetch_wait(model)
        assert model.data(model.index(2, 0), Qt.UserRole) == "readme.txt.cpenc"

    def test_name_decryptor_shows_original(self, qtbot, fetch_wait, tmp_path):
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
        fetch_wait(model)
        # 展示名应为解密后的原始名
        assert model.data(model.index(0, 0), Qt.DisplayRole) == "机密文档.pdf"

    def test_dir_name_decrypted(self, qtbot, fetch_wait, tmp_path):
        """文件夹后端名为无扩展名密文，同样应解密展示。"""
        session = Session("pw")
        key = session.derive_key(b"\x01" * 16)
        enc_dir = FilenameCipher.encrypt("项目资料", key)

        root = tmp_path / "enc_dirs"
        (root / enc_dir).mkdir(parents=True)
        backend = LocalFolderBackend(root)

        model = DirTreeModel(
            backend,
            name_decryptor=lambda s: FilenameCipher.decrypt(s, key),
        )
        fetch_wait(model)
        assert model.rowCount() == 1
        assert model.data(model.index(0, 0), Qt.DisplayRole) == "项目资料"
        # UserRole 仍是后端密文名（下载/展开等操作依赖）
        assert model.data(model.index(0, 0), Qt.UserRole) == enc_dir

    def test_dir_name_decrypt_fail_fallback(self, qtbot, fetch_wait, tmp_path):
        """目录名解密失败（未加密名/旧库）时回退原名。"""
        root = tmp_path / "plain_dirs"
        (root / "普通目录").mkdir(parents=True)
        backend = LocalFolderBackend(root)

        def bad_decryptor(s: str) -> str:
            raise ValueError("标签校验失败")

        model = DirTreeModel(backend, name_decryptor=bad_decryptor)
        fetch_wait(model)
        assert model.data(model.index(0, 0), Qt.DisplayRole) == "普通目录"

    def test_sorted_by_display_name(self, qtbot, fetch_wait, tmp_path):
        """开启文件名加密时按解密后的展示名排序（而非密文名）。"""
        session = Session("pw")
        key = session.derive_key(b"\x02" * 16)
        root = tmp_path / "enc_sort"
        root.mkdir()
        # 两个加密文件：明文 b 先于 a 的密文顺序无关紧要，
        # 断言按明文 a/b 升序即可
        for plain in ("b.txt", "a.txt"):
            stored = FilenameCipher.encrypt(plain, key) + ".cpenc"
            (root / stored).write_bytes(b"x")
        backend = LocalFolderBackend(root)

        model = DirTreeModel(
            backend,
            name_decryptor=lambda s: FilenameCipher.decrypt(s, key),
        )
        fetch_wait(model)
        names = [
            model.data(model.index(r, 0), Qt.DisplayRole) for r in range(2)
        ]
        assert names == ["a.txt", "b.txt"]

    def test_reload_resets(self, qtbot, fetch_wait, local_backend):
        """reload() 清空后重新懒加载。"""
        model = DirTreeModel(local_backend)
        fetch_wait(model)
        assert model.rowCount() == 3
        model.reload()
        assert model.rowCount() == 0          # 重置后未加载
        fetch_wait(model)
        assert model.rowCount() == 3


# ---------------------------------------------------------------------------
# MainWindow
# ---------------------------------------------------------------------------


class TestMainWindow:
    """主窗口骨架（IDE 风格）。"""

    def test_construct_menus(self, qtbot):
        """无后端构造：菜单齐备（密库/文件/播放）。

        FluentWindow 无真实菜单栏，经 menuBar() 兼容层查询菜单结构。
        """
        win = MainWindow()
        qtbot.addWidget(win)
        titles = [m.title() for m in win.menuBar().actions()]
        assert any("密库" in t for t in titles)
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

    def test_settings_page_version_label(self, qtbot):
        """设置页底部展示当前版本号（与包版本一致）。"""
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        assert sp._version_label is not None
        assert __version__ in sp._version_label.text()

    def test_settings_page_check_update_card(self, qtbot):
        """设置页「关于」分组含检查更新卡，内容展示当前版本。"""
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        assert sp._check_update_card is not None
        assert __version__ in sp._check_update_card.contentLabel.text()
        assert sp._check_update_card.button.text() == "检查更新"
        assert sp._check_update_card.isEnabled()

    def test_check_update_latest_dispatch(self, qtbot):
        """注入同版本假 fetch：结果为 latest，派发后按钮恢复可用。"""
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        sp._fetch_release = lambda: {
            "tag": __version__, "name": "", "url": "",
        }
        with qtbot.waitSignal(sp.updateChecked, timeout=3000) as blocker:
            sp._on_check_update()
        assert blocker.args[0]["status"] == "latest"
        # 手动派发（与排队槽幂等）：按钮状态与文案恢复
        sp._on_update_checked(blocker.args[0])
        assert sp._check_update_card.isEnabled()
        assert sp._check_update_card.button.text() == "检查更新"

    def test_check_update_error_dispatch(self, qtbot):
        """注入抛异常假 fetch：结果为 error，不崩溃且按钮恢复。"""
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page

        def _boom():
            raise ConnectionError("网络请求失败")

        sp._fetch_release = _boom
        with qtbot.waitSignal(sp.updateChecked, timeout=3000) as blocker:
            sp._on_check_update()
        assert blocker.args[0]["status"] == "error"
        sp._on_update_checked(blocker.args[0])
        assert sp._check_update_card.isEnabled()

    def test_vault_info_page_rename_entry(self, qtbot):
        """密库信息页库名称行含编辑按钮，update_info 更新名称显示。"""
        win = MainWindow()
        qtbot.addWidget(win)
        vip = win.vault_info_page
        assert vip._rename_btn is not None
        vip.update_info(vault_name="自定义名")
        assert vip._vault_name_label.text() == "自定义名"

    def test_vault_info_partial_update_keeps_basic_fields(self, qtbot):
        """部分更新语义：统计回调只写云端占用时基础信息保持原值。

        回归防护：旧实现默认值全量覆盖会把库名/后端/路径冲成横杠。
        """
        win = MainWindow()
        qtbot.addWidget(win)
        vip = win.vault_info_page
        vip.update_info(
            vault_name="工作库", backend_type="本地文件夹",
            backend_path="D:/v", filename_enc=True,
            connect_time="2026-08-27 10:00:00 · 已连接 0秒",
        )
        # 模拟后台统计回调回填（仅两个字段）
        vip.update_info(cloud_size="12.3 MB", file_count="42")
        assert vip._vault_name_label.text() == "工作库"
        assert vip._backend_type_label.text() == "本地文件夹"
        assert vip._backend_path_label.text() == "D:/v"
        assert vip._filename_enc_label.text() == "开"
        assert vip._cloud_size_label.text() == "12.3 MB"
        assert vip._file_count_label.text() == "42"
        # 未传 connect_time 不覆盖旧值（不再回退当前时刻）
        assert vip._connect_time_label.text().startswith("2026-08-27 10:00:00")

    def test_update_connect_time_only_touches_time_row(self, qtbot):
        """每秒级轻量刷新：只改连接时间行，不触碰其他字段。"""
        win = MainWindow()
        qtbot.addWidget(win)
        vip = win.vault_info_page
        vip.update_info(vault_name="工作库", cloud_size="1 MB")
        vip.update_connect_time("2026-08-27 10:00:05 · 已连接 5秒")
        assert vip._connect_time_label.text() == "2026-08-27 10:00:05 · 已连接 5秒"
        assert vip._vault_name_label.text() == "工作库"
        assert vip._cloud_size_label.text() == "1 MB"


def test_format_connect_time_formats():
    """连接时间格式化：秒/分秒/时分秒与未连接回退。"""
    from datetime import datetime, timedelta

    from cloudprism.app import _format_connect_time

    base = datetime(2026, 8, 27, 10, 0, 0)
    assert _format_connect_time(None) == "-"
    assert _format_connect_time(base, base).endswith("已连接 0秒")
    t = _format_connect_time(base, base + timedelta(seconds=90))
    assert t.startswith("2026-08-27 10:00:00")
    assert "已连接 1分30秒" in t
    t2 = _format_connect_time(base, base + timedelta(hours=1, minutes=2, seconds=3))
    assert "已连接 1小时2分3秒" in t2


# ---------------------------------------------------------------------------
# 三期新特性：并发设置 / 网格视图 / 传输页 / 多密库 / 同步卡 / 续传横幅
# ---------------------------------------------------------------------------


class TestConcurrencySetting:
    """并发传输设置启用并发射信号。"""

    def test_concurrency_signal(self, qtbot):
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        assert sp._concurrent_spin.isEnabled()
        with qtbot.waitSignal(sp.concurrencyChanged, timeout=1000) as blocker:
            sp._concurrent_spin.setValue(3)
        assert blocker.args[0] == 3


class TestGridView:
    """文件页列表/网格视图切换。"""

    def test_grid_mode_toggle(self, qtbot):
        win = MainWindow()
        qtbot.addWidget(win)
        fp = win._files_page
        assert not fp.is_grid_mode()
        with qtbot.waitSignal(fp.viewModeChanged, timeout=1000) as blocker:
            fp.set_grid_mode(True)
        assert blocker.args[0] == "grid"
        assert fp.is_grid_mode()
        # 重复切换同一视图不重复发信号（直接调方法验证提前返回）
        fp.set_grid_mode(True)
        with qtbot.waitSignal(fp.viewModeChanged, timeout=1000) as blocker:
            fp.set_grid_mode(False)
        assert blocker.args[0] == "list"
        assert not fp.is_grid_mode()


class TestTransfersPageNew:
    """传输页：任务卡重试与续传横幅。"""

    def test_retry_signal(self, qtbot):
        """失败任务卡片点重试：发射携带任务对象的信号。"""
        win = MainWindow()
        qtbot.addWidget(win)
        tp = win.transfers_page
        item = tp.add_task("a.txt", "upload")
        sentinel = object()  # 以任意对象充当任务载体（控制器经 UserRole 挂载）
        item.setData(Qt.ItemDataRole.UserRole, sentinel)
        card = tp._card(item)
        tp.finish_task(item, success=False)
        assert not card._retry_btn.isHidden()
        with qtbot.waitSignal(tp.retryRequested, timeout=1000) as blocker:
            card._retry_btn.click()
        assert blocker.args[0] is sentinel

    def test_resume_banner(self, qtbot):
        """续传横幅：显示/点击回调/隐藏（未显示窗口用 isHidden 判定）。"""
        win = MainWindow()
        qtbot.addWidget(win)
        tp = win.transfers_page
        assert tp._resume_banner.isHidden()
        calls = []
        tp.show_resume_banner(3, lambda: calls.append(1))
        assert not tp._resume_banner.isHidden()
        assert "3 项" in tp._resume_banner.text()
        tp._resume_banner.click()
        assert calls == [1]
        tp.hide_resume_banner()
        assert tp._resume_banner.isHidden()


class TestOtherVaults:
    """密库信息页：本后端的其他密库列表。"""

    def test_other_vault_cards(self, qtbot):
        from cloudprism.gui.vault_info_page import VaultInfoPage

        page = VaultInfoPage()
        qtbot.addWidget(page)
        # 默认隐藏（无其他密库）
        assert page._other_container.isHidden()
        page.set_other_vaults([{"path": "backup", "vault_id": None}])
        assert not page._other_container.isHidden()
        cards = page.other_vault_cards()
        assert len(cards) == 1
        with qtbot.waitSignal(
            page.connectOtherVaultRequested, timeout=1000
        ) as blocker:
            cards[0].connect_btn.click()
        assert blocker.args[0] == "backup"
        # 清空后回退隐藏
        page.set_other_vaults([])
        assert page._other_container.isHidden()


class TestSyncCard:
    """设置页文件夹同步卡。"""

    def test_sync_dir_and_signal(self, qtbot):
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        assert sp.sync_dir() == ""
        sp.set_sync_dir("D:/docs")
        assert sp.sync_dir() == "D:/docs"
        with qtbot.waitSignal(sp.syncRequested, timeout=1000) as blocker:
            sp._sync_btn.click()
        assert blocker.args[0] == "D:/docs"

    def test_sync_without_dir_no_signal(self, qtbot):
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        fired = []
        sp.syncRequested.connect(lambda p: fired.append(p))
        sp._sync_btn.click()
        assert fired == []

    def test_recovery_state_text(self, qtbot):
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        sp.update_recovery_state(True)
        assert "已启用" in sp._recovery_status.text()
        sp.update_recovery_state(False)
        assert "尚无" in sp._recovery_status.text()

    def test_new_expand_cards_font_unified(self, qtbot):
        """新增展开卡（恢复码/同步）内嵌控件同样过字体统一检查。"""
        win = MainWindow()
        qtbot.addWidget(win)
        sp = win.settings_page
        for w in (sp._sync_dir_edit,):
            assert w.font().pixelSize() == 14, f"{type(w).__name__} 字体未统一"

