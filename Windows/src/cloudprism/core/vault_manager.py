"""金库管理器：新建 / 连接金库的核心流程（非 GUI）。

- 新建：生成 VaultMetadata（随机 vault_id/salt/iv）-> 创建 Vault Marker ->
  上传到后端根目录
- 连接：从后端根目录下载 Vault Marker -> 校验主密码 -> 返回元信息

InitWizard 与后续 CLI 场景复用本模块；GUI 不直接操作 VaultMarker。
"""

from __future__ import annotations

import os
import tempfile

from cloudprism import constants
from cloudprism.crypto.vault import VaultMarker, VaultMetadata
from cloudprism.storage.backend import StorageBackend


class VaultError(Exception):
    """金库操作异常。"""


class VaultManager:
    """金库生命周期管理。"""

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
    ) -> VaultMetadata:
        """新建金库：生成并上传 Vault Marker。

        参数:
            master_password: 用户主密码
            filename_enc: 文件名加密开关（初始化后不可变更）

        返回:
            VaultMetadata

        异常:
            VaultError: 后端已存在金库（防止覆盖）
        """
        if self.has_vault():
            raise VaultError("后端已存在金库，请选择「连接」或更换后端")

        meta = VaultMarker.generate_metadata(filename_enc=filename_enc)
        data = VaultMarker.create(meta, master_password)

        # 经临时文件上传（复用后端分块上传）
        with tempfile.NamedTemporaryFile(
            delete=False, suffix=".vault"
        ) as tmp:
            tmp.write(data)
            tmp_path = tmp.name
        try:
            for _ in self.backend.upload_chunked(
                tmp_path, constants.VAULT_MARKER_NAME
            ):
                pass
        finally:
            try:
                os.unlink(tmp_path)
            except OSError:
                pass
        return meta

    # ------------------------------------------------------------------
    # 连接
    # ------------------------------------------------------------------

    def open_vault(self, master_password: str) -> VaultMetadata | None:
        """连接金库：下载并校验 Vault Marker。

        返回:
            校验通过返回 VaultMetadata；密码错误或无金库返回 None
        """
        data = self._download_marker()
        if data is None:
            return None
        return VaultMarker.verify(data, master_password)
