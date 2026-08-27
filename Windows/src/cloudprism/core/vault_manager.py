"""Mi库管理器：新建 / 连接 / 重命名Mi库的核心流程（非 GUI）。

- 新建：生成 VaultMetadata（随机 vault_id/salt/iv）-> 创建 Vault Marker ->
  上传到后端根目录（可同时携带用户自定义名称）
- 连接：从后端根目录下载 Vault Marker -> 校验主密码 -> 返回元信息
- 重命名：校验主密码后用新名称重新加密 Marker 并覆盖上传，
  名称随文件保存，跨设备跟随密库

InitWizard 与后续 CLI 场景复用本模块；GUI 不直接操作 VaultMarker。
"""

from __future__ import annotations

import base64
import dataclasses
import os
from typing import Callable

from cloudprism import constants
from cloudprism.crypto.vault import VaultMarker, VaultMetadata
from cloudprism.storage.backend import StorageBackend


class VaultError(Exception):
    """Mi库操作异常。"""


def _report(progress_cb: Callable[[str], None] | None, msg: str) -> None:
    """阶段进度回调（可为 None）；异常不阻断主流程，进度仅展示用。"""
    if progress_cb is None:
        return
    try:
        progress_cb(msg)
    except Exception:  # noqa: BLE001
        pass


class VaultManager:
    """Mi库生命周期管理。"""

    def __init__(self, backend: StorageBackend) -> None:
        self.backend = backend
        # 凭恢复码开库成功时暂存还原出的主密码（仅内存，供调用方构建会话）
        self.recovered_password: str | None = None

    # ------------------------------------------------------------------
    # 查找与判断
    # ------------------------------------------------------------------

    def has_vault(self, vault_path: str = "") -> bool:
        """指定位置（根目录或子目录）是否存在 Vault Marker。"""
        return self.backend.exists(self._marker_path(vault_path))

    @staticmethod
    def _marker_path(vault_path: str = "") -> str:
        """Marker 完整路径：根密库为根目录，附加密库在对应子目录下。"""
        vault_path = (vault_path or "").strip("/")
        return (
            f"{vault_path}/{constants.VAULT_MARKER_NAME}"
            if vault_path else constants.VAULT_MARKER_NAME
        )

    def _download_marker(self, vault_path: str = "") -> bytes | None:
        """下载 Vault Marker 内容；不存在返回 None。"""
        path = self._marker_path(vault_path)
        if not self.backend.exists(path):
            return None
        size = self.backend.get_size(path)
        # marker 很小（约 101 字节），整读
        return self.backend.download_range(path, 0, max(size - 1, 0))

    # ------------------------------------------------------------------
    # 多密库扫描（零协议变更：子目录 Marker 探测）
    # ------------------------------------------------------------------

    def list_vaults(self) -> list[dict]:
        """扫描后端上的全部密库（根目录 + 一级子目录）。

        返回 [{path, vault_id}]：根目录有 Marker 时主密库 path=""；
        一级子目录含 Marker 的为附加密库（path=子目录名）。
        密库名称需打开后才能解密获得，列表先显示路径。
        """
        vaults: list[dict] = []
        if self.backend.exists(constants.VAULT_MARKER_NAME):
            vaults.append({"path": "", "vault_id": None})
        try:
            entries = self.backend.list_dir("")
        except Exception:  # noqa: BLE001
            return vaults
        for e in entries:
            if not e.is_dir:
                continue
            if self.backend.exists(self._marker_path(e.name)):
                vaults.append({"path": e.name, "vault_id": None})
        return vaults

    # ------------------------------------------------------------------
    # 新建
    # ------------------------------------------------------------------

    def create_vault(
        self,
        master_password: str,
        filename_enc: bool,
        name: str = "",
        vault_path: str = "",
    ) -> VaultMetadata:
        """新建Mi库：生成并上传 Vault Marker。

        参数:
            master_password: 用户主密码
            filename_enc: 文件名加密开关（初始化后不可变更）
            name: 用户自定义密库名称（可选，随 Marker 加密保存）
            vault_path: 密库位置（空=后端根目录；否则为子目录名）

        返回:
            VaultMetadata

        异常:
            VaultError: 目标位置已存在Mi库（防止覆盖）
        """
        if self.has_vault(vault_path):
            raise VaultError("该位置已存在Mi库，请选择「连接」或更换位置")

        if vault_path.strip("/"):
            # 子目录密库：确保目录存在（已存在时容错）
            try:
                self.backend.mkdir(vault_path.strip("/"))
            except Exception:  # noqa: BLE001
                pass

        meta = VaultMarker.generate_metadata(filename_enc=filename_enc, name=name)
        data = VaultMarker.create(meta, master_password)
        self._upload_marker(data, vault_path)
        return meta

    def create_vault_with_recovery(
        self,
        master_password: str,
        filename_enc: bool,
        name: str = "",
        vault_path: str = "",
        progress_cb: Callable[[str], None] | None = None,
    ) -> tuple[VaultMetadata, str]:
        """新建Mi库并同步生成恢复码（一次性写入，推荐的新建入口）。

        相比 create_vault + generate_recovery_code 两步流程，省去开库复核与
        二次重写 Marker，PBKDF2 派生从 4 次降到 2 次（每次约数秒，
        迭代次数与 Android 端协议绑定不可调），显著缩短建库耗时。

        返回:
            (VaultMetadata, 恢复码)；恢复码仅返回给调用方展示，不落盘/上传；
            元信息 has_recovery=True

        异常:
            VaultError: 目标位置已存在Mi库（防止覆盖）
        """
        from Crypto.Random import get_random_bytes

        _report(progress_cb, "正在检查存储位置…")
        if self.has_vault(vault_path):
            raise VaultError("该位置已存在Mi库，请选择「连接」或更换位置")

        if vault_path.strip("/"):
            # 子目录密库：确保目录存在（已存在时容错）
            try:
                self.backend.mkdir(vault_path.strip("/"))
            except Exception:  # noqa: BLE001
                pass

        _report(progress_cb, "正在生成密库元数据…")
        meta = VaultMarker.generate_metadata(filename_enc=filename_enc, name=name)
        secret = get_random_bytes(constants.RECOVERY_SECRET_LEN)
        code = self.encode_recovery_code(secret)
        _report(progress_cb, "生成恢复码保护块（密钥派生，约需数秒）…")
        blob = VaultMarker.build_recovery_blob(secret, master_password)
        # 一次写入：Marker 加密与恢复块同时落盘，无需事后复核重传
        _report(progress_cb, "主密钥派生与加密（约需数秒）…")
        data = VaultMarker.create(meta, master_password, recovery_blob=blob)
        _report(progress_cb, "正在上传密库文件…")
        self._upload_marker(data, vault_path)
        return dataclasses.replace(meta, has_recovery=True), code

    # ------------------------------------------------------------------
    # 连接
    # ------------------------------------------------------------------

    def open_vault(
        self, master_password: str, vault_path: str = "",
        progress_cb: Callable[[str], None] | None = None,
    ) -> VaultMetadata | None:
        """连接Mi库：下载并校验 Vault Marker（签名兼容，默认根目录）。

        返回:
            校验通过返回 VaultMetadata；密码错误或无Mi库返回 None
        """
        _report(progress_cb, "正在载入密库文件…")
        data = self._download_marker(vault_path)
        if data is None:
            return None
        _report(progress_cb, "校验主密码（密钥派生，约需数秒）…")
        return VaultMarker.verify(data, master_password)

    # ------------------------------------------------------------------
    # 重命名（名称随文件保存，跨设备跟随密库）
    # ------------------------------------------------------------------

    def rename_vault(
        self,
        master_password: str,
        new_name: str,
        vault_path: str = "",
    ) -> VaultMetadata:
        """修改密库名称：校验主密码后用新名称重新加密 Marker 并覆盖上传。

        仅替换名称字段，vault_id / salt / iv / 加密配置均不变，
        已加密文件不受影响；既有恢复码块（v3 尾部）原样保留。

        异常:
            VaultError: 无Mi库 / 密码错误 / 上传失败时抛出，界面状态不变更
        """
        meta = self.open_vault(master_password, vault_path)
        if meta is None:
            raise VaultError("密码错误或后端无Mi库，无法修改名称")

        # 保留既有恢复码块（重命名不使恢复码失效）
        old_data = self._download_marker(vault_path) or b""
        _head, tail = VaultMarker.split_recovery_tail(old_data)
        blob = tail[2:] if len(tail) >= 2 else b""

        new_meta = dataclasses.replace(meta, name=new_name.strip())
        data = VaultMarker.create(new_meta, master_password, recovery_blob=blob)
        self._upload_marker(data, vault_path)
        return new_meta

    # ------------------------------------------------------------------
    # 恢复码（v3）：生成 / 凭码开库 / 编解码
    # ------------------------------------------------------------------

    def generate_recovery_code(
        self, master_password: str, vault_path: str = "",
        progress_cb: Callable[[str], None] | None = None,
    ) -> tuple[str, VaultMetadata]:
        """生成（或更换）恢复码：重加密 Marker 并追加恢复块后覆盖上传。

        恢复码 = 随机密钥的 Base32 形式（RECOVERY_CODE_LEN 个字符），
        仅返回给调用方展示，密钥与恢复码本身绝不落盘/上传；
        旧恢复码在新码生效后立即失效。

        返回:
            (恢复码, VaultMetadata)；元信息 has_recovery=True

        异常:
            VaultError: 无Mi库或密码错误时抛出
        """
        from Crypto.Random import get_random_bytes

        meta = self.open_vault(
            master_password, vault_path, progress_cb=progress_cb,
        )
        if meta is None:
            raise VaultError("密码错误或后端无Mi库，无法生成恢复码")

        _report(progress_cb, "正在生成新恢复码…")
        secret = get_random_bytes(constants.RECOVERY_SECRET_LEN)
        code = self.encode_recovery_code(secret)
        _report(progress_cb, "派生保护密钥（约需数秒）…")
        blob = VaultMarker.build_recovery_blob(secret, master_password)

        _report(progress_cb, "加密回写密库文件…")
        data = VaultMarker.create(meta, master_password, recovery_blob=blob)
        self._upload_marker(data, vault_path)
        return code, dataclasses.replace(meta, has_recovery=True)

    def open_vault_with_recovery(
        self, recovery_code: str, vault_path: str = "",
        progress_cb: Callable[[str], None] | None = None,
    ) -> VaultMetadata | None:
        """凭恢复码开库：解密恢复块还原主密码，再走正常校验。

        成功后还原出的主密码暂存于实例属性 ``recovered_password``
        （仅内存），供调用方构建会话；实例短生命周期，不落盘。

        返回:
            校验通过返回 VaultMetadata；恢复码无效/错误返回 None
        """
        _report(progress_cb, "正在解析恢复码…")
        secret = self.decode_recovery_code(recovery_code)
        if secret is None:
            return None
        _report(progress_cb, "正在载入密库文件…")
        data = self._download_marker(vault_path)
        if data is None:
            return None
        _head, tail = VaultMarker.split_recovery_tail(data)
        if len(tail) < 2:
            return None
        blob_len = int.from_bytes(tail[:2], "big")
        blob = tail[2: 2 + blob_len]
        if len(blob) != blob_len:
            return None
        _report(progress_cb, "正在还原主密码…")
        master_password = VaultMarker.decrypt_recovery_blob(blob, secret)
        if master_password is None:
            return None
        _report(progress_cb, "校验密库（密钥派生，约需数秒）…")
        meta = VaultMarker.verify(data, master_password)
        if meta is not None:
            self.recovered_password = master_password
        return meta

    @staticmethod
    def encode_recovery_code(secret: bytes) -> str:
        """随机密钥 -> 恢复码（Base32，无填充）。"""
        return base64.b32encode(secret).decode("ascii").rstrip("=")

    @staticmethod
    def decode_recovery_code(code: str) -> bytes | None:
        """恢复码 -> 随机密钥；格式不符（去分隔符/大写/补填充后仍非法）返回 None。"""
        cleaned = (code or "").replace("-", "").replace(" ", "").upper()
        if len(cleaned) != constants.RECOVERY_CODE_LEN:
            return None
        padded = cleaned + "=" * (-len(cleaned) % 8)
        try:
            return base64.b32decode(padded)
        except Exception:  # noqa: BLE001
            return None

    @staticmethod
    def format_recovery_code(code: str) -> str:
        """恢复码按 4 字符分组展示（XXXX-XXXX-XXXX-XXXX）。"""
        return "-".join(
            code[i: i + 4] for i in range(0, len(code), 4)
        )

    # ------------------------------------------------------------------
    # 上传（新建与重命名共享）
    # ------------------------------------------------------------------

    def _upload_marker(self, data: bytes, vault_path: str = "") -> None:
        """经临时文件上传 Vault Marker（复用后端分块上传接口）。

        Vault Marker 很小（约百余字节），临时文件仅用于适配后端接口。
        Marker 为整体重写的小文件，上传前先删除旧文件，规避后端分块上传的
        断点续传语义（续传会把新内容追加到旧内容之后而非覆盖）。
        """
        import tempfile as _tf

        path = self._marker_path(vault_path)
        if self.backend.exists(path):
            self.backend.delete(path)

        fd, tmp_path = _tf.mkstemp(suffix=".vault")
        try:
            with os.fdopen(fd, "wb") as tmp:
                tmp.write(data)
            for _ in self.backend.upload_chunked(tmp_path, path):
                pass
        finally:
            # 安全删除临时文件（含 Vault Marker 的 salt/密文）
            try:
                os.unlink(tmp_path)
            except OSError:
                pass
