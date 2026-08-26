"""Mi库管理器：新建 / 连接 / 重命名Mi库的核心流程（非 GUI）。

- 新建：生成 VaultMetadata（随机 vault_id/salt/iv）-> 创建 Vault Marker ->
  上传到后端根目录（可同时携带用户自定义名称）
- 连接：从后端根目录下载 Vault Marker -> 校验主密码 -> 返回元信息
- 重命名：校验主密码后用新名称重新加密 Marker 并覆盖上传，
  名称随文件保存，跨设备跟随密库

InitWizard 与后续 CLI 场景复用本模块；GUI 不直接操作 VaultMarker。
"""

from __future__ import annotations

import dataclasses
import os

from cloudprism import constants
from cloudprism.crypto.vault import VaultMarker, VaultMetadata
from cloudprism.storage.backend import StorageBackend


class VaultError(Exception):
    """Mi库操作异常。"""


class VaultManager:
    """Mi库生命周期管理。"""

    def __init__(self, backend: StorageBackend) -> None:
        self.backend = backend

    # ------------------------------------------------------------------
    # 查找与判断
    # ------------------------------------------------------------------

    def has_vault(self) -> bool:
        """后端根目录是否存在 Vault Marker。"""
        return self.backend.exists(constants.VAULT_MARKER_NAME)

    def _download_marker(self) -> bytes | None:
        """下载 Vault Marker 内容；不存在返回 None。"""
        if not self.has_vault():
            return None
        size = self.backend.get_size(constants.VAULT_MARKER_NAME)
        # marker 很小（约 101 字节），整读
        return self.backend.download_range(
            constants.VAULT_MARKER_NAME, 0, max(size - 1, 0)
        )

    # ------------------------------------------------------------------
    # 新建
    # ------------------------------------------------------------------

    def create_vault(
        self,
        master_password: str,
        filename_enc: bool,
        name: str = "",
    ) -> VaultMetadata:
        """新建Mi库：生成并上传 Vault Marker。

        参数:
            master_password: 用户主密码
            filename_enc: 文件名加密开关（初始化后不可变更）
            name: 用户自定义密库名称（可选，随 Marker 加密保存）

        返回:
            VaultMetadata

        异常:
            VaultError: 后端已存在Mi库（防止覆盖）
        """
        if self.has_vault():
            raise VaultError("后端已存在Mi库，请选择「连接」或更换后端")

        meta = VaultMarker.generate_metadata(filename_enc=filename_enc, name=name)
        data = VaultMarker.create(meta, master_password)
        self._upload_marker(data)
        return meta

    # ------------------------------------------------------------------
    # 连接
    # ------------------------------------------------------------------

    def open_vault(self, master_password: str) -> VaultMetadata | None:
        """连接Mi库：下载并校验 Vault Marker。

        返回:
            校验通过返回 VaultMetadata；密码错误或无Mi库返回 None
        """
        data = self._download_marker()
        if data is None:
            return None
        return VaultMarker.verify(data, master_password)

    # ------------------------------------------------------------------
    # 重命名（名称随文件保存，跨设备跟随密库）
    # ------------------------------------------------------------------

    def rename_vault(self, master_password: str, new_name: str) -> VaultMetadata:
        """修改密库名称：校验主密码后用新名称重新加密 Marker 并覆盖上传。

        仅替换名称字段，vault_id / salt / iv / 加密配置均不变，
        已加密文件不受影响。

        异常:
            VaultError: 无Mi库 / 密码错误 / 上传失败时抛出，界面状态不变更
        """
        meta = self.open_vault(master_password)
        if meta is None:
            raise VaultError("密码错误或后端无Mi库，无法修改名称")

        new_meta = dataclasses.replace(meta, name=new_name.strip())
        data = VaultMarker.create(new_meta, master_password)
        self._upload_marker(data)
        return new_meta

    # ------------------------------------------------------------------
    # 上传（新建与重命名共享）
    # ------------------------------------------------------------------

    def _upload_marker(self, data: bytes) -> None:
        """经临时文件上传 Vault Marker（复用后端分块上传接口）。

        Vault Marker 很小（约百余字节），临时文件仅用于适配后端接口。
        Marker 为整体重写的小文件，上传前先删除旧文件，规避后端分块上传的
        断点续传语义（续传会把新内容追加到旧内容之后而非覆盖）。
        """
        import tempfile as _tf

        if self.backend.exists(constants.VAULT_MARKER_NAME):
            self.backend.delete(constants.VAULT_MARKER_NAME)

        fd, tmp_path = _tf.mkstemp(suffix=".vault")
        try:
            with os.fdopen(fd, "wb") as tmp:
                tmp.write(data)
            for _ in self.backend.upload_chunked(
                tmp_path, constants.VAULT_MARKER_NAME
            ):
                pass
        finally:
            # 安全删除临时文件（含 Vault Marker 的 salt/密文）
            try:
                os.unlink(tmp_path)
            except OSError:
                pass
