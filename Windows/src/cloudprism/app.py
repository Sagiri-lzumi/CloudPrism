"""CloudPrism Windows 客户端入口（组合根）。

组装各模块：
  MainWindow（界面骨架）
    + InitWizard（初始化/连接，产出 session/backend/metadata）
    + TransferWorker（上传/下载，进度对话框）
    + PlayerView（流式解密播放）
    + DecryptingProxyServer（由 PlayerView 按需启停）
"""

from __future__ import annotations

import sys

from PySide6.QtWidgets import QApplication, QFileDialog, QMessageBox

from cloudprism.core.session import Session
from cloudprism.crypto.filename import FilenameCipher
from cloudprism.gui.init_wizard import InitWizard
from cloudprism.gui.main_window import MainWindow
from cloudprism.gui.player_view import PlayerView
from cloudprism.gui.transfer_worker import TransferWorker, start_transfer
from cloudprism.storage.backend import StorageBackend


class AppController:
    """应用控制器：连接窗口信号与各功能模块。"""

    def __init__(self, window: MainWindow) -> None:
        self.window = window
        # 向导产出（连接金库后填充）
        self.session: Session | None = None
        self.backend: StorageBackend | None = None
        self.metadata = None
        # 播放器窗口（同一时间一个）
        self._player: PlayerView | None = None

        # 信号接线
        window.initRequested.connect(self.show_init_wizard)
        window.uploadRequested.connect(self.upload_file)
        window.downloadRequested.connect(self.download_file)
        window.playRequested.connect(self.play_file)

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

        self.window.set_backend(self.backend, name_decryptor=name_decryptor)
        mode = "新建" if wizard.is_new_mode() else "连接"
        self.window.set_status(
            f"{mode}成功 · 文件名加密：{'开' if self.metadata.filename_enc else '关'}"
        )

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
                self.window.refresh()

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
        """流式播放选中的加密媒体文件。"""
        if self._require_vault() and remote_path:
            # 关闭旧播放窗口（同时停旧代理）
            if self._player is not None:
                self._player.close()
            self._player = PlayerView(
                self.session, self.backend, remote_path, parent=self.window
            )
            self._player.show()

    # ------------------------------------------------------------------
    # 辅助
    # ------------------------------------------------------------------

    def _require_vault(self) -> bool:
        """未连接金库时提示并返回 False。"""
        if self.session is None or self.backend is None:
            QMessageBox.information(
                self.window, "提示", "请先通过「金库 - 初始化/连接」连接云盘"
            )
            return False
        return True


def main() -> int:
    """程序入口。"""
    app = QApplication(sys.argv)
    window = MainWindow()
    controller = AppController(window)  # noqa: F841  控制器需保持引用
    window.show()
    return app.exec()


if __name__ == "__main__":
    sys.exit(main())
