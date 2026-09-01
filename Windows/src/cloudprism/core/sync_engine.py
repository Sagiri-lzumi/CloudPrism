"""增量同步引擎（首期：本地 → 云端单向增量）。

核心思路：
  - 索引文件 ``.cloudprism_index`` 存放于密库根（或子目录密库位置），
    内容为 JSON ``{相对路径: {size, mtime, uploaded_at}}``，经
    Encryptor 整体加密为 .cpenc 容器上传 —— 文件名加密开关开或关
    均可按固定文件名定位索引。
  - ``plan()`` 对比本地文件 size+mtime 与索引：新文件 / 已变更 / 未变。
  - ``build_tasks()`` 把需同步文件转为统一传输队列任务（进度走聚合）。
  - ``update_index()`` 在传输完成后整体重写索引（先删后传，规避后端
    分块上传的续传语义）。

首期边界：
  - 仅本地 → 云端单向增量；本地删除不传播到云端；
  - 云端被其他设备修改的冲突检测留给双向同步期；
  - 索引解析失败视为空索引（全量重扫），不阻断同步。
"""

from __future__ import annotations

import json
import os
import time
from dataclasses import dataclass, field

from cloudprism import constants
from cloudprism.core.decryptor import Decryptor
from cloudprism.core.encryptor import Encryptor
from cloudprism.core.session import Session
from cloudprism.core.transfer_queue import TransferTask
from cloudprism.crypto.filename import FilenameCipher
from cloudprism.storage.backend import StorageBackend

# mtime 比较容差（秒）：文件系统时间戳精度差异不视为变更
MTIME_TOLERANCE = 1.0


@dataclass
class SyncPlan:
    """一次同步计划：本地目录与索引对比的三态结果。"""

    local_dir: str
    new: list[dict] = field(default_factory=list)          # 索引中不存在
    changed: list[dict] = field(default_factory=list)      # size/mtime 变化
    unchanged: list[dict] = field(default_factory=list)    # 与索引一致

    @property
    def pending_count(self) -> int:
        """需要同步上传的文件数。"""
        return len(self.new) + len(self.changed)


class SyncEngine:
    """本地目录 → 云端密库的单向增量同步。

    参数:
        session: 主密码会话（派生数据密钥与文件名密钥）
        backend: 存储后端
        vault_path: 密库位置（空=后端根目录；否则为子目录名）
        filename_enc: 密库是否开启文件名加密
        name_salt: 文件名加密密钥盐（= 密库元信息 salt）；
            filename_enc 为 True 时必传
    """

    def __init__(
        self,
        session: Session,
        backend: StorageBackend,
        vault_path: str = "",
        filename_enc: bool = False,
        name_salt: bytes | None = None,
    ) -> None:
        self.session = session
        self.backend = backend
        self._vault_path = (vault_path or "").strip("/")
        self._filename_enc = filename_enc
        self._name_salt = name_salt

    # ------------------------------------------------------------------
    # 路径处理
    # ------------------------------------------------------------------

    def _remote_join(self, rel_remote: str) -> str:
        """密库内相对路径 -> 后端完整相对路径（含子目录密库前缀）。"""
        if self._vault_path:
            return f"{self._vault_path}/{rel_remote}"
        return rel_remote

    def _enc_name(self, name: str) -> str:
        """文件名加密开启时加密单个路径段。"""
        if not self._filename_enc:
            return name
        key = self.session.derive_key(self._name_salt)
        return FilenameCipher.encrypt(name, key)

    def remote_path(self, rel_path: str) -> str:
        """本地相对路径 -> 后端加密容器路径。

        逐段处理文件名加密，末段追加 .cpenc 扩展名。
        """
        segs = rel_path.replace("\\", "/").split("/")
        enc_segs = [self._enc_name(s) for s in segs if s]
        enc_segs[-1] += constants.FILE_EXTENSION
        return self._remote_join("/".join(enc_segs))

    def _index_remote_path(self) -> str:
        """索引文件的后端路径（固定名，不参与文件名加密）。"""
        return self._remote_join(constants.SYNC_INDEX_NAME)

    # ------------------------------------------------------------------
    # 索引读写
    # ------------------------------------------------------------------

    def load_index(self) -> dict:
        """读取并解密索引；不存在或损坏时返回空索引（全量重扫）。"""
        path = self._index_remote_path()
        try:
            if not self.backend.exists(path):
                return {}
            raw = Decryptor(self.session, self.backend).decrypt_range_to_bytes(path)
            data = json.loads(raw.decode("utf-8"))
            return data if isinstance(data, dict) else {}
        except Exception:  # noqa: BLE001
            # 索引损坏不阻断同步：视为空索引
            return {}

    def update_index(self, entries: dict) -> None:
        """合并上传成功的条目并重写索引（整体加密，先删后传）。

        参数:
            entries: {rel_path: {size, mtime}}；uploaded_at 由本方法补记
        """
        index = self.load_index()
        now = time.time()
        for rel, info in entries.items():
            index[rel] = {
                "size": info["size"],
                "mtime": info["mtime"],
                "uploaded_at": now,
            }

        payload = json.dumps(index, ensure_ascii=False).encode("utf-8")
        path = self._index_remote_path()

        import tempfile

        from cloudprism.core.paths import temp_dir

        # 临时文件落产品自管的 data/tmp/（部分环境 %TEMP% ACL 不完整）
        fd, tmp_path = tempfile.mkstemp(suffix=".cpidx", dir=temp_dir(create=True))
        try:
            with os.fdopen(fd, "wb") as tmp:
                tmp.write(payload)
            encrypted = Encryptor(self.session, self.backend).encrypt_to_bytes(
                tmp_path
            )
        finally:
            try:
                os.unlink(tmp_path)
            except OSError:
                pass

        # 先删后传：规避后端分块上传的断点续传语义（追加而非覆盖）
        fd2, tmp_enc = tempfile.mkstemp(suffix=".cpenc", dir=temp_dir(create=True))
        try:
            with os.fdopen(fd2, "wb") as tmp:
                tmp.write(encrypted)
            if self.backend.exists(path):
                self.backend.delete(path)
            for _ in self.backend.upload_chunked(tmp_enc, path):
                pass
        finally:
            try:
                os.unlink(tmp_enc)
            except OSError:
                pass

    # ------------------------------------------------------------------
    # 计划与任务
    # ------------------------------------------------------------------

    def plan(self, local_dir: str) -> SyncPlan:
        """扫描本地目录，对比索引生成三态同步计划。

        判定规则：
          - 索引无该相对路径 -> new
          - size 相同且 |mtime 差| <= 容差 -> unchanged
          - 其余 -> changed
        """
        local_dir = os.path.abspath(local_dir)
        index = self.load_index()
        result = SyncPlan(local_dir=local_dir)

        for dirpath, _dirs, files in os.walk(local_dir):
            for fname in files:
                local_path = os.path.join(dirpath, fname)
                rel_path = os.path.relpath(local_path, local_dir)
                rel_path = rel_path.replace(os.sep, "/")
                try:
                    st = os.stat(local_path)
                except OSError:
                    continue  # 扫描期间被删除等
                entry = {
                    "rel_path": rel_path,
                    "local_path": local_path,
                    "size": st.st_size,
                    "mtime": st.st_mtime,
                }
                old = index.get(rel_path)
                if old is None:
                    result.new.append(entry)
                elif (
                    old.get("size") == st.st_size
                    and abs(float(old.get("mtime", 0.0)) - st.st_mtime)
                    <= MTIME_TOLERANCE
                ):
                    result.unchanged.append(entry)
                else:
                    result.changed.append(entry)
        return result

    def build_tasks(self, plan: SyncPlan) -> list[TransferTask]:
        """把计划中的新增/变更文件转为统一队列上传任务。

        任务携带 expected_size/mtime 快照（脏续传校验用），
        display_name 为相对路径（子目录层级可见）。
        """
        tasks: list[TransferTask] = []
        for entry in list(plan.new) + list(plan.changed):
            task = TransferTask(
                local_path=entry["local_path"],
                remote_path=self.remote_path(entry["rel_path"]),
                display_name=entry["rel_path"],
                direction="upload",
                expected_size=entry["size"],
                expected_mtime=entry["mtime"],
            )
            tasks.append(task)
        return tasks
