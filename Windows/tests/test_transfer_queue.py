"""传输队列（TransferQueue）单元测试。

以假 worker/线程替换 create_worker_thread，不触碰真实加解密与后端：
并发上限与补位、失败自动重试、手动重试、取消、聚合进度、
续传记录持久化快照与脏续传校验。
"""

from __future__ import annotations

import pytest

pytest.importorskip("PySide6")

from PySide6.QtCore import QObject, Signal

import cloudprism.core.transfer_queue as tq
from cloudprism.core.transfer_queue import TransferQueue, TransferTask


# ---------------------------------------------------------------------------
# 假 worker / 线程
# ---------------------------------------------------------------------------


class FakeWorker(QObject):
    """按脚本发射信号的假传输 worker。

    script 元素：("progress", p) / ("finished",) / ("cancelled",) /
    ("error", msg)。cancel() 立即发射 cancelled（模拟分块边界停止）。
    """

    progress = Signal(float)
    finished = Signal()
    cancelled = Signal()
    error = Signal(str)

    def __init__(self, script, parent=None) -> None:
        super().__init__(parent)
        self._script = list(script)
        self.cancel_count = 0

    def run(self) -> None:
        for item in self._script:
            if item[0] == "progress":
                self.progress.emit(item[1])
            elif item[0] == "finished":
                self.finished.emit()
            elif item[0] == "cancelled":
                self.cancelled.emit()
            elif item[0] == "error":
                self.error.emit(item[1])

    def cancel(self) -> None:
        self.cancel_count += 1
        self.cancelled.emit()


class FakeThread:
    """start() 时同步执行 worker 脚本的假线程。"""

    def __init__(self, worker) -> None:
        self._worker = worker
        self.started = False
        self.wait_count = 0

    def start(self) -> None:
        self.started = True
        self._worker.run()

    def wait(self, timeout: int | None = None) -> bool:
        """假线程同步执行完毕，等待总是立即成功（对齐 QThread.wait 签名）。"""
        self.wait_count += 1
        return True


def install_factory(monkeypatch, scripts):
    """替换 create_worker_thread；scripts 为每次创建依次消费的脚本列表。

    返回创建的 (worker, thread) 列表。
    """
    created: list = []
    counter = {"n": 0}

    def factory(direction, session, backend, local, remote, **kw):
        i = counter["n"]
        counter["n"] += 1
        script = scripts[i] if i < len(scripts) else [("finished",)]
        worker = FakeWorker(script)
        thread = FakeThread(worker)
        created.append((worker, thread))
        return worker, thread

    monkeypatch.setattr(tq, "create_worker_thread", factory)
    return created


class FakeBackend:
    """记录调用并支持大小配置的假后端。"""

    def __init__(self, size: int = 151, exists: bool = True) -> None:
        self._size = size
        self._exists = exists
        self.deleted: list[str] = []

    def get_size(self, path: str) -> int:
        return self._size

    def exists(self, path: str) -> bool:
        return self._exists

    def delete(self, path: str) -> None:
        self.deleted.append(path)


def make_queue(monkeypatch, scripts, max_concurrent=2, backend=None, tmp_path=None):
    """构建绑定假会话/后端的队列。"""
    created = install_factory(monkeypatch, scripts)
    q = TransferQueue()
    q.bind(object(), backend or FakeBackend())
    q.set_max_concurrent(max_concurrent)
    return q, created


def upload_tasks(tmp_path, n: int, size: int = 100) -> list[TransferTask]:
    """在临时目录生成 n 个真实小文件的上传任务（_prepare_task 需 getsize）。"""
    tasks = []
    for i in range(n):
        p = tmp_path / f"f{i}.bin"
        p.write_bytes(b"x" * size)
        tasks.append(
            TransferTask(
                local_path=str(p), remote_path=f"r/f{i}.cpenc", direction="upload"
            )
        )
    return tasks


# ---------------------------------------------------------------------------
# 并发上限与补位
# ---------------------------------------------------------------------------


class TestConcurrency:
    """并发上限与完成补位。"""

    def test_concurrent_limit(self, qtbot, monkeypatch, tmp_path):
        """并发 2：入队 4 任务仅启动 2 个。"""
        q, created = make_queue(
            monkeypatch, [[("progress", 0.0)]] * 4, max_concurrent=2
        )
        q.enqueue(upload_tasks(tmp_path, 4))
        assert len(created) == 2
        assert sum(1 for t in q._tasks if t.state == tq.STATE_RUNNING) == 2
        assert sum(1 for t in q._tasks if t.state == tq.STATE_WAITING) == 2

    def test_finish_fills_slot(self, qtbot, monkeypatch, tmp_path):
        """一个任务完成后自动补位启动下一个。"""
        q, created = make_queue(
            monkeypatch, [[("progress", 0.0)]] * 4, max_concurrent=2
        )
        q.enqueue(upload_tasks(tmp_path, 4))
        # 手动触发第一个运行中任务完成（模拟 worker 信号）
        running = [t for t in q._tasks if t.state == tq.STATE_RUNNING]
        q._finish_task(running[0], success=True)
        assert len(created) == 3
        assert running[0].state == tq.STATE_DONE

    def test_set_max_concurrent_starts_waiting(self, qtbot, monkeypatch, tmp_path):
        """调大并发上限立即补位。"""
        q, created = make_queue(
            monkeypatch, [[("progress", 0.0)]] * 3, max_concurrent=1
        )
        q.enqueue(upload_tasks(tmp_path, 3))
        assert len(created) == 1
        q.set_max_concurrent(3)
        assert len(created) == 3


class TestWorkerRelease:
    """终态释放回归：先等线程退出再落 worker 引用（防打包态崩溃）。"""

    def test_finish_waits_then_releases(self, qtbot, monkeypatch, tmp_path):
        """任务完成后：_running 清空、线程 wait 被调用、防回收列表同步移除。"""
        q, created = make_queue(monkeypatch, [[("finished",)]])
        q.enqueue(upload_tasks(tmp_path, 1))
        task = q._tasks[0]
        assert task.state == tq.STATE_DONE
        assert q._running == {}
        assert q._threads == []
        assert created[0][1].wait_count == 1

    def test_clear_releases_running(self, qtbot, monkeypatch, tmp_path):
        """锁库清空：运行中任务取消后等线程退出再释放。"""
        q, created = make_queue(monkeypatch, [[("progress", 0.0)]])
        q.enqueue(upload_tasks(tmp_path, 1))
        q.clear()
        assert q._running == {}
        assert q._graveyard == []
        assert created[0][0].cancel_count == 1
        assert created[0][1].wait_count == 1


# ---------------------------------------------------------------------------
# 重试
# ---------------------------------------------------------------------------


class TestRetry:
    """自动重试一次 + 手动重试。"""

    def test_auto_retry_once_then_succeed(self, qtbot, monkeypatch, tmp_path):
        """首次失败自动重试一次后成功：state=done，retries=1。"""
        q, created = make_queue(
            monkeypatch, [[("error", "网络抖动")], [("finished",)]]
        )
        q.enqueue(upload_tasks(tmp_path, 1))
        task = q._tasks[0]
        assert task.state == tq.STATE_DONE
        assert task.retries == 1

    def test_auto_retry_exhausted_fails(self, qtbot, monkeypatch, tmp_path):
        """连续失败超出自动重试次数：置 failed 并记录错误。"""
        q, created = make_queue(
            monkeypatch, [[("error", "第一次")], [("error", "第二次")]]
        )
        q.enqueue(upload_tasks(tmp_path, 1))
        task = q._tasks[0]
        assert task.state == tq.STATE_FAILED
        assert task.error_msg == "第二次"
        assert task.retries == tq.AUTO_RETRIES

    def test_manual_retry_failed_task(self, qtbot, monkeypatch, tmp_path):
        """界面手动重试：重置状态后重新入队执行。"""
        q, created = make_queue(
            monkeypatch,
            [[("error", "e")], [("error", "e")], [("finished",)]],
        )
        q.enqueue(upload_tasks(tmp_path, 1))
        task = q._tasks[0]
        assert task.state == tq.STATE_FAILED
        q.retry(task)
        assert task.state == tq.STATE_DONE
        assert task.retries == 0
        assert len(created) == 3

    def test_retry_ignores_running_task(self, qtbot, monkeypatch, tmp_path):
        """非终态任务调用 retry 无效。"""
        q, created = make_queue(monkeypatch, [[("progress", 0.0)]])
        q.enqueue(upload_tasks(tmp_path, 1))
        task = q._tasks[0]
        assert task.state == tq.STATE_RUNNING
        q.retry(task)
        assert task.state == tq.STATE_RUNNING
        assert len(created) == 1


# ---------------------------------------------------------------------------
# 取消
# ---------------------------------------------------------------------------


class TestCancel:
    """cancel_all：等待中直接标记，运行中经 worker.cancel。"""

    def test_cancel_all(self, qtbot, monkeypatch, tmp_path):
        q, created = make_queue(
            monkeypatch, [[("progress", 0.0)]] * 3, max_concurrent=1
        )
        q.enqueue(upload_tasks(tmp_path, 3))
        finished_events: list[tuple] = []
        q.taskFinished.connect(lambda t, ok: finished_events.append((t, ok)))

        q.cancel_all()
        # 运行中的被 worker.cancel()（假 worker 立即发 cancelled）
        assert created[0][0].cancel_count == 1
        assert q._tasks[0].state == tq.STATE_CANCELLED
        # 等待中的直接标记取消且发终态信号
        assert q._tasks[1].state == tq.STATE_CANCELLED
        assert q._tasks[2].state == tq.STATE_CANCELLED
        assert all(ok is False for _t, ok in finished_events)
        assert q.is_idle

    def test_clear_resets_silently(self, qtbot, monkeypatch, tmp_path):
        """锁库清空：任务与线程记录归零，队列回到空闲。"""
        q, created = make_queue(monkeypatch, [[("progress", 0.0)]])
        q.enqueue(upload_tasks(tmp_path, 1))
        q.clear()
        assert q._tasks == []
        assert q._running == {}
        assert q.is_idle


# ---------------------------------------------------------------------------
# 聚合进度与状态查询
# ---------------------------------------------------------------------------


class TestAggregate:
    """字节级聚合进度。"""

    def test_aggregate_after_all_done(self, qtbot, monkeypatch, tmp_path):
        q, created = make_queue(
            monkeypatch, [[("finished",)]] * 2, max_concurrent=1
        )
        values: list[tuple] = []
        q.aggregateProgress.connect(lambda d, t: values.append((d, t)))
        q.enqueue(upload_tasks(tmp_path, 2, size=100))
        # 全部完成：完成字节 = 总字节 = 200
        assert values[-1] == (200, 200)

    def test_aggregate_partial_progress(self, qtbot, monkeypatch, tmp_path):
        q, created = make_queue(
            monkeypatch, [[("progress", 0.5)]] * 2, max_concurrent=1
        )
        values: list[tuple] = []
        q.aggregateProgress.connect(lambda d, t: values.append((d, t)))
        q.enqueue(upload_tasks(tmp_path, 2, size=100))
        # 仅第一个跑到 50%，第二个等待中但已预计入总字节：(50, 200)
        assert values[-1] == (50, 200)

    def test_download_total_from_remote_size(self, qtbot, monkeypatch, tmp_path):
        """下载任务总字节 = 远端大小 - 加密头估算。"""
        q, created = make_queue(monkeypatch, [[("progress", 0.0)]])
        task = TransferTask(
            local_path=str(tmp_path / "out.bin"),
            remote_path="r/f.cpenc",
            direction="download",
        )
        q.enqueue([task])
        assert task.total_bytes == 151 - 51

    def test_unfinished_tasks_snapshot(self, qtbot, monkeypatch, tmp_path):
        q, created = make_queue(
            monkeypatch, [[("finished",)], [("progress", 0.0)]]
        )
        q.enqueue(upload_tasks(tmp_path, 2))
        unfinished = q.unfinished_tasks()
        assert len(unfinished) == 1
        assert unfinished[0].state == tq.STATE_RUNNING
        assert q.has_active


# ---------------------------------------------------------------------------
# 脏续传校验
# ---------------------------------------------------------------------------


class TestResumeVerify:
    """携带续传记录的上传任务启动前校验本地源文件。"""

    def test_dirty_source_deletes_remote_partial(
        self, qtbot, monkeypatch, tmp_path
    ):
        """源文件大小与记录不符：删除远端半成品后整传。"""
        backend = FakeBackend(exists=True)
        q, created = make_queue(
            monkeypatch, [[("finished",)]], backend=backend
        )
        p = tmp_path / "src.bin"
        p.write_bytes(b"x" * 100)
        task = TransferTask(
            local_path=str(p),
            remote_path="r/src.cpenc",
            direction="upload",
            expected_size=999,  # 与实际不符 → 脏续传
            expected_mtime=12345.0,
        )
        q.enqueue([task])
        assert backend.deleted == ["r/src.cpenc"]
        assert task.state == tq.STATE_DONE

    def test_clean_source_keeps_remote(self, qtbot, monkeypatch, tmp_path):
        """源文件与记录一致：不删除远端（按已有字节续传）。"""
        import os

        backend = FakeBackend(exists=True)
        q, created = make_queue(
            monkeypatch, [[("finished",)]], backend=backend
        )
        p = tmp_path / "src.bin"
        p.write_bytes(b"x" * 100)
        st = os.stat(str(p))
        task = TransferTask(
            local_path=str(p),
            remote_path="r/src.cpenc",
            direction="upload",
            expected_size=st.st_size,
            expected_mtime=st.st_mtime,
        )
        q.enqueue([task])
        assert backend.deleted == []
        assert task.state == tq.STATE_DONE
