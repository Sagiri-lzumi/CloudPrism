"""传输 worker 测试（pytest-qt）。

验证上传/下载 worker 的进度、完成、出错与取消信号。
worker 在真实 QThread 中运行，用 qtbot.waitSignal 同步等待；
每个用例结束时显式 quit+wait 保证线程干净退出（避免销毁运行中的线程）。
"""

from __future__ import annotations

import os

import pytest

pytest.importorskip("PySide6")

from PySide6.QtCore import QThread

from cloudprism.core.encryptor import Encryptor
from cloudprism.core.session import Session
from cloudprism.gui.transfer_worker import TransferWorker, start_transfer
from cloudprism.storage.local_backend import LocalFolderBackend


MASTER_PW = "transfer_test_pw"


@pytest.fixture
def session():
    return Session(MASTER_PW)


@pytest.fixture
def backend(tmp_path):
    root = tmp_path / "backend"
    root.mkdir()
    return LocalFolderBackend(root)


def _run_worker(qtbot, worker, wait_signal, timeout=120_000) -> QThread:
    """搭建线程并运行 worker，等待目标信号后确保线程干净退出。

    显式 quit+wait 而非仅依赖跨线程信号投递顺序，规避
    「销毁仍在运行的 QThread」导致的崩溃。
    """
    thread = QThread()
    worker.moveToThread(thread)
    thread.started.connect(worker.run)
    for sig in (worker.finished, worker.cancelled, worker.error):
        sig.connect(thread.quit)
    thread.start()

    with qtbot.waitSignal(wait_signal, timeout=timeout):
        pass

    # 显式停线程并等待其真正结束
    thread.quit()
    assert thread.wait(10_000), "工作线程未在期限内退出"
    return thread


class TestTransferWorkerUpload:
    """加密上传 worker。"""

    def test_upload_success(self, qtbot, session, backend, tmp_path):
        """上传：finished 信号，进度单调到 1.0，密文落盘。"""
        src = tmp_path / "data.bin"
        src.write_bytes(os.urandom(1000))

        worker = TransferWorker(
            TransferWorker.KIND_UPLOAD, session, backend,
            str(src), "data.bin.cpenc", chunk=128,
        )
        progress_values = []
        worker.progress.connect(progress_values.append)

        _run_worker(qtbot, worker, worker.finished)

        assert worker._cancel_requested is False
        assert progress_values, "应有进度信号"
        assert progress_values[-1] == pytest.approx(1.0)
        # 进度单调不减（加密 0~0.5 + 上传 0.5~1.0 加权）
        for a, b in zip(progress_values, progress_values[1:]):
            assert b >= a - 1e-9
        assert backend.exists("data.bin.cpenc")

    def test_upload_error_signal(self, qtbot, session, backend):
        """源文件不存在：error 信号。"""
        worker = TransferWorker(
            TransferWorker.KIND_UPLOAD, session, backend,
            "Z:/不存在/nope.bin", "x.cpenc",
        )
        errors = []
        worker.error.connect(errors.append)

        _run_worker(qtbot, worker, worker.error, timeout=30_000)

        assert errors, "应有 error 信号"

    def test_upload_with_kdf_salt_reuses_salt(self, qtbot, session, backend, tmp_path):
        """kdf_salt 透传：产物头内盐即传入盐（命中密钥缓存的加速路径）。"""
        from cloudprism.crypto.header import FileHeader

        vault_salt = b"\x77" * 16
        src = tmp_path / "salted.bin"
        src.write_bytes(os.urandom(300))
        worker = TransferWorker(
            TransferWorker.KIND_UPLOAD, session, backend,
            str(src), "salted.cpenc", chunk=128, kdf_salt=vault_salt,
        )

        _run_worker(qtbot, worker, worker.finished)

        head = backend.download_range("salted.cpenc", 0, 63)
        assert FileHeader.parse_bytes(head).salt == vault_salt


class TestTransferWorkerDownload:
    """下载解密 worker。"""

    def test_download_success(self, qtbot, session, backend, tmp_path):
        """下载：finished 信号，明文还原。"""
        # 先加密上传样本
        plaintext = os.urandom(500)
        src = tmp_path / "src.bin"
        src.write_bytes(plaintext)
        for _ in Encryptor(session, backend, chunk=128).encrypt_and_upload(
            str(src), "sample.cpenc"
        ):
            pass

        out = tmp_path / "out.bin"
        worker = TransferWorker(
            TransferWorker.KIND_DOWNLOAD, session, backend,
            str(out), "sample.cpenc", chunk=64,
        )

        _run_worker(qtbot, worker, worker.finished)

        assert out.read_bytes() == plaintext

    def test_download_missing_remote_error(self, qtbot, session, backend, tmp_path):
        """远端不存在：error 信号。"""
        out = tmp_path / "out.bin"
        worker = TransferWorker(
            TransferWorker.KIND_DOWNLOAD, session, backend,
            str(out), "nope.cpenc",
        )
        errors = []
        worker.error.connect(errors.append)

        _run_worker(qtbot, worker, worker.error, timeout=30_000)

        assert errors, "应有 error 信号"


class TestTransferWorkerCancel:
    """取消逻辑。"""

    def test_cancel_before_run_emits_cancelled(self, qtbot, session, backend, tmp_path):
        """运行前即取消：首个分块边界发出 cancelled。"""
        src = tmp_path / "data.bin"
        src.write_bytes(os.urandom(100))

        worker = TransferWorker(
            TransferWorker.KIND_UPLOAD, session, backend,
            str(src), "c.bin.cpenc", chunk=16,
        )
        worker.cancel()      # 预先请求取消

        _run_worker(qtbot, worker, worker.cancelled, timeout=30_000)


class TestTransferDialog:
    """进度对话框。"""

    def test_dialog_signals_wired(self, qtbot, session, backend, tmp_path):
        """start_transfer 全链路：对话框弹出并随完成自动 accept。"""
        src = tmp_path / "small.bin"
        src.write_bytes(b"x" * 50)

        dialog = start_transfer(
            TransferWorker.KIND_UPLOAD, session, backend,
            str(src), "dlg.bin.cpenc", chunk=16,
        )
        qtbot.addWidget(dialog)

        with qtbot.waitSignal(dialog.accepted, timeout=120_000):
            dialog.show()
        # 显式停线程并等待结束，避免对象回收竞态
        dialog._thread.quit()
        assert dialog._thread.wait(10_000)
        assert backend.exists("dlg.bin.cpenc")

    def test_dialog_cancel_button_requests_cancel(self, qtbot, session, backend, tmp_path):
        """取消按钮触发 worker.cancel()，cancelled 信号关闭对话框。"""
        src = tmp_path / "small.bin"
        src.write_bytes(b"x" * 50)
        dialog = start_transfer(
            TransferWorker.KIND_UPLOAD, session, backend,
            str(src), "dlg2.bin.cpenc", chunk=16,
        )
        qtbot.addWidget(dialog)
        dialog._on_cancel()       # 直接调用槽（等价点击）
        assert dialog.worker._cancel_requested is True
        # 等取消生效（对话框被 rejected 关闭）
        with qtbot.waitSignal(dialog.rejected, timeout=30_000):
            pass
        dialog._thread.quit()
        assert dialog._thread.wait(10_000)
