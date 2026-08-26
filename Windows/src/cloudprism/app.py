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
from PySide6.QtWidgets import (
    QApplication,
    QFileDialog,
    QInputDialog,
    QLineEdit,
    QMessageBox,
)

# Fluent 提示组件（模态确认用 MessageBox，非阻断提示用 InfoBar）
from qfluentwidgets import InfoBar, InfoBarPosition
from qfluentwidgets import MessageBox as FluentMessageBox

from cloudprism.core.session import Session
from cloudprism.core.settings_store import SettingsStore
from cloudprism.core.sync_engine import SyncEngine
from cloudprism.core.thumbnail import ThumbnailCache
from cloudprism.core.backend_factory import describe_backend
from cloudprism.core.vault_manager import VaultManager
from cloudprism.crypto.filename import FilenameCipher
from cloudprism.gui.busy_op import run_busy
from cloudprism.gui.dir_tree_model import DirTreeModel
from cloudprism.gui.init_wizard import InitWizard, RecoveryCodeDialog
from cloudprism.gui.main_window import MainWindow
from cloudprism.gui.perf_monitor import PerfMonitor
from cloudprism.gui.quick_connect import QuickConnectDialog
from cloudprism.gui.theme import apply_theme, current_mode
from cloudprism.core.transfer_queue import (
    STATE_CANCELLED,
    TransferQueue,
    TransferTask,
)
from cloudprism.storage.backend import StorageBackend


def _human_size(n: int) -> str:
    """字节数 -> 人类可读大小。"""
    size = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if size < 1024 or unit == "TB":
            return f"{int(size)} {unit}" if unit == "B" else f"{size:.1f} {unit}"
        size /= 1024
    return f"{n} B"


def _vault_display_name(metadata, ellipsis: bool = False) -> str:
    """密库显示名：用户自定义名称优先，否则回退 vault_id 前 8 位。

    :param ellipsis: 回退名是否追加省略号（信息页长标识用）
    """
    if metadata is None:
        return "-"
    name = getattr(metadata, "name", "") or ""
    if name.strip():
        return name.strip()
    vault_id_hex = metadata.vault_id.hex()
    short = vault_id_hex[:8]
    return short + "..." if ellipsis and len(vault_id_hex) > 8 else short


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

        # 子目录密库的根前缀（连接后由 _apply_connection 填充）
        self._vault_root: str = ""
        # 恢复码生成后台线程引用（防 GC）；测试可置 _sync_vault_ops 同步执行
        self._recovery_thread = None
        self._sync_vault_ops = False

        # 接入设置持久化存储（须在自动锁定定时器同步前完成载入）
        self._store = SettingsStore()
        window.settings_page.attach_store(self._store)

        # 传输队列（任务级并发调度；并发数持久化，传输参数入队时从设置页同步）
        self._queue = TransferQueue(self.window)
        self._queue.max_concurrent = max(1, self._store.concurrent_count())
        self._queue.taskProgress.connect(self._on_task_progress)
        self._queue.taskFinished.connect(self._on_task_finished)
        self._queue.aggregateProgress.connect(self._on_aggregate_progress)
        self._task_cards: dict = {}  # task -> 传输队列页条目
        self._batch_total = 0        # 当前批次任务总数（底部条 (n/m) 计数）
        # 文件夹同步批次跟踪（任务完成后写回加密索引）
        self._sync_engine: SyncEngine | None = None
        self._sync_pending: dict = {}   # task -> 同步条目（含 rel_path/size/mtime/kind）
        self._sync_done: list = []
        window.transfer_progress.cancelRequested.connect(self._queue.cancel_all)
        window.settings_page.concurrencyChanged.connect(
            self._queue.set_max_concurrent
        )
        window.transfers_page.retryRequested.connect(self._on_retry_task)

        # 自动锁定：每 10 秒检查一次空闲时长；事件过滤器刷新最后活跃时间
        self._last_active = time.monotonic()
        self._lock_timer = QTimer(self.window)
        self._lock_timer.setInterval(10_000)
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
        window.settings_page.recoveryCodeRequested.connect(
            self._on_generate_recovery_code
        )
        window.settings_page.syncRequested.connect(self._on_sync_folder)

        # 密库信息页信号（含最近密库快速连接/移除/重命名）
        window.vault_info_page.connectRequested.connect(self.show_init_wizard)
        window.vault_info_page.quickConnectRequested.connect(self._on_quick_connect)
        window.vault_info_page.removeVaultRequested.connect(self._on_remove_vault)
        window.vault_info_page.renameRequested.connect(self._on_rename_vault)
        window.vault_info_page.connectOtherVaultRequested.connect(
            self._on_connect_other_vault
        )
        window.vault_info_page.refreshRequested.connect(self._refresh_vault_info)
        window.vault_info_page.lockRequested.connect(self._lock_vault)

        # 设置页密库重命名请求（与密库页共用同一控制器槽）
        window.settings_page.vaultRenameRequested.connect(self._on_rename_vault)

        # 文件树拖放信号
        window.file_tree.filesDropped.connect(self._on_files_dropped)
        window.file_tree.filesDraggedOut.connect(self._on_files_dragged_out)

        # 文件页视图切换（网格视图需刷新当前目录缩略图）
        window.side_panel.files_page.viewModeChanged.connect(
            lambda _mode: self._refresh_grid()
        )

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
        """定时检查：空闲超时且已连接密库时自动锁定。

        传输进行中豁免：锁库会使会话失效导致在传任务全部失败。
        """
        minutes = self.window.settings_page.auto_lock_minutes
        if minutes <= 0 or self.session is None:
            return
        if self._queue.has_active:
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
        # 建库/开库完成（可能异步）时经 finishedSetup 应用一次即可，
        # exec 返回后不再重复应用，避免恢复码弹窗与装载动作执行两次
        wizard.exec()

    def _apply_setup(self, wizard: InitWizard) -> None:
        """应用向导产出并记录最近密库。"""
        self._apply_connection(
            wizard.backend, wizard.metadata, wizard.session,
            vault_path=getattr(wizard, "vault_path", ""),
        )
        self._remember_current_vault()
        # 新建密库时生成的恢复码：入库完成后展示，提示用户离线保存
        code = getattr(wizard, "recovery_code", "")
        if code:
            RecoveryCodeDialog(code, parent=self.window).exec()

    def _on_generate_recovery_code(self) -> None:
        """生成/更换恢复码：先输入主密码确认，重加密 Marker 后展示新码。"""
        if not self._require_vault() or self.backend is None:
            return
        password, ok = QInputDialog.getText(
            self.window,
            "生成恢复码",
            "请输入主密码确认身份：",
            QLineEdit.EchoMode.Password,
        )
        if not ok or not password:
            return

        vm = VaultManager(self.backend)
        vault_root = self._vault_root

        def op():
            # 3 次数秒级派生，后台执行避免冻结界面
            return vm.generate_recovery_code(password, vault_root)

        def on_done(result):
            code, meta = result
            self.metadata = meta
            self.window.settings_page.update_recovery_state(True)
            RecoveryCodeDialog(code, parent=self.window).exec()

        def on_error(msg: str):
            box = FluentMessageBox("生成恢复码失败", msg, self.window)
            box.exec()

        # 持有线程引用防 GC；测试可置 _sync_vault_ops 走同步路径
        self._recovery_thread = run_busy(
            op, on_done, on_error,
            parent=self.window, sync=self._sync_vault_ops,
        )

    def _on_sync_folder(self, local_dir: str) -> None:
        """文件夹同步：对比索引生成计划，新增/变更文件走统一队列。"""
        import os

        if not self._require_vault() or self.session is None or self.backend is None:
            return
        if not os.path.isdir(local_dir):
            self.window.settings_page.update_sync_status("本地目录不存在，请重新选择")
            return
        if self._sync_pending:
            self.window.settings_page.update_sync_status("上一轮同步尚未结束，请稍候")
            return
        self._store.set_sync_dir(local_dir)

        engine = SyncEngine(
            self.session, self.backend,
            vault_path=self._vault_root,
            filename_enc=bool(self.metadata and self.metadata.filename_enc),
            name_salt=self.metadata.salt if self.metadata else None,
        )
        try:
            plan = engine.plan(local_dir)
        except Exception as exc:  # noqa: BLE001
            box = FluentMessageBox("同步失败", str(exc), self.window)
            box.exec()
            return
        if plan.pending_count == 0:
            self.window.settings_page.update_sync_status(
                "已是最新：无新增或变更文件"
            )
            return

        tasks = engine.build_tasks(plan)
        entries = list(plan.new) + list(plan.changed)
        for e, kind in [(e, "new") for e in plan.new] + [
            (e, "changed") for e in plan.changed
        ]:
            e["kind"] = kind
        self._sync_engine = engine
        self._sync_pending = dict(zip(tasks, entries))
        self._sync_done = []
        self.window.settings_page.update_sync_status(
            f"同步中：{len(plan.new)} 新增 / {len(plan.changed)} 变更"
        )
        self._enqueue_tasks(tasks)

    def _finish_sync_batch(self) -> None:
        """同步批次全部终态：写回加密索引并展示结果摘要。"""
        engine = self._sync_engine
        done = self._sync_done
        self._sync_engine = None
        self._sync_done = []
        if engine is None:
            return
        entries = {
            e["rel_path"]: {"size": e["size"], "mtime": e["mtime"]}
            for e in done
        }
        try:
            engine.update_index(entries)
        except Exception as exc:  # noqa: BLE001
            InfoBar.warning(
                title="同步提醒",
                content=f"文件已上传但索引写入失败（下次将重传）：{exc}",
                parent=self.window, position=InfoBarPosition.TOP_RIGHT, duration=5000,
            )
            return
        n_new = sum(1 for e in done if e.get("kind") == "new")
        n_changed = len(done) - n_new
        self.window.settings_page.update_sync_status(
            f"同步完成：{n_new} 新增 / {n_changed} 更新"
        )
        self._refresh_tree()

    def _apply_connection(
        self, backend, metadata, session, vault_path: str = "",
    ) -> None:
        """装载一次成功连接：后端、目录树、状态栏与各信息页。

        向导与快速连接共用本方法；子目录密库经 vault_path 指定根前缀。
        """
        self.session = session
        self.backend = backend
        self.metadata = metadata
        # 子目录密库的根前缀（文件树与路径拼接统一经目录树模型处理）
        self._vault_root = (vault_path or "").strip("/")

        # 绑定传输队列（任务入队后即可调度）
        self._queue.bind(session, backend)

        # 缩略图缓存与会话绑定（锁库时丢弃）
        self._thumb_cache = ThumbnailCache(session)

        # 文件名加密开启时注入解密器（目录树显示原始名）
        name_decryptor = None
        if self.metadata is not None and self.metadata.filename_enc:
            salt = self.metadata.salt

            def name_decryptor(enc_name: str) -> str:  # noqa: F811
                key = self.session.derive_key(salt)
                return FilenameCipher.decrypt(enc_name, key)

        # 装载目录树模型（子目录密库以密库位置为可见根）
        self._tree_model = DirTreeModel(
            self.backend, name_decryptor=name_decryptor,
            root=self._vault_root,
        )
        self.window.side_panel.files_page.set_model(self._tree_model)

        # 连接文件树选中信号
        sel_model = self.window.file_tree.selectionModel()
        if sel_model:
            sel_model.selectionChanged.connect(self._on_tree_selection)

        # 更新状态栏
        self.window.set_connected(True)

        # 更新设置页连接信息（友好显示名而非类名；含自定义密库名称）
        backend_label, backend_path, _ = describe_backend(self.backend)
        self.window.settings_page.update_connection_info(
            backend_type=backend_label,
            backend_path=backend_path,
            filename_enc=self.metadata.filename_enc,
            vault_name=_vault_display_name(self.metadata),
        )
        # 恢复码状态（Marker 是否携带 v3 恢复块）
        self.window.settings_page.update_recovery_state(
            getattr(self.metadata, "has_recovery", False)
        )
        self.window.settings_page._refresh_cache_usage()

        # 更新密库信息页
        self._update_vault_info_page()

        # 检测上次未完成的传输，提供续传入口（横幅）
        self._maybe_offer_resume()

    def _maybe_offer_resume(self) -> None:
        """存在未完成传输记录时，在传输页顶部显示恢复横幅。"""
        pending = self._store.pending_transfers()
        if not pending:
            self.window.transfers_page.hide_resume_banner()
            return
        self.window.transfers_page.show_resume_banner(
            len(pending), self._resume_pending_transfers
        )

    def _resume_pending_transfers(self) -> None:
        """将上次未完成的传输记录重建为任务并重新入队。

        上传任务携带记录时的 size/mtime 快照，启动前经队列做脏续传校验；
        本地源文件已不存在的记录静默跳过。
        """
        import os
        pending = self._store.pending_transfers()
        if not pending or not self._require_vault():
            return
        tasks: list[TransferTask] = []
        for rec in pending:
            local = rec.get("local", "")
            remote = rec.get("remote", "")
            if not local or not remote:
                continue
            direction = rec.get("direction", "upload")
            if direction == "upload" and not os.path.isfile(local):
                continue  # 源文件已不存在，无法续传
            task = TransferTask(
                local_path=local, remote_path=remote,
                display_name=rec.get("name") or os.path.basename(local),
                direction=direction,
            )
            if direction == "upload" and rec.get("size") is not None:
                task.expected_size = rec.get("size")
                task.expected_mtime = rec.get("mtime")
            tasks.append(task)
        # 无论是否全部有效都隐藏横幅：无效记录随本次重写清理
        self._store.set_pending_transfers([])
        if tasks:
            self._enqueue_tasks(tasks)

    def _remember_current_vault(self) -> None:
        """将当前成功连接写入最近密库记录并刷新引导页列表。"""
        if self.backend is None:
            return
        label, path, btype = describe_backend(self.backend)
        record = {
            "backend_type": btype,
            "label": label,
            "path": path,
            "vault_name": _vault_display_name(self.metadata),
            # 子目录密库位置；重连时按此定位 Marker，缺失会误报密码错误
            "vault_path": self._vault_root,
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
            # 子目录密库经记录中的位置还原根前缀
            self._apply_connection(
                dlg.backend, dlg.metadata, dlg.session,
                vault_path=dlg.vault_path,
            )
            self._remember_current_vault()

    def _on_connect_other_vault(self, vault_path: str) -> None:
        """连接本后端的其他密库：复用当前后端，仅需目标密库主密码。"""
        if self.backend is None:
            return
        show_name = vault_path or "根目录"
        password, ok = QInputDialog.getText(
            self.window,
            "连接其他密库",
            f"请输入密库“{show_name}”的主密码：",
            QLineEdit.EchoMode.Password,
        )
        if not ok or not password:
            return
        try:
            meta = VaultManager(self.backend).open_vault(password, vault_path)
        except Exception as exc:  # noqa: BLE001
            box = FluentMessageBox("连接失败", str(exc), self.window)
            box.exec()
            return
        if meta is None:
            box = FluentMessageBox(
                "连接失败", "主密码不正确。", self.window
            )
            box.exec()
            return
        self._apply_connection(
            self.backend, meta, Session(password), vault_path=vault_path
        )
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

    def _on_rename_vault(self, new_name: str) -> None:
        """修改当前密库名称：重新加密 Vault Marker 并覆盖上传。

        名称随文件保存，跨设备跟随密库；失败时界面状态不变更。
        """
        if not self._require_vault():
            return
        new_name = (new_name or "").strip()
        if not new_name:
            return
        try:
            self.metadata = VaultManager(self.backend).rename_vault(
                self.session.master_password, new_name
            )
        except Exception as e:  # noqa: BLE001
            box = FluentMessageBox("修改名称失败", str(e), self.window)
            box.exec()
            return
        # 成功后同步刷新信息页、设置页与最近记录（名称全局一致）
        self._update_vault_info_page()
        backend_label, backend_path, _ = describe_backend(self.backend)
        self.window.settings_page.update_connection_info(
            backend_type=backend_label,
            backend_path=backend_path,
            filename_enc=self.metadata.filename_enc,
            vault_name=new_name,
        )
        self._remember_current_vault()

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
        # 网格视图下选中变更同步刷新当前目录（选中文件则显示其所在目录）
        self._refresh_grid()

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
                self._enqueue_tasks([TransferTask(
                    local_path=path, remote_path=remote_path,
                    display_name=default, direction="download",
                )])

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
        """文件拖出：解密下载到用户选择的目标目录（目录递归展开）。"""
        if not self._require_vault() or not remote_paths:
            return
        import os
        # 让用户选择保存目录
        save_dir = QFileDialog.getExistingDirectory(
            self.window, "选择保存目录"
        )
        if not save_dir:
            return
        tasks: list[TransferTask] = []
        for rp in remote_paths:
            if not rp.endswith(".cpenc"):
                # 目录：递归展开为下载任务（本地目录树由展开器创建）
                tasks.extend(self._expand_remote_dir_tasks(rp, save_dir))
                continue
            # 解密文件名作为本地保存名（去掉 .cpenc 后还原）
            fname = self._decrypt_name(rp.rsplit("/", 1)[-1][: -len(".cpenc")])
            local_path = os.path.join(save_dir, fname)
            tasks.append(TransferTask(
                local_path=local_path, remote_path=rp,
                display_name=fname, direction="download",
            ))
        self._enqueue_tasks(tasks)

    def _selected_remote_dir(self) -> str:
        """获取文件树当前选中目录的远端路径（无选中时为密库根）。"""
        indexes = self.window.file_tree.selectedIndexes()
        if not indexes:
            return getattr(self, "_vault_root", "")
        node = indexes[0].internalPointer()
        if node is None:
            return getattr(self, "_vault_root", "")
        if not node.is_dir:
            node = node.parent
        if node is None:
            return getattr(self, "_vault_root", "")
        parts: list[str] = []
        cur = node
        while cur is not None and cur.name:
            parts.append(cur.name)
            cur = cur.parent
        parts.reverse()
        if not parts:
            # 树根节点：子目录密库时为其位置路径，否则为后端根
            return getattr(self, "_vault_root", "")
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
        self._refresh_grid()

    def _refresh_grid(self) -> None:
        """网格视图下刷新当前目录条目与缩略图（列表视图下空操作）。"""
        files_page = self.window.side_panel.files_page
        if not files_page.is_grid_mode() or self._tree_model is None:
            return
        if self.session is None or self.backend is None:
            return

        # 当前目录：选中目录本身；选中文件则取其父目录；无选中为根
        indexes = files_page.tree.selectedIndexes()
        index = indexes[0] if indexes else QModelIndex()
        node = index.internalPointer() if index.isValid() else None
        if node is not None and not node.is_dir:
            index = index.parent()
            node = index.internalPointer() if index.isValid() else None

        model = self._tree_model
        if model.canFetchMore(index):
            model.fetchMore(index)

        from cloudprism.gui.preview_panel import classify_file

        entries = []
        for child in model.dir_entries(node):
            display = model.display_name(child.name)
            rpath = model.remote_path(child)
            is_image = (not child.is_dir) and classify_file(display) == "image"
            entries.append((display, rpath, child.is_dir, is_image))
        files_page.populate_grid(
            self.session, self.backend, self._thumb_cache, entries
        )

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
        """右键下载：单文件保存解密；文件夹递归展开批量下载。"""
        remote = self._resolve_remote_path(display_path)
        if remote.endswith(".cpenc"):
            self.download_file(remote)
        else:
            self._download_dir(remote)

    def _download_dir(self, remote_dir: str) -> None:
        """下载整个文件夹：选择目标目录后递归展开入统一队列。"""
        if not self._require_vault() or not remote_dir:
            return
        save_dir = QFileDialog.getExistingDirectory(
            self.window, "选择保存目录"
        )
        if not save_dir:
            return
        self._enqueue_tasks(self._expand_remote_dir_tasks(remote_dir, save_dir))

    def _expand_remote_dir_tasks(
        self, remote_dir: str, save_root: str
    ) -> list[TransferTask]:
        """递归展开远端目录为下载任务。

        文件名/目录名按文件名加密设置还原本地名；本地目录树按解密结构
        预先创建，每文件走统一传输队列（天然获得聚合进度/取消/重试）。
        """
        import os
        tasks: list[TransferTask] = []
        dir_name = self._decrypt_name(remote_dir.rsplit("/", 1)[-1])
        stack = [(remote_dir, os.path.join(save_root, dir_name))]
        while stack:
            rdir, ldir = stack.pop()
            try:
                os.makedirs(ldir, exist_ok=True)
                entries = self.backend.list_dir(rdir)
            except Exception:
                continue  # 单个目录失败不影响其他目录
            for e in entries:
                rpath = f"{rdir}/{e.name}"
                if e.is_dir:
                    stack.append(
                        (rpath, os.path.join(ldir, self._decrypt_name(e.name)))
                    )
                else:
                    base = e.name
                    if base.endswith(".cpenc"):
                        base = base[: -len(".cpenc")]
                    fname = self._decrypt_name(base)
                    tasks.append(TransferTask(
                        local_path=os.path.join(ldir, fname),
                        remote_path=rpath,
                        display_name=fname, direction="download",
                    ))
        return tasks

    def _decrypt_name(self, name: str) -> str:
        """后端名还原本地名；文件名加密关闭时原样返回，失败时返回原名。"""
        if not self._is_filename_enc():
            return name
        try:
            salt = self.metadata.salt
            key = self.session.derive_key(salt)
            return FilenameCipher.decrypt(name, key)
        except Exception:
            return name

    # ------------------------------------------------------------------
    # 后台传输管理
    # ------------------------------------------------------------------

    def _upload_paths(self, paths: list[str], remote_dir: str) -> None:
        """批量入队本地路径（文件或文件夹）后台加密上传。"""
        import os
        tasks: list[TransferTask] = []
        for path in paths:
            if os.path.isfile(path):
                tasks.append(self._make_upload_task(path, remote_dir))
            elif os.path.isdir(path):
                tasks.extend(self._expand_dir_tasks(path, remote_dir))
        self._enqueue_tasks(tasks)

    def _make_upload_task(self, local_path: str, remote_dir: str) -> TransferTask:
        """构造单文件上传任务（携带本地源文件快照供脏续传校验）。"""
        import os
        name = os.path.basename(local_path)
        enc_name = self._encrypt_filename(name) if self._is_filename_enc() else name
        remote = f"{remote_dir}/{enc_name}.cpenc" if remote_dir else f"{enc_name}.cpenc"
        try:
            st = os.stat(local_path)
            size, mtime = st.st_size, st.st_mtime
        except OSError:
            size, mtime = None, None
        return TransferTask(
            local_path=local_path, remote_path=remote,
            display_name=name, direction="upload",
            expected_size=size, expected_mtime=mtime,
        )

    def _expand_dir_tasks(self, dir_path: str, remote_dir: str) -> list[TransferTask]:
        """展开目录为上传任务：逐级创建远端目录后逐文件入队。

        目录名按文件名加密设置处理；已存在的目录容错跳过。
        """
        import os
        tasks: list[TransferTask] = []
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

    def _enqueue_tasks(self, tasks: list[TransferTask]) -> None:
        """一批传输任务入队并发队列（追加式，不打断现有任务）。"""
        if not tasks:
            return
        # 传输参数从设置页同步（分块大小 / 并行加密核数）
        self._queue.set_transfer_options(
            chunk=self.window.settings_page.chunk_size,
            max_workers=self.window.settings_page.max_cores,
        )
        transfers_page = self.window.transfers_page
        for t in tasks:
            item = transfers_page.add_task(t.display_name, t.direction)
            item.setData(Qt.ItemDataRole.UserRole, t)  # 重试时映射回任务
            self._task_cards[t] = item
        self._batch_total += len(tasks)
        self._queue.enqueue(tasks)
        self._sync_pending_records()

    def _on_task_progress(self, task: TransferTask) -> None:
        """单任务进度：同步传输页条目与底部进度条。"""
        item = self._task_cards.get(task)
        if item is not None:
            self.window.transfers_page.update_task(item, task.progress)
        bar = self.window.transfer_progress
        if not bar.isVisible():
            # 首次显示：带上批次 (n/m) 计数
            unfinished = len(self._queue.unfinished_tasks())
            bar.show_task(
                task.display_name, direction=task.direction,
                current=max(1, self._batch_total - unfinished + 1),
                total=max(1, self._batch_total),
            )
        bar.update_progress(task.progress)

    def _on_task_finished(self, task: TransferTask, success: bool) -> None:
        """单任务终态：更新条目；全部结束时收尾并刷新文件树。"""
        item = self._task_cards.get(task)
        if item is not None:
            self.window.transfers_page.finish_task(
                item, success, cancelled=(task.state == STATE_CANCELLED)
            )
        # 同步批次跟踪：收集成功条目，批次清空时写回索引并展示摘要
        if self._sync_pending:
            entry = self._sync_pending.pop(task, None)
            if entry is not None:
                if success:
                    self._sync_done.append(entry)
                if not self._sync_pending:
                    self._finish_sync_batch()
        self._sync_pending_records()
        if self._queue.is_idle:
            # 本批全部结束：展示结果并刷新文件树（上传后新文件可见）
            self._batch_total = 0
            self.window.transfer_progress.task_finished(success)
            self._refresh_tree()

    def _on_retry_task(self, task: TransferTask) -> None:
        """手动重试失败/取消的任务（重置传输页条目后重新入队）。"""
        item = self._task_cards.get(task)
        if item is not None:
            self.window.transfers_page.reset_task(item)
        self._queue.retry(task)

    def _on_aggregate_progress(self, done: int, total: int) -> None:
        """聚合进度（字节级）：驱动底部进度条与未完成记录持久化。"""
        self.window.transfer_progress.update_aggregate(done, total)

    def _sync_pending_records(self) -> None:
        """持久化未完成传输记录（重启后续传用）；全部结束后自动清空。

        仅记录路径与快照信息，密码与会话信息不落盘。
        """
        records = []
        for t in self._queue.unfinished_tasks():
            rec = {
                "local": t.local_path, "remote": t.remote_path,
                "name": t.display_name, "direction": t.direction,
            }
            if t.expected_size is not None:
                rec["size"] = t.expected_size
                rec["mtime"] = t.expected_mtime
            records.append(rec)
        self._store.set_pending_transfers(records)

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
        # 重置传输队列（进行中的任务因会话失效而停止；续传记录保留供重连后恢复）
        self._queue.clear()
        self._task_cards.clear()
        self._thumb_cache = None  # 缩略图缓存与会话绑定，一并丢弃
        self.window.set_connected(False)
        self.window.vault_info_page.show_disconnected()
        self.window.preview_panel.show_welcome()
        self.window.transfer_progress.hide_bar()
        # 清空文件树模型与网格视图残留条目
        self.window.side_panel.files_page.tree.setModel(None)
        self.window.side_panel.files_page.grid_list.clear()
        # 引导页回填最近密库记录，便于快速重连
        self.window.vault_info_page.set_recent_vaults(self._store.recent_vaults())
        # 设置页恢复码提示回到未连接态；同步批次状态一并重置
        self.window.settings_page.update_recovery_state(None)
        self._sync_engine = None
        self._sync_pending = {}
        self._sync_done = []

    def _update_vault_info_page(self) -> None:
        """更新密库信息页显示（云端占用/文件数在后台线程递归统计）。"""
        if self.session is None or self.backend is None:
            self.window.vault_info_page.show_disconnected()
            return

        self.window.vault_info_page.show_connected()

        # 库名称：用户自定义名称优先，否则回退 vault_id 截短
        vault_name = _vault_display_name(self.metadata, ellipsis=True)

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

        # 本后端的其他密库（子目录 Marker 扫描；排除当前连接的密库）
        try:
            vaults = VaultManager(self.backend).list_vaults()
        except Exception:  # noqa: BLE001
            vaults = []
        others = [
            v for v in vaults if (v.get("path") or "") != self._vault_root
        ]
        self.window.vault_info_page.set_other_vaults(others)

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
