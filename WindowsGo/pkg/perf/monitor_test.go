package perf

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestSpeedDifferential 速度按 done 字节差分 / 墙钟计算。
func TestSpeedDifferential(t *testing.T) {
	var done atomic.Int64
	m := New()
	m.SetDoneGetter(done.Load)

	// 模拟 1.1 秒内完成 5 MiB → 速度约 4.5 MiB/s
	time.Sleep(100 * time.Millisecond) // 让基线时间流逝
	done.Add(5 * 1024 * 1024)
	time.Sleep(1100 * time.Millisecond)

	s := m.Sample()
	if s.SpeedMBps < 3.5 || s.SpeedMBps > 5.5 {
		t.Errorf("速度应约 4.5 MiB/s（容差带），实得 %.2f", s.SpeedMBps)
	}
}

// TestSpeedIdle 无新增字节时速度归零。
func TestSpeedIdle(t *testing.T) {
	m := New()
	m.SetDoneGetter(func() int64 { return 42 }) // 常量累计（无增量）
	m.Sample()                                  // 预热：消耗常量增量重建基线（New 时 getter 未注入）
	time.Sleep(1100 * time.Millisecond)
	if s := m.Sample(); s.SpeedMBps != 0 {
		t.Errorf("无增量速度应为 0，实得 %.2f", s.SpeedMBps)
	}
}

// TestCacheSizeScan 缓存目录递归统计（含子目录）。
func TestCacheSizeScan(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, n int) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.bin", 1000)
	write("sub/b.bin", 2000)
	write("sub/deep/c.bin", 4000)

	m := New()
	m.SetCachePath(dir)
	if got := m.Sample().CacheBytes; got != 7000 {
		t.Errorf("缓存大小应为 7000，实得 %d", got)
	}

	m.SetCachePath(filepath.Join(dir, "nonexistent"))
	if got := m.Sample().CacheBytes; got != 0 {
		t.Errorf("不存在目录应计 0，实得 %d", got)
	}
}

// TestCPUPercentRange busy 后 CPU 占用应显著 >0 且不超 100。
func TestCPUPercentRange(t *testing.T) {
	m := New()
	// 空转基线
	m.Sample()
	// 密集计算约 300ms（把 CPU 打上去）
	deadline := time.Now().Add(300 * time.Millisecond)
	x := 0
	for time.Now().Before(deadline) {
		x++
	}
	_ = x
	s := m.Sample()
	if s.CPUPercent <= 0 {
		t.Errorf("busy 周期后 CPU%% 应 > 0，实得 %.1f", s.CPUPercent)
	}
	if s.CPUPercent > 100 {
		t.Errorf("CPU%% 应 ≤ 100（单核语义 clamp），实得 %.1f", s.CPUPercent)
	}
}

// TestFormatFunctions 三个格式化函数与 Python 语义一致。
func TestFormatFunctions(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"speed-idle", FormatSpeed(0), "速度: --"},
		{"speed-kb", FormatSpeed(0.5), "速度: 512 KB/s"},
		{"speed-mb", FormatSpeed(2.0), "速度: 2.0 MB/s"},
		{"cache-zero", FormatCache(0), "缓存: --"},
		{"cache-b", FormatCache(512), "缓存: 512 B"},
		{"cache-kb", FormatCache(2048), "缓存: 2.0 KB"},
		{"cache-mb", FormatCache(3 * 1024 * 1024), "缓存: 3.0 MB"},
		{"cache-gb", FormatCache(2 * 1024 * 1024 * 1024), "缓存: 2.0 GB"},
		{"cpu-idle", FormatCPU(0.05), "CPU: --"},
		{"cpu-pct", FormatCPU(23.4), "CPU: 23%"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: %q != %q", c.name, c.got, c.want)
		}
	}
}
