"""CloudPrism Windows 客户端入口（组合根）。

组装各模块：
  MainWindow（IDE 风格三栏界面）
    + InitWizard（初始化/连接，产出 session/backend/metadata）
    + TransferWorker（上传/下载，进度对话框）
    + PreviewPanel（内嵌预览与播放）
    + PerfMonitor（状态栏性能指标）
"""

from __future__ import annotations

import sys
import time

from PySide6.QtCore import QEvent, QModelIndex, QObject, Signal, Qt, QTimer
from PySide6.QtWidgets import QApplication, QFileDialog, QInputDialog, QMessageBox

# Fluent 提示组件（模态确认用 MessageBox，非阻断提示用 InfoBar）
from qfluentwidgets import InfoBar, InfoBarPosition
from qfluentwidgets import MessageBox as FluentMessageBox

from cloudprism.core.session import Session
from cloudprism.core.settings_store import SettingsStore
from cloudprism.core.backend_factory import describe_backend
from cloudprism.crypto.filename import FilenameCipher
from cloudprism.gui.dir_tree_model import DirTreeModel
from cloudprism.gui.init_wizard import InitWizard
from cloudprism.gui.main_window import MainWindow
from cloudprism.gui.perf_monitor import PerfMonitor
from cloudprism.gui.quick_connect import QuickConnectDialog
from cloudprism.gui.theme import apply_theme, current_mode
from cloudprism.gui.transfer_worker import TransferWorker, start_transfer_bg
from cloudprism.storage.backend import StorageBackend


def _human_size(n: int) -> str:
    """字节数 -> 人类可读大小。"""
    size = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if size < 1024 or unit == "TB":
            return f"{int(size)} {unit}" if unit == "B" else f"{size:.1f} {unit}"
        size /= 1024
    return f"{n} B"


class AppController(QObject):
    """应用控制器：连接窗口信号与各功能模块。"""

    def __init__(self, window: MainWindow) -> None:
        super().__init__(window)  # QObject 父级：随窗口销毁，支持事件过滤
        self.window = window
        # 向导产出（连接Mi库后填充）
        self.session: Session | None = None
        self.backend: StorageBackend | None = None
        self.metadata = None

        # 目录树模型
        self._tree_model: DirTreeModel | None = None

        # 传输队列状态（串行调度：同时只有一个任务在跑）
        self._transfer_queue: list[tuple[str, str, str, str]] = []
        self._transfer_total = 0
        self._transfer_current = 0
        self._transfer_busy = False
        self._active_threads: list = []
        self._current_queue_item = None  # 传输队列页当前条目（TransfersPage 联动）

        # 接入设置持久化存储（须在自动锁定定时器同步前完成载入）
        self._store = SettingsStore()
        window.settings_page.attach_store(self._store)

        # 自动锁定：每 60 秒检查一次空闲时长；事件过滤器刷新最后活跃时间
        self._last_active = time.monotonic()
        self._lock_timer = QTimer(self.window)
        self._lock_timer.setInterval(60_000)
        self._lock_timer.timeout.connect(self._check_auto_lock)
        self._setup_auto_lock()

        # 密库统计后台线程引用（防回收）
        self._stats_thread = None
        self._stats_seq = 0  # 统计序号：仅最新一次的结果回填界面

        # 性能监控
        self._perf = PerfMonitor(parent=window)
        self._perf.statsUpdated.connect(window.update_perf_stats)

        # 信号接线
        window.initRequested.connect(self.show_init_wizard)
        window.uploadRequested.connect(self.upload_file)
        window.downloadRequested.connect(self.download_file)
        window.playRequested.connect(self.play_file)
        window.refreshRequested.connect(self._refresh_tree)
        window.lockRequested.connect(self._lock_vault)

        # 文件树选中 -> 预览
        window.file_tree.selectionModel().selectionChanged.connect(
            self._on_tree_selection
        ) if window.file_tree.selectionModel() else None

        # 设置页更新连接信息 / 重连请求 / 自动锁定变更 / 主题与字体
        window.settings_page.cacheSettingsChanged.connect(self._on_cache_settings_changed)
        window.settings_page.reconnectRequested.connect(self.show_init_wizard)
        window.settings_page.themeChanged.connect(self._on_theme_changed)
        window.settings_page.fontSizeChanged.connect(self._on_font_size_changed)
        window.settings_page.autoLockChanged.connect(self._sync_lock_timer)

        # 密库信息页信号（含最近密库快速连接/移除）
        window.vault_info_page.connectRequested.connect(self.show_init_wizard)
        window.vault_info_page.quickConnectRequested.connect(self._on_quick_connect)
        window.vault_info_page.removeVaultRequested.connect(self._on_remove_vault)
        window.vault_info_page.refreshRequested.connect(self._refresh_vault_info)
        window.vault_info_page.lockRequested.connect(self._lock_vault)

        # 文件树拖放信号
        window.file_tree.filesDropped.connect(self._on_files_dropped)
        window.file_tree.filesDraggedOut.connect(self._on_files_dragged_out)

        # 文件树右键菜单信号
        window.file_tree.downloadRequested.connect(self._ctx_download)
        window.file_tree.uploadHereRequested.connect(self.upload_file)
        window.file_tree.newFolderRequested.connect(self._new_folder)
        window.file_tree.renameRequested.connect(self._rename_remote)
        window.file_tree.deleteRequested.connect(self._delete_remote)
        window.file_tree.refreshRequested.connect(self._refresh_tree)

        # 启动性能监控
        self._perf.start()

        # 未连接引导页回填最近密库记录（重启后仍可见）
        window.vault_info_page.set_recent_vaults(self._store.recent_vaults())

    # ------------------------------------------------------------------
    # 自动锁定
    # ------------------------------------------------------------------

    def _setup_auto_lock(self) -> None:
        """安装事件过滤器记录用户活动，并按设置启动/停止检查定时器。"""
        self.window.installEventFilter(self)
        self._sync_lock_timer()

    def _sync_lock_timer(self) -> None:
        """根据设置页的自动锁定时长启停定时器。"""
        if self.window.settings_page.auto_lock_minutes > 0:
            self._lock_timer.start()
        else:
            self._lock_timer.stop()

    def eventFilter(self, obj, event) -> bool:  # noqa: N802
        """用户输入（鼠标/键盘）时刷新最后活跃时间。"""
        etype = event.type()
        if etype in (
            QEvent.Type.MouseButtonPress, QEvent.Type.MouseMove, QEvent.Type.Wheel,
            QEvent.Type.KeyPress, QEvent.Type.KeyRelease,
        ):
            self._last_active = time.monotonic()
        return False  # 不拦截事件，仅记录

    def _check_auto_lock(self) -> None:
        """定时检查：空闲超时且已连接密库时自动锁定。"""
        minutes = self.window.settings_page.auto_lock_minutes
        if minutes <= 0 or self.session is None:
            return
        if time.monotonic() - self._last_active >= minutes * 60:
            self._lock_vault()

    # ------------------------------------------------------------------
    # 主题与字体（设置页接线）
    # ------------------------------------------------------------------

    def _on_theme_changed(self, theme: str) -> None:
        """主题切换：实时应用 Fluent 明/暗主题（system 自动解析）。"""
        app = QApplication.instance()
        if app is not None:
            apply_theme(app, theme)

    def _on_font_size_changed(self, size: int) -> None:
        """字体大小切换：应用到整个应用。"""
        app = QApplication.instance()
        if app is not None:
            font = app.font()
            font.setPointSize(size)
            app.setFont(font)
            # 字号经 apply_theme 统一应用（携带字号避免沿用旧值丢失）
            apply_theme(app, current_mode(), font_size=size)

    # ------------------------------------------------------------------
    # 初始化 / 连接
    # ------------------------------------------------------------------

    def show_init_wizard(self) -> None:
        """弹出初始化向导；成功后装载后端与目录树。"""
        wizard = InitWizard(self.window, store=self._store)
        wizard.finishedSetup.connect(lambda: self._apply_setup(wizard))
        if wizard.exec() == InitWizard.Accepted and wizard.metadata is not None:
            self._apply_setup(wizard)

    def _apply_setup(self, wizard: InitWizard) -> None:
        """应用向导产出并记录最近密库。"""
        self._apply_connection(wizard.backend, wizard.metadata, wizard.session)
        self._remember_current_vault()

    def _apply_connection(self, backend, metadata, session) -> None:
        """装载一次成功连接：后端、目录树、状态栏与各信息页。

        向导与快速连接共用本方法。
        """
        self.session = session
        self.backend = backend
        self.metadata = metadata

        # 文件名加密开启时注入解密器（目录树显示原始名）
        name_decryptor = None
        if self.metadata is not None and self.metadata.filename_enc:
            salt = self.metadata.salt

            def name_decryptor(enc_name: str) -> str:  # noqa: F811
                key = self.session.derive_key(salt)
                return FilenameCipher.decrypt(enc_name, key)

        # 装载目录树模型
        self._tree_model = DirTreeModel(self.backend, name_decryptor=name_decryptor)
        self.window.side_panel.files_page.set_model(self._tree_model)

        # 连接文件树选中信号
        sel_model = self.window.file_tree.selectionModel()
        if sel_model:
            sel_model.selectionChanged.connect(self._on_tree_selection)

        # 更新状态栏
        self.window.set_connected(True)

        # 更新设置页连接信息（友好显示名而非类名）
        backend_label, backend_path, _ = describe_backend(self.backend)
        self.window.settings_page.update_connection_info(
            backend_type=backend_label,
            backend_path=backend_path,
            filename_enc=self.metadata.filename_enc,
        )
        self.window.settings_page._refresh_cache_usage()

        # 更新密库信息页
        self._update_vault_info_page()

    def _remember_current_vault(self) -> None:
        """将当前成功连接写入最近密库记录并刷新引导页列表。"""
        if self.backend is None:
            return
        label, path, btype = describe_backend(self.backend)
        vault_id_hex = self.metadata.vault_id.hex() if self.metadata else ""
        record = {
            "backend_type": btype,
            "label": label,
            "path": path,
            "vault_name": vault_id_hex[:8] if vault_id_hex else "-",
        }
        if btype == "webdav":
            # 仅存账号，密码绝不落盘
            auth = getattr(self.backend, "auth", ("", ""))
            record["webdav_user"] = auth[0] if auth else ""
        self._store.remember_vault(record)
        self.window.vault_info_page.set_recent_vaults(self._store.recent_vaults())

    def _on_quick_connect(self, record: dict) -> None:
        """最近密库一键重连：仅输主密码（WebDAV 另输服务器密码）。"""
        dlg = QuickConnectDialog(record, parent=self.window)
        if dlg.exec() == QuickConnectDialog.Accepted and dlg.metadata is not None:
            self._apply_connection(dlg.backend, dlg.metadata, dlg.session)
            self._remember_current_vault()

    def _on_remove_vault(self, record: dict) -> None:
        """移除最近密库记录（仅删记录，不影响云端数据）。"""
        box = FluentMessageBox(
            "移除记录",
            f"确定移除该密库记录？（不影响云端数据）\n{record.get('path', '')}",
            self.window,
        )
        if box.exec():
            self._store.forget_vault(record.get("key", ""))
            self.window.vault_info_page.set_recent_vaults(
                self._store.recent_vaults()
            )

    # ------------------------------------------------------------------
    # 文件树选中 -> 预览
    # ------------------------------------------------------------------

    def _on_tree_selection(self, selected, deselected) -> None:
        """文件树选中变更：更新预览面板。"""
        indexes = selected.indexes()
        if not indexes or self.session is None or self.backend is None:
            self.window.preview_panel.show_welcome()
            return

        index = indexes[0]
        node = index.internalPointer()
        if node is None or node.is_dir:
            self.window.preview_panel.show_welcome()
            return

        remote_path = self._build_remote_path(node)
        self.window.preview_panel.show_file(self.session, self.backend, remote_path)

    def _build_remote_path(self, node) -> str:
        """从树节点构建远端路径。"""
        parts = []
        current = node
        while current is not None and current.name:
            parts.append(current.name)
            current = current.parent
        parts.reverse()
        return "/".join(parts)

    # ------------------------------------------------------------------
    # 上传 / 下载
    # ------------------------------------------------------------------

    def upload_file(self, remote_dir: str = "") -> None:
        """选择本地文件（支持多选）-> 加密上传到选中目录（后台）。"""
        if not self._require_vault():
            return
        paths, _ = QFileDialog.getOpenFileNames(
            self.window, "选择要加密上传的文件（可多选）"
        )
        if not paths:
            return
        self._upload_paths(paths, remote_dir)

    def upload_files(self, local_paths: list[str], remote_dir: str = "") -> None:
        """批量上传（供拖放调用），支持文件与文件夹。"""
        if not self._require_vault() or not local_paths:
            return
        import os
        valid_paths = [p for p in local_paths if os.path.exists(p)]
        if valid_paths:
            self._upload_paths(valid_paths, remote_dir)

    def download_file(self, remote_path: str = "") -> None:
        """选择保存位置 -> 下载解密选中文件（后台）。"""
        if self._require_vault() and remote_path:
            # 去掉 .cpenc 作为默认保存名
            default = remote_path.rsplit("/", 1)[-1]
            if default.endswith(".cpenc"):
                default = default[: -len(".cpenc")]
            path, _ = QFileDialog.getSaveFileName(
                self.window, "保存解密文件", default
            )
            if path:
                self._start_bg_transfer(
                    path, remote_path,
                    file_name=default, direction="download",
                )

    # ------------------------------------------------------------------
    # 播放
    # ------------------------------------------------------------------

    def play_file(self, remote_path: str = "") -> None:
        """在预览面板中播放选中的加密媒体文件。"""
        if self._require_vault() and remote_path:
            self.window.preview_panel.show_media(
                self.session, self.backend, remote_path
            )

    # ------------------------------------------------------------------
    # 缓存设置
    # ------------------------------------------------------------------

    def _on_cache_settings_changed(self, limit_mb: int, cache_path: str) -> None:
        """缓存设置变更：更新性能监控的缓存路径。"""
        self._perf.set_cache_path(cache_path)

    # ------------------------------------------------------------------
    # 拖放操作
    # ------------------------------------------------------------------

    def _on_files_dropped(self, local_paths: list[str]) -> None:
        """文件拖入：批量加密上传到当前选中目录。"""
        remote_dir = self._selected_remote_dir()
        self.upload_files(local_paths, remote_dir)

    def _on_files_dragged_out(self, remote_paths: list[str]) -> None:
        """文件拖出：解密下载到用户选择的目标目录。"""
        if not self._require_vault() or not remote_paths:
            return
        import os
        # 让用户选择保存目录
        save_dir = QFileDialog.getExistingDirectory(
            self.window, "选择保存目录"
        )
        if not save_dir:
            return
        for rp in remote_paths:
            # 解密文件名作为本地保存名
            fname = rp.rsplit("/", 1)[-1]
            if self._is_filename_enc() and fname.endswith(".cpenc"):
                enc_base = fname[: -len(".cpenc")]
                try:
                    salt = self.metadata.salt
                    key = self.session.derive_key(salt)
                    fname = FilenameCipher.decrypt(enc_base, key)
                except Exception:
                    fname = fname[: -len(".cpenc")] if fname.endswith(".cpenc") else fname
            elif fname.endswith(".cpenc"):
                fname = fname[: -len(".cpenc")]
            local_path = os.path.join(save_dir, fname)
            self._start_bg_transfer(
                local_path, rp,
                file_name=fname, direction="download",
            )

    def _selected_remote_dir(self) -> str:
        """获取文件树当前选中目录的远端路径。"""
        indexes = self.window.file_tree.selectedIndexes()
        if not indexes:
            return ""
        node = indexes[0].internalPointer()
        if node is None:
            return ""
        if not node.is_dir:
            node = node.parent
        if node is None:
            return ""
        parts: list[str] = []
        cur = node
        while cur is not None and cur.name:
            parts.append(cur.name)
            cur = cur.parent
        parts.reverse()
        return "/".join(parts)

    # ------------------------------------------------------------------
    # 辅助
    # ------------------------------------------------------------------

    def _require_vault(self) -> bool:
        """未连接Mi库时提示并返回 False。"""
        if self.session is None or self.backend is None:
            # 保留 QMessageBox.information：测试（test_player_view）经 app_mod.QMessageBox patch 拦截
            QMessageBox.information(
                self.window, "提示", "请先通过「Mi库 - 初始化/连接」连接云盘"
            )
            return False
        return True

    def _is_filename_enc(self) -> bool:
        """当前密库是否开启文件名加密。"""
        return self.metadata is not None and self.metadata.filename_enc

    def _encrypt_filename(self, name: str) -> str:
        """用文件名加密密钥加密文件名，返回 Base32 编码串。"""
        salt = self.metadata.salt
        key = self.session.derive_key(salt)
        return FilenameCipher.encrypt(name, key)

    def _refresh_tree(self) -> None:
        """刷新文件树。"""
        if self._tree_model is not None:
            self._tree_model.reload()

    # ------------------------------------------------------------------
    # 右键菜单操作（新建/重命名/删除/下载）
    # ------------------------------------------------------------------

    def _resolve_remote_path(self, display_path: str) -> str:
        """把树上传来的路径解析为后端真实路径。

        文件名加密开启时树节点展示的是解密名，需还原为密文段；
        优先原样尝试（未加密库直接命中），否则对文件段/目录段逐级加密。
        """
        if self.backend is None:
            return display_path
        if self._exists_safe(display_path):
            return display_path
        if not self._is_filename_enc():
            return display_path

        segments = display_path.split("/")

        def _enc_seg(seg: str) -> str:
            try:
                return self._encrypt_filename(seg)
            except Exception:
                return seg

        # 文件路径：最后一段为 <原名>.cpenc 形式，加密后加扩展名；目录名直接加密
        last = segments[-1]
        if last.endswith(".cpenc"):
            base = last[: -len(".cpenc")]
            cand_file = _enc_seg(base) + ".cpenc"
        else:
            cand_file = _enc_seg(last)
        enc_dirs = [_enc_seg(s) for s in segments[:-1]]
        cand = "/".join(enc_dirs + [cand_file]) if enc_dirs else cand_file
        if self._exists_safe(cand):
            return cand

        # 回退：目录段未加密（旧库/混合场景）
        cand2 = "/".join(segments[:-1] + [cand_file])
        return cand2

    def _exists_safe(self, path: str) -> bool:
        """安全判断远端路径是否存在（异常视为不存在）。"""
        try:
            return self.backend.exists(path)
        except Exception:
            return False

    def _new_folder(self, display_dir: str) -> None:
        """右键新建文件夹。"""
        if not self._require_vault():
            return
        name, ok = QInputDialog.getText(
            self.window, "新建文件夹", "文件夹名称："
        )
        if not ok or not name.strip():
            return
        name = name.strip()
        if "/" in name:
            InfoBar.warning(
                title="提示", content="文件夹名称不能包含 /",
                parent=self.window, position=InfoBarPosition.TOP_RIGHT, duration=3000,
            )
            return
        enc_name = self._encrypt_filename(name) if self._is_filename_enc() else name
        base = self._resolve_remote_path(display_dir) if display_dir else ""
        remote = f"{base}/{enc_name}" if base else enc_name
        try:
            self.backend.mkdir(remote)
        except Exception as e:
            InfoBar.warning(
                title="新建失败", content=str(e),
                parent=self.window, position=InfoBarPosition.TOP_RIGHT, duration=4000,
            )
            return
        self._refresh_tree()

    def _rename_remote(self, display_path: str) -> None:
        """右键重命名（文件/目录）。"""
        if not self._require_vault():
            return
        old_display = display_path.rsplit("/", 1)[-1]
        if old_display.endswith(".cpenc"):
            old_display = old_display[: -len(".cpenc")]
        new_name, ok = QInputDialog.getText(
            self.window, "重命名", "新名称：", text=old_display
        )
        if not ok or not new_name.strip():
            return
        new_name = new_name.strip()
        if "/" in new_name:
            InfoBar.warning(
                title="提示", content="名称不能包含 /",
                parent=self.window, position=InfoBarPosition.TOP_RIGHT, duration=3000,
            )
            return

        old_remote = self._resolve_remote_path(display_path)
        is_file = old_remote.endswith(".cpenc")
        enc_new = (
            self._encrypt_filename(new_name) if self._is_filename_enc() else new_name
        )
        new_leaf = enc_new + ".cpenc" if is_file else enc_new
        parent = old_remote.rsplit("/", 1)[0] if "/" in old_remote else ""
        new_remote = f"{parent}/{new_leaf}" if parent else new_leaf
        try:
            self.backend.rename(old_remote, new_remote)
        except Exception as e:
            InfoBar.warning(
                title="重命名失败", content=str(e),
                parent=self.window, position=InfoBarPosition.TOP_RIGHT, duration=4000,
            )
            return
        self._refresh_tree()

    def _delete_remote(self, display_path: str) -> None:
        """右键删除（弹确认框）。"""
        if not self._require_vault():
            return
        name = display_path.rsplit("/", 1)[-1]
        box = FluentMessageBox(
            "确认删除",
            f"确定删除「{name}」吗？\n此操作不可撤销。",
            self.window,
        )
        if not box.exec():
            return
        remote = self._resolve_remote_path(display_path)
        try:
            self.backend.delete(remote)
        except Exception as e:
            InfoBar.warning(
                title="删除失败", content=str(e),
                parent=self.window, position=InfoBarPosition.TOP_RIGHT, duration=4000,
            )
            return
        self._refresh_tree()

    def _ctx_download(self, display_path: str) -> None:
        """右键下载单个文件。"""
        self.download_file(self._resolve_remote_path(display_path))

    # ------------------------------------------------------------------
    # 后台传输管理
    # ------------------------------------------------------------------

    def _upload_paths(self, paths: list[str], remote_dir: str) -> None:
        """批量入队本地路径（文件或文件夹）后台加密上传。"""
        import os
        tasks: list[tuple[str, str, str, str]] = []
        for path in paths:
            if os.path.isfile(path):
                tasks.append(self._make_upload_task(path, remote_dir))
            elif os.path.isdir(path):
                tasks.extend(self._expand_dir_tasks(path, remote_dir))
        if not tasks:
            return
        self._transfer_total += len(tasks)
        self._transfer_queue.extend(tasks)
        if not self._transfer_busy:
            self._run_next_transfer()

    def _make_upload_task(self, local_path: str, remote_dir: str) -> tuple:
        """构造单文件上传任务：(本地路径, 远端路径, 显示名, 方向)。"""
        import os
        name = os.path.basename(local_path)
        enc_name = self._encrypt_filename(name) if self._is_filename_enc() else name
        remote = f"{remote_dir}/{enc_name}.cpenc" if remote_dir else f"{enc_name}.cpenc"
        return (local_path, remote, name, "upload")

    def _expand_dir_tasks(self, dir_path: str, remote_dir: str) -> list[tuple]:
        """展开目录为上传任务：逐级创建远端目录后逐文件入队。

        目录名按文件名加密设置处理；已存在的目录容错跳过。
        """
        import os
        tasks: list[tuple] = []
        base_name = os.path.basename(dir_path.rstrip(os.sep))
        root_enc = self._encrypt_filename(base_name) if self._is_filename_enc() else base_name
        root_remote = f"{remote_dir}/{root_enc}" if remote_dir else root_enc

        def _mkdir_ignore(remote: str) -> None:
            try:
                self.backend.mkdir(remote)
            except Exception:
                pass  # 已存在等错误不影响后续上传

        _mkdir_ignore(root_remote)
        for dirpath, _dirnames, filenames in os.walk(dir_path):
            rel = os.path.relpath(dirpath, dir_path).replace(os.sep, "/")
            if rel == ".":
                target_dir = root_remote
            else:
                # 子目录各段按需加密后逐级创建
                segs = rel.split("/")
                enc_segs = [
                    self._encrypt_filename(s) if self._is_filename_enc() else s
                    for s in segs
                ]
                target_dir = root_remote + "/" + "/".join(enc_segs)
                _mkdir_ignore(target_dir)
            for fname in filenames:
                tasks.append(
                    self._make_upload_task(os.path.join(dirpath, fname), target_dir)
                )
        return tasks

    def _start_bg_transfer(
        self,
        local_path: str,
        remote_path: str,
        file_name: str = "",
        direction: str = "upload",
    ) -> None:
        """入队并启动单个后台传输任务（追加式，不打断现有队列）。"""
        self._transfer_total += 1
        self._transfer_queue.append((local_path, remote_path, file_name, direction))
        if not self._transfer_busy:
            self._run_next_transfer()

    def _run_next_transfer(self) -> None:
        """执行队列中的下一个传输任务（串行）。"""
        if not self._transfer_queue:
            self._transfer_busy = False
            return
        self._transfer_busy = True
        local_path, remote_path, file_name, direction = self._transfer_queue.pop(0)
        self._transfer_current += 1

        chunk = self.window.settings_page.chunk_size
        max_workers = self.window.settings_page.max_cores
        kind = (
            TransferWorker.KIND_UPLOAD
            if direction == "upload"
            else TransferWorker.KIND_DOWNLOAD
        )
        worker, thread = start_transfer_bg(
            kind,
            self.session, self.backend,
            local_path, remote_path,
            chunk=chunk, max_workers=max_workers,
            parent=self.window,
        )
        # 保持线程引用防止被回收
        self._active_threads.append(thread)
        thread.finished.connect(
            lambda t=thread: self._active_threads.remove(t)
            if t in self._active_threads else None
        )

        # 传输队列页 + 底部进度条联动（item 闭包捕获，避免被后续任务覆盖）
        transfers_page = self.window.transfers_page
        item = transfers_page.add_task(file_name, direction)
        self.window.transfer_progress.show_task(
            file_name, direction=direction,
            current=self._transfer_current, total=self._transfer_total,
            cancel_fn=worker.cancel,
        )

        def _on_progress(p: float) -> None:
            self.window.transfer_progress.update_progress(p)
            transfers_page.update_task(item, p)

        def _finish(success: bool) -> None:
            transfers_page.finish_task(item, success)
            self.window.transfer_progress.task_finished(success)
            if not self._transfer_queue:
                # 本批全部结束：重置计数并刷新文件树（上传后新文件可见）
                self._transfer_total = 0
                self._transfer_current = 0
                self._transfer_busy = False
                self._refresh_tree()
            else:
                self._run_next_transfer()

        worker.progress.connect(_on_progress)
        worker.finished.connect(lambda: _finish(True))
        worker.cancelled.connect(lambda: _finish(False))
        worker.error.connect(lambda msg: _finish(False))

    def _refresh_vault_info(self) -> None:
        """刷新密库信息页。"""
        self._update_vault_info_page()

    def _lock_vault(self) -> None:
        """锁定密库：清除会话与后端引用，重置界面。"""
        if self.session:
            self.session.close()  # 清零主密码与派生密钥
        self.session = None
        self.backend = None
        self.metadata = None
        self._tree_model = None
        # 重置传输队列状态（进行中的任务会因会话失效而报错结束）
        self._transfer_queue.clear()
        self._transfer_total = 0
        self._transfer_current = 0
        self._transfer_busy = False
        self.window.set_connected(False)
        self.window.vault_info_page.show_disconnected()
        self.window.preview_panel.show_welcome()
        self.window.transfer_progress.hide_bar()
        # 清空文件树模型
        self.window.side_panel.files_page.tree.setModel(None)
        # 引导页回填最近密库记录，便于快速重连
        self.window.vault_info_page.set_recent_vaults(self._store.recent_vaults())

    def _update_vault_info_page(self) -> None:
        """更新密库信息页显示（云端占用/文件数在后台线程递归统计）。"""
        if self.session is None or self.backend is None:
            self.window.vault_info_page.show_disconnected()
            return

        self.window.vault_info_page.show_connected()

        # 计算库名称（vault_id 截短）
        vault_id_hex = self.metadata.vault_id.hex() if self.metadata else "-"
        vault_name = vault_id_hex[:8] + "..." if len(vault_id_hex) > 8 else vault_id_hex

        # 后端信息（友好显示名而非类名）
        backend_type, backend_path, _ = describe_backend(self.backend)

        # 本地缓存大小（本地磁盘遍历，很快）
        cache_path = self.window.settings_page.cache_path
        cache_size = "-"
        import os
        if cache_path and os.path.isdir(cache_path):
            total = 0
            for dirpath, _dirs, files in os.walk(cache_path):
                for f in files:
                    try:
                        total += os.path.getsize(os.path.join(dirpath, f))
                    except OSError:
                        pass
            cache_size = _human_size(total)

        self.window.vault_info_page.update_info(
            vault_name=vault_name,
            backend_type=backend_type,
            backend_path=backend_path,
            filename_enc=self.metadata.filename_enc if self.metadata else False,
            cloud_size="计算中…",
            cache_size=cache_size,
            file_count="计算中…",
        )

        # 云端占用递归统计放到后台线程，避免远程后端阻塞 UI
        self._start_stats_worker()

    def _start_stats_worker(self) -> None:
        """后台线程递归遍历后端，统计总大小与文件数。

        统计在工作线程执行，结果经 Qt 信号排队投递回主线程回填界面。
        """
        from PySide6.QtCore import QObject

        backend = self.backend
        if backend is None:
            return
        self._stats_seq += 1
        seq = self._stats_seq

        class _StatsEmitter(QObject):
            done = Signal(int, int)  # (总字节数, 文件数)

        emitter = _StatsEmitter()

        def _on_done(total_size: int, count: int) -> None:
            # 仅最新一次统计的结果才回填（避免旧结果覆盖）
            if seq == self._stats_seq and self.session is not None:
                self.window.vault_info_page.update_info(
                    cloud_size=_human_size(total_size),
                    file_count=str(count),
                )

        emitter.done.connect(_on_done)

        def _bg() -> None:
            total_size = 0
            count = 0
            stack = [""]
            while stack:
                cur = stack.pop()
                try:
                    entries = backend.list_dir(cur)
                except Exception:
                    continue
                for e in entries:
                    if e.is_dir:
                        stack.append(f"{cur}/{e.name}" if cur else e.name)
                    else:
                        total_size += e.size
                        count += 1
            emitter.done.emit(total_size, count)

        import threading

        th = threading.Thread(target=_bg, daemon=True)
        # 保持引用防止线程/发射器被回收
        self._stats_thread = (th, emitter)  # type: ignore[assignment]
        th.start()


def main() -> int:
    """程序入口。"""
    import os
    import tempfile

    # 重定向 qfluentwidgets 的 qconfig 落盘路径：库设置卡片默认把配置写到
    # 工作目录的 config/config.json，会污染源码目录/打包目录；本应用自有
    # 持久化（SettingsStore），此处仅把库配置引到临时目录
    from qfluentwidgets import qconfig

    qconfig.load(
        os.path.join(tempfile.gettempdir(), "cloudprism_qfluent_config.json")
    )

    app = QApplication(sys.argv)
    store = SettingsStore()
    # 应用 Fluent 主题（浅色 / 深色 / 跟随系统，沿用持久化选择）
    modes = ["system", "dark", "light"]
    idx = store.theme_index()
    mode = modes[idx] if 0 <= idx < len(modes) else "system"
    apply_theme(app, mode, font_size=store.font_size())
    # 恢复持久化的字体大小（apply_theme 内部已设置，此处兼顾其他控件）
    font = app.font()
    font.setPointSize(store.font_size())
    app.setFont(font)

    # 窗口图标：优先打包内嵌资源，其次源码目录的 assets/
    from PySide6.QtGui import QIcon

    base = getattr(sys, "_MEIPASS", os.path.join(os.path.dirname(__file__)))
    for cand in (
        os.path.join(base, "assets", "icon.png"),
        os.path.join(os.path.dirname(base), "assets", "icon.png"),
        os.path.join(base, "..", "..", "assets", "icon.png"),
    ):
        if os.path.exists(cand):
            app.setWindowIcon(QIcon(cand))
            break

    window = MainWindow()
    controller = AppController(window)  # noqa: F841  控制器需保持引用
    window.show()
    exit_code = app.exec()
    # 停止性能监控
    controller._perf.stop()
    # 退出时清零主密码与派生密钥，防止内存残留
    if controller.session is not None:
        controller.session.close()
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
