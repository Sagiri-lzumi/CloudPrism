"""传输 worker：在 QThread 中执行加密上传 / 下载解密。

TransferWorker 封装 Encryptor / Decryptor 管线，提供：
  - progress(float) 信号：进度 0.0~1.0
  - finished() 信号：成功完成
  - cancelled() 信号：用户取消
  - error(str) 信号：异常信息
  - cancel()：请求取消（下一分块边界生效）

start_transfer() 一步完成线程搭建并弹出进度对话框（进度条 + 取消按钮）。
"""

from __future__ import annotations

from PySide6.QtCore import QObject, QThread, Signal, Slot
from PySide6.QtWidgets import (
    QDialog,
    QLabel,
    QProgressBar,
    QPushButton,
    QVBoxLayout,
)

from cloudprism.core.decryptor import Decryptor
from cloudprism.core.encryptor import Encryptor
from cloudprism.core.session import Session
from cloudprism.storage.backend import StorageBackend


class TransferWorker(QObject):
    """上传/下载 worker（运行于工作线程）。"""

    # 进度 0.0~1.0
    progress = Signal(float)
    # 成功完成
    finished = Signal()
    # 用户取消
    cancelled = Signal()
    # 出错（参数：错误信息）
    error = Signal(str)

    # 传输方向
    KIND_UPLOAD = "upload"
    KIND_DOWNLOAD = "download"

    def __init__(
        self,
        kind: str,
        session: Session,
        backend: StorageBackend,
        local_path: str,
        remote_path: str,
        chunk: int = 1 << 20,
        parent: QObject | None = None,
        max_workers: int = 1,
        kdf_salt: bytes | None = None,
    ) -> None:
        super().__init__(parent)
        self.kind = kind
        self.session = session
        self.backend = backend
        self.local_path = local_path
        self.remote_path = remote_path
        self.chunk = chunk
        self.max_workers = max_workers
        # 上传加密复用的 KDF 盐（密库元信息盐）：命中 Session 密钥缓存，
        # 避免每文件重跑 PBKDF2；None 时 Encryptor 回退随机盐。
        self.kdf_salt = kdf_salt
        self._cancel_requested = False

        # 文件名（供进度显示）
        import os
        self.file_name = os.path.basename(local_path)

    def cancel(self) -> None:
        """请求取消（在下一分块边界生效）。"""
        self._cancel_requested = True

    @Slot()
    def run(self) -> None:
        """执行传输（在工作线程中调用）。"""
        try:
            if self.kind == self.KIND_UPLOAD:
                pipeline = Encryptor(
                    self.session, self.backend, chunk=self.chunk
                ).encrypt_and_upload(
                    self.local_path, self.remote_path,
                    max_workers=self.max_workers,
                    salt=self.kdf_salt,
                )
            else:
                pipeline = Decryptor(
                    self.session, self.backend, chunk=self.chunk
                ).download_and_decrypt(self.remote_path, self.local_path)

            last = 0.0
            for p in pipeline:
                # 取消检查：每个分块边界一次
                if self._cancel_requested:
                    self.cancelled.emit()
                    return
                last = p
                self.progress.emit(p)

            if self._cancel_requested:
                self.cancelled.emit()
                return
            # 确保终值 1.0 一定发出（空文件管线可能不产出）
            if last < 1.0:
                self.progress.emit(1.0)
            self.finished.emit()
        except Exception as e:  # noqa: BLE001
            self.error.emit(str(e))


class TransferDialog(QDialog):
    """传输进度对话框（进度条 + 取消按钮）。"""

    def __init__(self, worker: TransferWorker, title: str, parent=None) -> None:
        super().__init__(parent)
        self.setWindowTitle(title)
        self.setMinimumWidth(360)
        self.worker = worker

        lay = QVBoxLayout(self)
        self._label = QLabel("传输中…", self)
        lay.addWidget(self._label)
        self._bar = QProgressBar(self)
        self._bar.setRange(0, 100)
        lay.addWidget(self._bar)
        self._cancel_btn = QPushButton("取消", self)
        self._cancel_btn.clicked.connect(self._on_cancel)
        lay.addWidget(self._cancel_btn)

        # 信号接线
        worker.progress.connect(self._on_progress)
        worker.finished.connect(self.accept)
        worker.cancelled.connect(self.reject)
        worker.error.connect(self._on_error)

    def _on_progress(self, p: float) -> None:
        """更新进度条。"""
        self._bar.setValue(int(p * 100))
        self._label.setText(f"传输中… {int(p * 100)}%")

    def _on_cancel(self) -> None:
        """取消按钮：请求取消（对话框由 cancelled 信号关闭）。"""
        self._cancel_btn.setEnabled(False)
        self._label.setText("正在取消…")
        self.worker.cancel()

    def _on_error(self, msg: str) -> None:
        """出错：显示错误，关闭对话框。"""
        self._label.setText(f"出错：{msg}")
        self._cancel_btn.setText("关闭")
        self._cancel_btn.setEnabled(True)
        self._cancel_btn.clicked.disconnect()
        self._cancel_btn.clicked.connect(self.reject)


def start_transfer(
    kind: str,
    session: Session,
    backend: StorageBackend,
    local_path: str,
    remote_path: str,
    chunk: int = 1 << 20,
    parent=None,
    max_workers: int = 1,
    kdf_salt: bytes | None = None,
) -> TransferDialog:
    """一步启动：创建 worker + 线程 + 进度对话框。

    返回对话框（模态由调用方决定）；线程随传输结束自动清理。
    注意：新代码应优先使用 start_transfer_bg()。
    """
    worker, thread = create_worker_thread(
        kind, session, backend, local_path, remote_path, chunk, parent,
        max_workers, kdf_salt,
    )

    title = "加密上传" if kind == TransferWorker.KIND_UPLOAD else "下载解密"
    dialog = TransferDialog(worker, title, parent)
    # 对话框持有线程引用防止被回收
    dialog._thread = thread  # noqa: SLF001
    thread.start()
    return dialog


def create_worker_thread(
    kind: str,
    session: Session,
    backend: StorageBackend,
    local_path: str,
    remote_path: str,
    chunk: int = 1 << 20,
    parent=None,
    max_workers: int = 1,
    kdf_salt: bytes | None = None,
) -> tuple[TransferWorker, QThread]:
    """创建 worker + 线程并接线，但不启动线程。

    供 TransferQueue 等调度器使用：调用方自行决定启动时机，
    并连接 worker 的 progress/finished/cancelled/error 信号。
    """
    thread = QThread(parent)
    worker = TransferWorker(
        kind, session, backend, local_path, remote_path,
        chunk=chunk, max_workers=max_workers, kdf_salt=kdf_salt,
    )
    worker.moveToThread(thread)

    # 生命周期：started -> run；结束信号 -> 线程退出；线程收尾 -> 对象清理
    thread.started.connect(worker.run)
    for sig in (worker.finished, worker.cancelled, worker.error):
        sig.connect(thread.quit)
    thread.finished.connect(worker.deleteLater)
    thread.finished.connect(thread.deleteLater)

    return worker, thread


def start_transfer_bg(
    kind: str,
    session: Session,
    backend: StorageBackend,
    local_path: str,
    remote_path: str,
    chunk: int = 1 << 20,
    parent=None,
    max_workers: int = 1,
    kdf_salt: bytes | None = None,
) -> tuple[TransferWorker, QThread]:
    """后台启动传输（不弹窗），返回 (worker, thread)。

    调用方负责连接 worker 的 progress/finished/cancelled/error 信号。
    线程在传输结束后自动清理。
    """
    worker, thread = create_worker_thread(
        kind, session, backend, local_path, remote_path, chunk, parent,
        max_workers, kdf_salt,
    )
    thread.start()
    return worker, thread
