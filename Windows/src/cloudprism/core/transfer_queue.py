"""传输任务队列：并发调度、聚合进度与自动重试。

TransferQueue 取代旧版串行队列（app.py 内的 _transfer_queue 手工调度），
提供任务级并行：同时最多运行 max_concurrent 个任务，完成后自动补位。

  - TransferTask：单个传输任务的完整状态（进度/字节数/重试/错误）
  - TransferQueue（QObject）：enqueue 入队即调度；失败自动重试 1 次，
    仍失败置 failed 供界面手动重试；信号按字节聚合全局进度

断点续传一致性：来自持久化记录的任务（携带 expected_size/expected_mtime）
在启动上传前校验本地源文件未变化，变化则先删除远端半成品再整传，
避免基于脏偏移续传出错文件（三期"先删后传"教训的延续）。
"""

from __future__ import annotations

import os
from dataclasses import dataclass

from PySide6.QtCore import QObject, Signal

from cloudprism.core.session import Session
from cloudprism.gui.transfer_worker import create_worker_thread
from cloudprism.storage.backend import StorageBackend

# 加密文件头估算长度（magic 8 + version 4 + hl 4 + saltlen 1 + salt 16
# + ivlen 1 + iv 16 + flags 1 = 51）；下载聚合进度用它从远端大小估算明文大小
_HEADER_ESTIMATE = 51

# 任务状态机
STATE_WAITING = "waiting"
STATE_RUNNING = "running"
STATE_DONE = "done"
STATE_FAILED = "failed"
STATE_CANCELLED = "cancelled"

# 失败后自动重试次数（网络容错；超出置 failed 供手动重试）
AUTO_RETRIES = 1


@dataclass(eq=False)
class TransferTask:
    """单个传输任务的状态载体。

    以对象身份比较/哈希（eq=False）：任务对象常被用作字典键
    （界面条目映射、同步批次跟踪），且按值比较无业务意义。

    expected_size/expected_mtime 为入队时的本地源文件快照，仅由
    续传记录重建的任务携带；非 None 时启动前做脏续传校验。
    """

    local_path: str
    remote_path: str
    display_name: str = ""
    direction: str = "upload"  # "upload" / "download"
    state: str = STATE_WAITING
    progress: float = 0.0
    total_bytes: int = 0
    done_bytes: int = 0
    retries: int = 0
    error_msg: str = ""
    expected_size: int | None = None
    expected_mtime: float | None = None

    def __post_init__(self) -> None:
        if not self.display_name:
            self.display_name = os.path.basename(self.local_path)


class TransferQueue(QObject):
    """并发传输队列（任务级并行 + 字节级聚合进度）。"""

    # 单任务进度更新
    taskProgress = Signal(object)
    # 单任务终态（成功/失败/取消都发出，success 表示是否成功）
    taskFinished = Signal(object, bool)
    # 全局聚合进度（已完成字节, 总字节）
    aggregateProgress = Signal(int, int)

    def __init__(self, parent: QObject | None = None) -> None:
        super().__init__(parent)
        self.session: Session | None = None
        self.backend: StorageBackend | None = None

        # 传输参数（由控制器在入队前从设置页同步）
        self.chunk: int = 1 << 20
        self.max_workers: int = 1

        self.max_concurrent: int = 2

        self._tasks: list[TransferTask] = []          # 本会话全部任务
        self._running: dict[TransferTask, object] = {}  # task -> worker
        self._threads: list = []                      # 防回收引用

    # ------------------------------------------------------------------
    # 连接与参数
    # ------------------------------------------------------------------

    def bind(self, session: Session, backend: StorageBackend) -> None:
        """绑定一次成功连接（连接密库后调用）。"""
        self.session = session
        self.backend = backend

    def set_transfer_options(self, chunk: int, max_workers: int) -> None:
        """同步设置页的分块大小与并行加密核数。"""
        self.chunk = chunk
        self.max_workers = max_workers

    def set_max_concurrent(self, n: int) -> None:
        """调整并发上限；调大时立即补位启动等待中的任务。"""
        self.max_concurrent = max(1, int(n))
        self._pump()

    # ------------------------------------------------------------------
    # 入队与调度
    # ------------------------------------------------------------------

    def enqueue(self, tasks: list[TransferTask]) -> None:
        """入队一批任务并立即调度。"""
        if not tasks:
            return
        self._tasks.extend(tasks)
        self._precount_bytes(tasks)
        self._emit_aggregate()
        self._pump()

    @staticmethod
    def _precount_bytes(tasks: list[TransferTask]) -> None:
        """预统计上传任务总字节：等待中任务也计入聚合进度，
        避免进度条在任务启动时跳变（下载任务启动时按远端大小估算）。"""
        for t in tasks:
            if t.direction == "upload" and t.total_bytes == 0:
                try:
                    t.total_bytes = os.path.getsize(t.local_path)
                except OSError:
                    pass  # 启动时 _prepare_task 会再次统计并报失败

    def retry(self, task: TransferTask) -> None:
        """手动重试失败/取消的任务（重置状态重新入队）。"""
        if task.state not in (STATE_FAILED, STATE_CANCELLED):
            return
        task.state = STATE_WAITING
        task.progress = 0.0
        task.done_bytes = 0
        task.retries = 0
        task.error_msg = ""
        self._emit_aggregate()
        self._pump()

    def cancel_all(self) -> None:
        """取消全部任务：运行中的在分块边界停止，等待中的直接标记。"""
        for task in list(self._tasks):
            if task.state == STATE_WAITING:
                task.state = STATE_CANCELLED
                self.taskFinished.emit(task, False)
        for worker in list(self._running.values()):
            worker.cancel()
        self._emit_aggregate()

    def clear(self) -> None:
        """锁库时调用：静默停止全部任务并清空状态（不发信号）。"""
        for worker in list(self._running.values()):
            worker.cancel()
        self._tasks.clear()
        self._running.clear()
        self._threads.clear()

    # ------------------------------------------------------------------
    # 状态查询
    # ------------------------------------------------------------------

    @property
    def has_active(self) -> bool:
        """是否有运行中或等待中的任务（自动锁定豁免用）。"""
        if self._running:
            return True
        return any(t.state == STATE_WAITING for t in self._tasks)

    @property
    def is_idle(self) -> bool:
        return not self.has_active

    def unfinished_tasks(self) -> list[TransferTask]:
        """未完成的任务快照（续传记录持久化用）。"""
        return [
            t for t in self._tasks
            if t.state in (STATE_WAITING, STATE_RUNNING)
        ]

    # ------------------------------------------------------------------
    # 内部调度
    # ------------------------------------------------------------------

    def _pump(self) -> None:
        """补位调度：并发未满时启动等待中的任务。"""
        if self.session is None or self.backend is None:
            return
        for task in self._tasks:
            if len(self._running) >= self.max_concurrent:
                break
            if task.state != STATE_WAITING:
                continue
            self._start_task(task)

    def _start_task(self, task: TransferTask) -> None:
        """启动单个任务：准备大小/续传校验 -> 创建 worker 线程。"""
        try:
            self._prepare_task(task)
        except Exception as e:  # noqa: BLE001
            task.state = STATE_FAILED
            task.error_msg = str(e)
            self.taskFinished.emit(task, False)
            self._emit_aggregate()
            return

        task.state = STATE_RUNNING
        worker, thread = create_worker_thread(
            task.direction, self.session, self.backend,
            task.local_path, task.remote_path,
            chunk=self.chunk, parent=self, max_workers=self.max_workers,
        )
        worker.progress.connect(lambda p, t=task: self._on_progress(t, p))
        worker.finished.connect(lambda t=task: self._on_done(t))
        worker.cancelled.connect(lambda t=task: self._on_cancelled(t))
        worker.error.connect(lambda msg, t=task: self._on_error(t, msg))

        self._running[task] = worker
        self._threads.append(thread)
        thread.start()

    def _prepare_task(self, task: TransferTask) -> None:
        """启动前准备：统计总字节 + 脏续传校验。

        上传：本地文件大小；下载：远端大小减去加密头估算。
        携带续传记录（expected_* 非 None）的上传任务：本地源文件
        size/mtime 与记录不一致时先删除远端半成品，防止脏续传。
        """
        if task.direction == "upload":
            task.total_bytes = os.path.getsize(task.local_path)
            if task.expected_size is not None:
                self._verify_resume_source(task)
        else:
            try:
                remote_size = self.backend.get_size(task.remote_path)
                task.total_bytes = max(0, remote_size - _HEADER_ESTIMATE)
            except Exception:  # noqa: BLE001
                task.total_bytes = 0

    def _verify_resume_source(self, task: TransferTask) -> None:
        """校验续传任务的本地源文件未变化；变化则删除远端半成品。"""
        try:
            st = os.stat(task.local_path)
            source_ok = (
                st.st_size == task.expected_size
                and abs(st.st_mtime - (task.expected_mtime or 0.0)) < 1.0
            )
        except OSError:
            source_ok = False

        if source_ok:
            return
        # 源文件已变：远端半成品基于旧内容，删除后整传
        try:
            if self.backend.exists(task.remote_path):
                remote_size = self.backend.get_size(task.remote_path)
                if 0 < remote_size <= task.total_bytes + _HEADER_ESTIMATE + 16:
                    self.backend.delete(task.remote_path)
        except Exception:  # noqa: BLE001
            pass  # 删除失败不阻断：上传按已有字节续传最多浪费带宽

    # ------------------------------------------------------------------
    # worker 信号处理
    # ------------------------------------------------------------------

    def _on_progress(self, task: TransferTask, p: float) -> None:
        task.progress = p
        task.done_bytes = int(p * task.total_bytes)
        self.taskProgress.emit(task)
        self._emit_aggregate()

    def _on_done(self, task: TransferTask) -> None:
        self._finish_task(task, success=True)

    def _on_cancelled(self, task: TransferTask) -> None:
        self._running.pop(task, None)
        task.state = STATE_CANCELLED
        self.taskFinished.emit(task, False)
        self._emit_aggregate()
        self._pump()

    def _on_error(self, task: TransferTask, msg: str) -> None:
        self._running.pop(task, None)
        if task.retries < AUTO_RETRIES:
            # 自动重试一次（网络抖动容错）
            task.retries += 1
            task.state = STATE_WAITING
            task.progress = 0.0
            task.done_bytes = 0
            self._emit_aggregate()
            self._pump()
            return
        task.state = STATE_FAILED
        task.error_msg = msg
        self.taskFinished.emit(task, False)
        self._emit_aggregate()
        self._pump()

    def _finish_task(self, task: TransferTask, success: bool) -> None:
        self._running.pop(task, None)
        if success:
            task.state = STATE_DONE
            task.progress = 1.0
            task.done_bytes = task.total_bytes
        self.taskFinished.emit(task, success)
        self._emit_aggregate()
        self._pump()

    # ------------------------------------------------------------------
    # 聚合进度
    # ------------------------------------------------------------------

    def _emit_aggregate(self) -> None:
        """按字节聚合全部任务进度（完成的任务计满额）。"""
        done = 0
        total = 0
        for t in self._tasks:
            total += t.total_bytes
            if t.state == STATE_DONE:
                done += t.total_bytes
            elif t.state in (STATE_RUNNING, STATE_WAITING, STATE_FAILED):
                done += t.done_bytes
        self.aggregateProgress.emit(done, total)
