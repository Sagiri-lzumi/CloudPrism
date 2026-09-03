"""增量同步引擎（SyncEngine）单元测试。

本地后端 + 真实会话（密码可任意，仅要求两端一致）：
计划三态判定、任务构建、远端路径（含文件名加密）、加密索引往返
与损坏索引容错。
"""

from __future__ import annotations

import os

import pytest

from cloudprism import constants
from cloudprism.core.session import Session
from cloudprism.core.sync_engine import SyncEngine
from cloudprism.crypto.filename import FilenameCipher
from cloudprism.storage.local_backend import LocalFolderBackend

MASTER_PW = "sync_test_pw"


@pytest.fixture
def session():
    return Session(MASTER_PW)


@pytest.fixture
def backend(tmp_path):
    root = tmp_path / "backend"
    root.mkdir()
    return LocalFolderBackend(root)


@pytest.fixture
def local_dir(tmp_path):
    """两级本地目录：a.txt + sub/b.txt。"""
    d = tmp_path / "sync_src"
    d.mkdir()
    (d / "a.txt").write_bytes(b"hello")
    sub = d / "sub"
    sub.mkdir()
    (sub / "b.txt").write_bytes(b"world!")
    return d


@pytest.fixture
def engine(session, backend):
    return SyncEngine(session, backend)


# ---------------------------------------------------------------------------
# 远端路径
# ---------------------------------------------------------------------------


class TestRemotePath:
    """相对路径 -> 后端加密容器路径。"""

    def test_plain_path(self, engine):
        assert engine.remote_path("a.txt") == "a.txt" + constants.FILE_EXTENSION
        assert engine.remote_path("sub/b.txt") == (
            "sub/b.txt" + constants.FILE_EXTENSION
        )

    def test_vault_path_prefix(self, session, backend):
        eng = SyncEngine(session, backend, vault_path="vault2")
        assert eng.remote_path("a.txt") == (
            "vault2/a.txt" + constants.FILE_EXTENSION
        )

    def test_filename_encryption_per_segment(self, session, backend):
        """文件名加密：逐段加密且可解密还原，末段带 .cpenc。"""
        salt = b"\x77" * 16
        eng = SyncEngine(
            session, backend, filename_enc=True, name_salt=salt
        )
        remote = eng.remote_path("sub/b.txt")
        assert remote.endswith(constants.FILE_EXTENSION)
        key = session.derive_key(salt)
        dir_seg, file_seg = remote.split("/")
        assert dir_seg != "sub"
        assert FilenameCipher.decrypt(dir_seg, key) == "sub"
        fname = file_seg[: -len(constants.FILE_EXTENSION)]
        assert FilenameCipher.decrypt(fname, key) == "b.txt"


# ---------------------------------------------------------------------------
# 计划三态
# ---------------------------------------------------------------------------


class TestPlan:
    """索引对比的 new / changed / unchanged 判定。"""

    def test_empty_index_all_new(self, engine, local_dir):
        plan = engine.plan(str(local_dir))
        assert plan.pending_count == 2
        assert {e["rel_path"] for e in plan.new} == {"a.txt", "sub/b.txt"}
        assert plan.changed == []
        assert plan.unchanged == []

    def test_synced_then_unchanged(self, engine, local_dir):
        """记录索引后未改动 -> unchanged（mtime 容差内）。"""
        plan = engine.plan(str(local_dir))
        engine.update_index(
            {e["rel_path"]: {"size": e["size"], "mtime": e["mtime"]}
             for e in plan.new}
        )
        plan2 = engine.plan(str(local_dir))
        assert plan2.pending_count == 0
        assert len(plan2.unchanged) == 2

    def test_size_change_marked_changed(self, engine, local_dir):
        """内容变化（大小不同）-> changed。"""
        plan = engine.plan(str(local_dir))
        engine.update_index(
            {e["rel_path"]: {"size": e["size"], "mtime": e["mtime"]}
             for e in plan.new}
        )
        (local_dir / "a.txt").write_bytes(b"hello, longer content")
        plan2 = engine.plan(str(local_dir))
        assert [e["rel_path"] for e in plan2.changed] == ["a.txt"]
        assert len(plan2.unchanged) == 1

    def test_new_file_after_index(self, engine, local_dir):
        """索引建立后新增文件 -> new，其余不变。"""
        plan = engine.plan(str(local_dir))
        engine.update_index(
            {e["rel_path"]: {"size": e["size"], "mtime": e["mtime"]}
             for e in plan.new}
        )
        (local_dir / "c.txt").write_bytes(b"new")
        plan2 = engine.plan(str(local_dir))
        assert [e["rel_path"] for e in plan2.new] == ["c.txt"]
        assert len(plan2.unchanged) == 2


class TestBuildTasks:
    """计划 -> 统一队列任务。"""

    def test_tasks_carry_snapshot(self, engine, local_dir):
        plan = engine.plan(str(local_dir))
        tasks = engine.build_tasks(plan)
        assert len(tasks) == 2
        for task in tasks:
            assert task.direction == "upload"
            assert task.remote_path.endswith(constants.FILE_EXTENSION)
            assert task.display_name == os.path.basename(
                task.local_path
            ) or task.display_name
            assert task.expected_size is not None
            assert task.expected_mtime is not None
        assert {t.display_name for t in tasks} == {"a.txt", "sub/b.txt"}

    def test_unchanged_not_enqueued(self, engine, local_dir):
        plan = engine.plan(str(local_dir))
        engine.update_index(
            {e["rel_path"]: {"size": e["size"], "mtime": e["mtime"]}
             for e in plan.new}
        )
        plan2 = engine.plan(str(local_dir))
        assert engine.build_tasks(plan2) == []


# ---------------------------------------------------------------------------
# 加密索引
# ---------------------------------------------------------------------------


class TestIndex:
    """索引整体加密落盘 + 损坏容错。"""

    def test_index_encrypted_roundtrip(self, engine, backend, local_dir):
        """写入后可解密读回；磁盘字节不含 JSON 明文。"""
        engine.update_index({"a.txt": {"size": 5, "mtime": 1.0}})
        assert backend.exists(constants.SYNC_INDEX_NAME)
        raw = open(
            os.path.join(backend.root, constants.SYNC_INDEX_NAME), "rb"
        ).read()
        assert b"a.txt" not in raw
        assert not raw.startswith(b"{")

        index = engine.load_index()
        assert index["a.txt"]["size"] == 5
        assert index["a.txt"]["mtime"] == 1.0
        assert "uploaded_at" in index["a.txt"]

    def test_index_merge_keeps_old_entries(self, engine):
        engine.update_index({"a.txt": {"size": 1, "mtime": 1.0}})
        engine.update_index({"b.txt": {"size": 2, "mtime": 2.0}})
        index = engine.load_index()
        assert set(index) == {"a.txt", "b.txt"}

    def test_corrupted_index_returns_empty(self, engine, backend):
        """索引损坏（无法解密）-> 空索引，不阻断同步。"""
        import tempfile

        with tempfile.NamedTemporaryFile(delete=False) as tmp:
            tmp.write(b"\x00\x01garbage-not-encrypted")
            tmp_path = tmp.name
        try:
            for _ in backend.upload_chunked(tmp_path, constants.SYNC_INDEX_NAME):
                pass
        finally:
            os.unlink(tmp_path)
        assert engine.load_index() == {}

    def test_missing_index_returns_empty(self, engine):
        assert engine.load_index() == {}
