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
from cloudprism.gui.transfer_worker import TransferWorker, start_transfer
from cloudprism.storage.backend import StorageBackend


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
        """选择本地文件 -> 加密上传到选中目录。"""
        if self._require_vault():
            path, _ = QFileDialog.getOpenFileName(
                self.window, "选择要加密上传的文件"
            )
            if path:
                # 目标路径 = 选中目录（或根）+ 原文件名 + .cpenc
                name = path.rsplit("/", 1)[-1].rsplit("\\", 1)[-1]
                remote = f"{remote_dir}/{name}.cpenc" if remote_dir else f"{name}.cpenc"
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

    def _refresh_tree(self) -> None:
        """刷新文件树。"""
        if self._tree_model is not None:
            self._tree_model.reload()


def main() -> int:
    """程序入口。"""
    app = QApplication(sys.argv)
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
