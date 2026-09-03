"""性能监控测试（pytest-qt）。

验证 PerfMonitor 的信号发射与格式化函数。
"""

from __future__ import annotations

import os

import pytest

pytest.importorskip("PySide6")

from cloudprism.gui.perf_monitor import (
    PerfMonitor,
    format_cache,
    format_cpu,
    format_speed,
)


class TestFormatFunctions:
    """格式化函数。"""

    def test_format_speed_zero(self):
        """零速度显示为 --。"""
        assert format_speed(0.0) == "速度: --"
        assert format_speed(0.001) == "速度: --"

    def test_format_speed_kb(self):
        """KB/s 级别。"""
        result = format_speed(0.5)
        assert "KB/s" in result
        assert "512" in result

    def test_format_speed_mb(self):
        """MB/s 级别。"""
        result = format_speed(2.5)
        assert "MB/s" in result
        assert "2.5" in result

    def test_format_cache_zero(self):
        """零缓存显示为 --。"""
        assert format_cache(0) == "缓存: --"

    def test_format_cache_bytes(self):
        """字节级别。"""
        assert format_cache(100) == "缓存: 100 B"

    def test_format_cache_mb(self):
        """MB 级别。"""
        result = format_cache(1024 * 1024)
        assert "MB" in result
        assert "1.0" in result

    def test_format_cpu_zero(self):
        """零 CPU 显示为 --。"""
        assert format_cpu(0.0) == "CPU: --"
        assert format_cpu(0.05) == "CPU: --"

    def test_format_cpu_percent(self):
        """CPU 百分比。"""
        result = format_cpu(25.5)
        assert "26" in result or "25" in result
        assert "%" in result


class TestPerfMonitor:
    """性能监控器。"""

    def test_report_bytes_accumulates(self, qtbot):
        """report_bytes 累加字节数。"""
        monitor = PerfMonitor()
        monitor.report_bytes(100)
        monitor.report_bytes(200)
        assert monitor._bytes_since_last == 300

    def test_set_cache_path(self, qtbot):
        """set_cache_path 更新缓存路径。"""
        monitor = PerfMonitor()
        monitor.set_cache_path("/tmp/test_cache")
        assert monitor._cache_path == "/tmp/test_cache"

    def test_calc_cache_size_nonexistent(self, qtbot):
        """不存在的缓存目录返回 0。"""
        monitor = PerfMonitor(cache_path="/nonexistent/path")
        assert monitor._calc_cache_size() == 0

    def test_calc_cache_size_with_files(self, qtbot, tmp_path):
        """有文件的缓存目录返回正确大小。"""
        cache_dir = tmp_path / "cache"
        cache_dir.mkdir()
        (cache_dir / "file1").write_bytes(b"x" * 100)
        (cache_dir / "file2").write_bytes(b"y" * 200)

        monitor = PerfMonitor(cache_path=str(cache_dir))
        assert monitor._calc_cache_size() == 300

    def test_stats_signal_emitted(self, qtbot):
        """start() 后定时器触发信号发射。"""
        monitor = PerfMonitor()

        received = []
        monitor.statsUpdated.connect(
            lambda speed, cache, cpu: received.append((speed, cache, cpu))
        )
        monitor.start()

        # 等待至少一个采样周期
        qtbot.wait(1200)
        monitor.stop()

        assert len(received) >= 1
        speed, cache, cpu = received[0]
        assert isinstance(speed, float)
        assert isinstance(cache, int)
        assert isinstance(cpu, float)
