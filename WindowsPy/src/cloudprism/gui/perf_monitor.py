"""性能监控：定时采集传输速度、缓存占用、CPU 使用率。

PerfMonitor 通过 QTimer 每秒采集一次数据并发射 statsUpdated 信号，
供状态栏显示实时性能指标。

采集方式：
  - 传输速度：由 TransferWorker 通过 report_bytes() 回报字节数
  - 缓存占用：扫描缓存目录大小
  - CPU 占用：os.times() 差值计算（避免额外依赖 psutil）
"""

from __future__ import annotations

import os
import time

from PySide6.QtCore import QObject, QTimer, Signal


class PerfMonitor(QObject):
    """性能监控：定时采集传输速度、缓存占用、CPU 使用率。"""

    # 信号：(transfer_speed_MB/s, cache_bytes, cpu_percent)
    statsUpdated = Signal(float, int, float)

    # 采集间隔（毫秒）
    INTERVAL_MS = 1000

    def __init__(self, cache_path: str = "", parent=None) -> None:
        super().__init__(parent)

        self._cache_path = cache_path

        # 传输速度计算：累计字节数 + 上次采样时间
        self._bytes_since_last = 0
        self._last_sample_time = time.monotonic()

        # CPU 计算：上次采样的进程时间
        self._last_cpu_time = self._get_cpu_time()

        # 定时器
        self._timer = QTimer(self)
        self._timer.setInterval(self.INTERVAL_MS)
        self._timer.timeout.connect(self._sample)

    # ------------------------------------------------------------------
    # 公开方法
    # ------------------------------------------------------------------

    def start(self) -> None:
        """开始定时采集。"""
        self._last_sample_time = time.monotonic()
        self._last_cpu_time = self._get_cpu_time()
        self._bytes_since_last = 0
        self._timer.start()

    def stop(self) -> None:
        """停止采集。"""
        self._timer.stop()

    def report_bytes(self, n: int) -> None:
        """由 TransferWorker 调用，回报本次传输的字节数。"""
        self._bytes_since_last += n

    def set_cache_path(self, path: str) -> None:
        """设置缓存目录路径（设置页变更时调用）。"""
        self._cache_path = path

    # ------------------------------------------------------------------
    # 内部方法
    # ------------------------------------------------------------------

    def _sample(self) -> None:
        """定时采样：计算速度/缓存/CPU 并发射信号。"""
        now = time.monotonic()
        elapsed = now - self._last_sample_time
        if elapsed <= 0:
            elapsed = 1.0

        # 传输速度（MB/s）
        speed = (self._bytes_since_last / 1024 / 1024) / elapsed
        self._bytes_since_last = 0
        self._last_sample_time = now

        # 缓存占用（字节）
        cache_bytes = self._calc_cache_size()

        # CPU 占用（%）
        cpu_pct = self._calc_cpu_percent()

        self.statsUpdated.emit(speed, cache_bytes, cpu_pct)

    def _calc_cache_size(self) -> int:
        """计算缓存目录磁盘占用（字节）。"""
        if not self._cache_path or not os.path.isdir(self._cache_path):
            return 0
        total = 0
        for dirpath, _dirs, files in os.walk(self._cache_path):
            for f in files:
                fp = os.path.join(dirpath, f)
                try:
                    total += os.path.getsize(fp)
                except OSError:
                    pass
        return total

    def _get_cpu_time(self) -> float:
        """获取当前进程 CPU 时间（秒）。"""
        t = os.times()
        return t.user + t.system

    def _calc_cpu_percent(self) -> float:
        """计算进程 CPU 占用百分比。

        基于 os.times() 差值，避免额外依赖 psutil。
        返回 0~100 范围的值。
        """
        now_cpu = self._get_cpu_time()
        now_wall = time.monotonic()

        cpu_delta = now_cpu - self._last_cpu_time
        wall_delta = now_wall - (self._last_sample_time - self.INTERVAL_MS / 1000)
        if wall_delta <= 0:
            wall_delta = 1.0

        self._last_cpu_time = now_cpu

        # CPU 百分比 = 进程 CPU 时间差 / 墙钟时间差 * 100
        pct = (cpu_delta / wall_delta) * 100
        # 限制在合理范围
        return max(0.0, min(pct, 100.0))


def format_speed(speed_mb: float) -> str:
    """格式化传输速度显示。"""
    if speed_mb < 0.01:
        return "速度: --"
    if speed_mb < 1:
        return f"速度: {speed_mb * 1024:.0f} KB/s"
    return f"速度: {speed_mb:.1f} MB/s"


def format_cache(size_bytes: int) -> str:
    """格式化缓存占用显示。"""
    if size_bytes == 0:
        return "缓存: --"
    size = float(size_bytes)
    for unit in ("B", "KB", "MB", "GB"):
        if size < 1024 or unit == "GB":
            if unit == "B":
                return f"缓存: {int(size)} {unit}"
            return f"缓存: {size:.1f} {unit}"
        size /= 1024
    return f"缓存: {size_bytes} B"


def format_cpu(pct: float) -> str:
    """格式化 CPU 占用显示。"""
    if pct < 0.1:
        return "CPU: --"
    return f"CPU: {pct:.0f}%"
