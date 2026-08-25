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

from PySide6.QtCore import QModelIndex, Qt
from PySide6.QtWidgets import QApplication, QFileDialog, QMessageBox

from cloudprism.core.session import Session
from cloudprism.crypto.filename import FilenameCipher
from cloudprism.gui.dir_tree_model import DirTreeModel
from cloudprism.gui.init_wizard import InitWizard
from cloudprism.gui.main_window import MainWindow
from cloudprism.gui.perf_monitor import PerfMonitor
from cloudprism.gui.theme import apply_windows11_style
from cloudprism.gui.transfer_worker import TransferWorker, start_transfer
from cloudprism.storage.backend import StorageBackend


def _human_size(n: int) -> str:
    """字节数 -> 人类可读大小。"""
    size = float(n)
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if size < 1024 or unit == "TB":
            return f"{int(size)} {unit}" if unit == "B" else f"{size:.1f} {unit}"
        size /= 1024
    return f"{n} B"


class AppController:
    """应用控制器：连接窗口信号与各功能模块。"""

    def __init__(self, window: MainWindow) -> None:
        self.window = window
        # 向导产出（连接Mi库后填充）
        self.session: Session | None = None
        self.backend: StorageBackend | None = None
        self.metadata = None

        # 目录树模型
        self._tree_model: DirTreeModel | None = None

        # 性能监控
        self._perf = PerfMonitor(parent=window)
        self._perf.statsUpdated.connect(window.update_perf_stats)

        # 信号接线
        window.initRequested.connect(self.show_init_wizard)
        window.uploadRequested.connect(self.upload_file)
        window.downloadRequested.connect(self.download_file)
        window.playRequested.connect(self.play_file)

        # 文件树选中 -> 预览
        window.file_tree.selectionModel().selectionChanged.connect(
            self._on_tree_selection
        ) if window.file_tree.selectionModel() else None

        # 设置页更新连接信息
        window.settings_page.cacheSettingsChanged.connect(self._on_cache_settings_changed)

        # 密库信息页信号
        window.vault_info_page.connectRequested.connect(self.show_init_wizard)
        window.vault_info_page.refreshRequested.connect(self._refresh_vault_info)

        # 文件树拖放信号
        window.file_tree.filesDropped.connect(self._on_files_dropped)
        window.file_tree.filesDraggedOut.connect(self._on_files_dragged_out)

        # 启动性能监控
        self._perf.start()

    # ------------------------------------------------------------------
    # 初始化 / 连接
    # ------------------------------------------------------------------

    def show_init_wizard(self) -> None:
        """弹出初始化向导；成功后装载后端与目录树。"""
        wizard = InitWizard(self.window)
        wizard.finishedSetup.connect(lambda: self._apply_setup(wizard))
        if wizard.exec() == InitWizard.Accepted and wizard.metadata is not None:
            self._apply_setup(wizard)

    def _apply_setup(self, wizard: InitWizard) -> None:
        """应用向导产出：设置后端、目录树与状态栏。"""
        self.session = wizard.session
        self.backend = wizard.backend
        self.metadata = wizard.metadata

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

        # 更新设置页连接信息
        backend_type = type(self.backend).__name__
        backend_path = getattr(self.backend, "_root", "") or getattr(self.backend, "_url", "")
        self.window.settings_page.update_connection_info(
            backend_type=backend_type,
            backend_path=backend_path,
            filename_enc=self.metadata.filename_enc,
        )
        self.window.settings_page._refresh_cache_usage()

        # 更新密库信息页
        self._update_vault_info_page()

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
        """选择本地文件（支持多选）-> 加密上传到选中目录。"""
        if not self._require_vault():
            return
        paths, _ = QFileDialog.getOpenFileNames(
            self.window, "选择要加密上传的文件（可多选）"
        )
        if not paths:
            return
        for path in paths:
            name = path.rsplit("/", 1)[-1].rsplit("\\", 1)[-1]
            # 文件名加密开启时，加密文件名主体
            enc_name = self._encrypt_filename(name) if self._is_filename_enc() else name
            remote = f"{remote_dir}/{enc_name}.cpenc" if remote_dir else f"{enc_name}.cpenc"
            dlg = start_transfer(
                TransferWorker.KIND_UPLOAD, self.session, self.backend,
                path, remote,
            )
            dlg.exec()
        self._refresh_tree()

    def upload_files(self, local_paths: list[str], remote_dir: str = "") -> None:
        """批量上传文件（供拖放调用）。"""
        if not self._require_vault() or not local_paths:
            return
        for path in local_paths:
            import os
            if not os.path.isfile(path):
                continue
            name = os.path.basename(path)
            enc_name = self._encrypt_filename(name) if self._is_filename_enc() else name
            remote = f"{remote_dir}/{enc_name}.cpenc" if remote_dir else f"{enc_name}.cpenc"
            dlg = start_transfer(
                TransferWorker.KIND_UPLOAD, self.session, self.backend,
                path, remote,
            )
            dlg.exec()
        self._refresh_tree()

    def download_file(self, remote_path: str = "") -> None:
        """选择保存位置 -> 下载解密选中文件。"""
        if self._require_vault() and remote_path:
            # 去掉 .cpenc 作为默认保存名
            default = remote_path.rsplit("/", 1)[-1]
            if default.endswith(".cpenc"):
                default = default[: -len(".cpenc")]
            path, _ = QFileDialog.getSaveFileName(
                self.window, "保存解密文件", default
            )
            if path:
                dlg = start_transfer(
                    TransferWorker.KIND_DOWNLOAD, self.session, self.backend,
                    path, remote_path,
                )
                dlg.exec()

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
        from PySide6.QtWidgets import QFileDialog
        import os
        import tempfile
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
            dlg = start_transfer(
                TransferWorker.KIND_DOWNLOAD, self.session, self.backend,
                local_path, rp,
            )
            dlg.exec()

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

    def _refresh_vault_info(self) -> None:
        """刷新密库信息页。"""
        self._update_vault_info_page()

    def _update_vault_info_page(self) -> None:
        """更新密库信息页显示。"""
        if self.session is None or self.backend is None:
            self.window.vault_info_page.show_disconnected()
            return

        self.window.vault_info_page.show_connected()

        # 计算库名称（vault_id 截短）
        vault_id_hex = self.metadata.vault_id.hex() if self.metadata else "-"
        vault_name = vault_id_hex[:8] + "..." if len(vault_id_hex) > 8 else vault_id_hex

        # 后端信息
        backend_type = type(self.backend).__name__
        backend_path = getattr(self.backend, "_root", "") or getattr(self.backend, "_url", "")

        # 估算云端大小（遍历后端文件）
        cloud_size = "计算中…"
        file_count = "-"
        try:
            entries = self.backend.list_dir("")
            total_size = 0
            count = 0
            for e in entries:
                if not e.is_dir:
                    total_size += e.size
                    count += 1
            cloud_size = _human_size(total_size)
            file_count = str(count)
        except Exception:
            cloud_size = "-"

        # 本地缓存大小
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
            cloud_size=cloud_size,
            cache_size=cache_size,
            file_count=file_count,
        )


def main() -> int:
    """程序入口。"""
    app = QApplication(sys.argv)
    # 应用 Windows 11 原生风格主题
    apply_windows11_style(app)
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
