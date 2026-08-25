"""应用设置持久化存储（QSettings 封装）。

持久化以下设置（Windows 下存于注册表，其他平台为用户配置目录）：
  - 外观：主题索引、字体大小
  - 缓存：大小上限、缓存路径
  - 传输：分块大小索引
  - 性能：加密最大内核数
  - 安全：自动锁定超时索引
  - 连接：后端类型与上次连接参数（密码/token 绝不落盘于此）
"""

from __future__ import annotations

from PySide6.QtCore import QSettings


class SettingsStore:
    """QSettings 的类型化读写封装。"""

    ORGANIZATION = "CloudPrism"
    APPLICATION = "CloudPrism"

    def __init__(self, settings: QSettings | None = None) -> None:
        # 允许注入 settings 实例（测试用内存 QSettings）
        self._s = settings or QSettings(self.ORGANIZATION, self.APPLICATION)

    # ------------------------------------------------------------------
    # 通用读写
    # ------------------------------------------------------------------

    def _get(self, key: str, default, cast=str):
        """读取并转换类型；缺失或转换失败时返回默认值。"""
        v = self._s.value(key)
        if v is None:
            return default
        try:
            return cast(v)
        except (TypeError, ValueError):
            return default

    def _set(self, key: str, value) -> None:
        self._s.setValue(key, value)

    def sync(self) -> None:
        """立即写入磁盘/注册表。"""
        self._s.sync()

    # ------------------------------------------------------------------
    # 外观
    # ------------------------------------------------------------------

    def theme_index(self) -> int:
        """主题下拉索引（0=跟随系统 1=深色 2=浅色）。"""
        return self._get("appearance/theme_index", 0, int)

    def set_theme_index(self, index: int) -> None:
        self._set("appearance/theme_index", index)

    def font_size(self) -> int:
        """字体大小（px），默认 14。"""
        return self._get("appearance/font_size", 14, int)

    def set_font_size(self, size: int) -> None:
        self._set("appearance/font_size", size)

    # ------------------------------------------------------------------
    # 缓存
    # ------------------------------------------------------------------

    def cache_limit_mb(self) -> int:
        return self._get("cache/limit_mb", 512, int)

    def set_cache_limit_mb(self, mb: int) -> None:
        self._set("cache/limit_mb", mb)

    def cache_path(self) -> str:
        """缓存目录；空串表示使用默认临时目录。"""
        return self._get("cache/path", "", str)

    def set_cache_path(self, path: str) -> None:
        self._set("cache/path", path)

    # ------------------------------------------------------------------
    # 传输 / 性能 / 安全
    # ------------------------------------------------------------------

    def chunk_index(self) -> int:
        """分块大小下拉索引（0=256KB 1=512KB 2=1MB 3=4MB）。"""
        return self._get("transfer/chunk_index", 1, int)

    def set_chunk_index(self, index: int) -> None:
        self._set("transfer/chunk_index", index)

    def max_cores(self) -> int:
        """加密最大内核数，0 表示未设置（使用界面默认）。"""
        return self._get("perf/max_cores", 0, int)

    def set_max_cores(self, cores: int) -> None:
        self._set("perf/max_cores", cores)

    def auto_lock_index(self) -> int:
        """自动锁定下拉索引（0=从不 1=5分钟 2=15分钟 3=30分钟）。"""
        return self._get("security/auto_lock_index", 0, int)

    def set_auto_lock_index(self, index: int) -> None:
        self._set("security/auto_lock_index", index)

    # ------------------------------------------------------------------
    # 上次连接参数（密码不落盘）
    # ------------------------------------------------------------------

    def backend_type(self) -> str:
        """上次使用的后端类型：local / webdav / baidu。"""
        return self._get("conn/backend_type", "local", str)

    def set_backend_type(self, t: str) -> None:
        self._set("conn/backend_type", t)

    def local_dir(self) -> str:
        return self._get("conn/local_dir", "", str)

    def set_local_dir(self, d: str) -> None:
        self._set("conn/local_dir", d)

    def webdav_url(self) -> str:
        return self._get("conn/webdav_url", "", str)

    def set_webdav_url(self, u: str) -> None:
        self._set("conn/webdav_url", u)

    def webdav_user(self) -> str:
        return self._get("conn/webdav_user", "", str)

    def set_webdav_user(self, u: str) -> None:
        self._set("conn/webdav_user", u)
