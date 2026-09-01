"""流式预览播放器测试（pytest-qt）。

媒体解码依赖系统编解码器，自动化测试聚焦可验证的部分：
  - 播放器消费的代理 URL 正确（返回 206 与解密明文，支持 Range）
  - Seek 用的 URL 与播放 URL 一致（代理层换算已由代理测试覆盖）
  - 关闭窗口停止代理（端口释放）
  - AppController 组装（向导产出 -> 窗口装载）
"""

from __future__ import annotations

import urllib.error
import urllib.request

import pytest

pytest.importorskip("PySide6")
pytest.importorskip("PySide6.QtMultimedia")

from cloudprism.app import AppController
from cloudprism.core.encryptor import Encryptor
from cloudprism.core.session import Session
from cloudprism.crypto.filename import FilenameCipher
from cloudprism.gui.main_window import MainWindow
from cloudprism.gui.player_view import PlayerView, PlayerWidget
from cloudprism.storage.local_backend import LocalFolderBackend


MASTER_PW = "player_test_pw"


@pytest.fixture
def session():
    return Session(MASTER_PW)


@pytest.fixture
def backend_with_media(tmp_path, session):
    """带一个加密媒体文件的本地后端。"""
    root = tmp_path / "backend"
    root.mkdir()
    backend = LocalFolderBackend(root)
    plaintext = bytes(range(256)) * 40          # 10240 字节
    src = tmp_path / "media.bin"
    src.write_bytes(plaintext)
    for _ in Encryptor(session, backend, chunk=256).encrypt_and_upload(
        str(src), "videos/sample.cpenc"
    ):
        pass
    return backend, plaintext


def _http_get(url: str, headers: dict | None = None):
    """GET，返回 (status, data, headers)。"""
    req = urllib.request.Request(url, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, r.read(), dict(r.headers)
    except urllib.error.HTTPError as e:
        return e.code, e.read(), dict(e.headers)


class TestPlayerView:
    """流式播放窗口（向后兼容）。"""

    def test_media_url_is_local_proxy(self, qtbot, session, backend_with_media):
        """媒体 URL 指向本地代理且路径已编码。"""
        backend, _ = backend_with_media
        view = PlayerView(session, backend, "videos/sample.cpenc")
        qtbot.addWidget(view)
        url = view.media_url
        assert url.startswith("http://127.0.0.1:")
        assert "videos/sample.cpenc" in url

    def test_media_url_serves_decrypted_plaintext(
        self, qtbot, session, backend_with_media
    ):
        """播放器消费的 URL 返回 206 且内容为解密明文（含 Range）。"""
        backend, plaintext = backend_with_media
        view = PlayerView(session, backend, "videos/sample.cpenc")
        qtbot.addWidget(view)

        # 整文件
        status, data, _ = _http_get(view.media_url)
        assert status == 206
        assert data == plaintext

        # Range（Seek 的底层机制）
        status, data, headers = _http_get(
            view.media_url, {"Range": "bytes=1000-1999"}
        )
        assert status == 206
        assert data == plaintext[1000:2000]
        assert "bytes 1000-1999" in headers.get("Content-Range", "")

    def test_url_encoded_remote_path(self, qtbot, session, tmp_path):
        """含中文/空格的远端路径正确编码并可播放。"""
        root = tmp_path / "b2"
        root.mkdir()
        backend = LocalFolderBackend(root)
        plaintext = b"0123456789" * 10
        src = tmp_path / "p.bin"
        src.write_bytes(plaintext)
        for _ in Encryptor(session, backend, chunk=16).encrypt_and_upload(
            str(src), "视频 目录/我的 电影.cpenc"
        ):
            pass
        view = PlayerView(session, backend, "视频 目录/我的 电影.cpenc")
        qtbot.addWidget(view)
        status, data, _ = _http_get(view.media_url)
        assert status == 206
        assert data == plaintext

    def test_close_stops_proxy(self, qtbot, session, backend_with_media):
        """关闭窗口后代理端口释放（连接被拒）。"""
        backend, _ = backend_with_media
        view = PlayerView(session, backend, "videos/sample.cpenc")
        qtbot.addWidget(view)
        url = view.media_url
        status, _, _ = _http_get(url)
        assert status == 206

        view.close()
        # 代理已停：连接应失败
        try:
            _http_get(url)
            reachable = True
        except (urllib.error.URLError, ConnectionError, OSError):
            reachable = False
        assert reachable is False

    def test_two_views_isolated(self, qtbot, session, backend_with_media):
        """两个播放窗口并存：各自代理互不干扰（独立端口与状态）。"""
        backend, plaintext = backend_with_media
        v1 = PlayerView(session, backend, "videos/sample.cpenc")
        qtbot.addWidget(v1)
        v2 = PlayerView(session, backend, "videos/sample.cpenc")
        qtbot.addWidget(v2)
        assert v1.media_url != v2.media_url      # 不同端口
        # 两个 URL 都能正确服务（各自代理用各自后端状态）
        for v in (v1, v2):
            status, data, _ = _http_get(v.media_url, {"Range": "bytes=0-99"})
            assert status == 206
            assert data == plaintext[:100]


class TestPlayerWidget:
    """可嵌入播放器组件。"""

    def test_widget_media_url(self, qtbot, session, backend_with_media):
        """PlayerWidget 的媒体 URL 指向本地代理。"""
        backend, _ = backend_with_media
        widget = PlayerWidget(session, backend, "videos/sample.cpenc")
        qtbot.addWidget(widget)
        url = widget.media_url
        assert url.startswith("http://127.0.0.1:")
        assert "videos/sample.cpenc" in url
        # 清理
        widget.stop_proxy()

    def test_widget_serves_plaintext(self, qtbot, session, backend_with_media):
        """PlayerWidget 的代理返回解密明文。"""
        backend, plaintext = backend_with_media
        widget = PlayerWidget(session, backend, "videos/sample.cpenc")
        qtbot.addWidget(widget)
        status, data, _ = _http_get(widget.media_url)
        assert status == 206
        assert data == plaintext
        widget.stop_proxy()


class TestAppController:
    """应用组装。"""

    def test_apply_setup_loads_window(self, qtbot, tmp_path, monkeypatch):
        """向导产出 -> 窗口装载后端与目录树。"""
        import cloudprism.app as app_mod
        # 拦截模态提示框（避免阻塞测试）
        monkeypatch.setattr(
            app_mod.QMessageBox, "information", lambda *a, **k: None
        )

        from cloudprism.core.vault_manager import VaultManager
        from cloudprism.gui.init_wizard import InitWizard

        root = tmp_path / "vault"
        root.mkdir()
        vm = VaultManager(LocalFolderBackend(root))
        meta = vm.create_vault("pw", filename_enc=False)

        win = MainWindow()
        qtbot.addWidget(win)
        ctrl = AppController(win)
        assert ctrl._require_vault() is False     # 未连接（提示被拦截）

        # 程序化构造向导产出（不走模态 exec）
        wizard = InitWizard()
        qtbot.addWidget(wizard)
        wizard.backend = LocalFolderBackend(root)
        wizard.metadata = meta
        wizard.session = Session("pw")

        ctrl._apply_setup(wizard)
        assert ctrl.session is not None
        assert ctrl._require_vault() is True      # 已连接
        # 状态栏已连接
        assert win._status_conn.text() == "已连接"

    def test_apply_setup_with_filename_enc(self, qtbot, fetch_wait, tmp_path):
        """文件名加密开启时目录树注入解密器（显示原始名）。"""
        from cloudprism.core.vault_manager import VaultManager
        from cloudprism.gui.init_wizard import InitWizard

        root = tmp_path / "vault2"
        root.mkdir()
        backend = LocalFolderBackend(root)
        vm = VaultManager(backend)
        meta = vm.create_vault("pw2", filename_enc=True)

        # 上传一个文件名加密的文件
        session = Session("pw2")
        key = session.derive_key(meta.salt)
        stored = FilenameCipher.encrypt("秘密.txt", key) + ".cpenc"
        (root / stored).write_bytes(b"data")

        win = MainWindow()
        qtbot.addWidget(win)
        ctrl = AppController(win)
        wizard = InitWizard()
        qtbot.addWidget(wizard)
        wizard.backend = backend
        wizard.metadata = meta
        wizard.session = session
        ctrl._apply_setup(wizard)

        # 目录树显示解密后的原始名
        model = win.side_panel.files_page.tree.model()
        from PySide6.QtCore import QModelIndex, Qt
        fetch_wait(model)
        assert model.rowCount() == 1
        assert model.data(model.index(0, 0), Qt.DisplayRole) == "秘密.txt"

    def test_expand_remote_dir_tasks_decrypts_names(self, qtbot, tmp_path, monkeypatch):
        """文件夹递归下载展开：本地目录树与文件名均还原文名。"""
        import os

        import cloudprism.app as app_mod
        monkeypatch.setattr(
            app_mod.QMessageBox, "information", lambda *a, **k: None
        )

        from cloudprism.core.vault_manager import VaultManager
        from cloudprism.gui.init_wizard import InitWizard

        root = tmp_path / "vault_enc"
        root.mkdir()
        backend = LocalFolderBackend(root)
        vm = VaultManager(backend)
        meta = vm.create_vault("pw", filename_enc=True)

        session = Session("pw")
        key = session.derive_key(meta.salt)
        # 后端构造加密目录结构：项目资料 / 秘密.txt（均为密文名）
        enc_dir = FilenameCipher.encrypt("项目资料", key)
        enc_file = FilenameCipher.encrypt("秘密.txt", key) + ".cpenc"
        (root / enc_dir).mkdir()
        (root / enc_dir / enc_file).write_bytes(b"data")

        win = MainWindow()
        qtbot.addWidget(win)
        ctrl = AppController(win)
        wizard = InitWizard()
        qtbot.addWidget(wizard)
        wizard.backend = backend
        wizard.metadata = meta
        wizard.session = session
        ctrl._apply_setup(wizard)

        save_root = tmp_path / "downloads"
        save_root.mkdir()
        tasks = ctrl._expand_remote_dir_tasks(enc_dir, str(save_root))

        assert len(tasks) == 1
        t = tasks[0]
        assert t.direction == "download"
        assert t.remote_path == f"{enc_dir}/{enc_file}"
        # 本地文件名/目录名均为明文（非 Base32 密文）
        assert t.local_path.endswith(os.path.join("项目资料", "秘密.txt"))
        # 本地目录树按明文结构预先创建
        assert (save_root / "项目资料").is_dir()

    def test_selection_video_enters_media_page(
        self, qtbot, fetch_wait, tmp_path, monkeypatch
    ):
        """文件名加密库选中视频：按展示名分类直达播放界面。

        后端叶子名为 Base32 密文 + .cpenc，旧分类逻辑会误判为信息页。
        """
        import cloudprism.app as app_mod
        monkeypatch.setattr(
            app_mod.QMessageBox, "information", lambda *a, **k: None
        )

        from PySide6.QtCore import QItemSelectionModel
        from cloudprism.core.vault_manager import VaultManager
        from cloudprism.gui.init_wizard import InitWizard

        root = tmp_path / "vault_media"
        root.mkdir()
        backend = LocalFolderBackend(root)
        vm = VaultManager(backend)
        meta = vm.create_vault("pw3", filename_enc=True)

        session = Session("pw3")
        key = session.derive_key(meta.salt)
        stored = FilenameCipher.encrypt("电影.mp4", key) + ".cpenc"
        (root / stored).write_bytes(b"data")

        win = MainWindow()
        qtbot.addWidget(win)
        ctrl = AppController(win)
        wizard = InitWizard()
        qtbot.addWidget(wizard)
        wizard.backend = backend
        wizard.metadata = meta
        wizard.session = session
        ctrl._apply_setup(wizard)

        model = win.file_tree.model()
        fetch_wait(model)
        assert model.rowCount() == 1
        # 选中文件节点 -> 预览分发应落入媒体页（播放器）
        win.file_tree.selectionModel().select(
            model.index(0, 0),
            QItemSelectionModel.ClearAndSelect | QItemSelectionModel.Rows,
        )
        assert win.preview_panel.currentIndex() == win.preview_panel.PAGE_MEDIA
        # 清理播放器与代理，避免端口残留影响后续用例
        win.preview_panel._stop_current_player()
