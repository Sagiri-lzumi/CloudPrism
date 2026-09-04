// Package perf 提供性能指标采集：传输速度、缓存占用、进程 CPU 使用率。
//
// 对照 WindowsPy/src/cloudprism/gui/perf_monitor.py 的 PerfMonitor，但修正
// 一处 Python 死接线：Python 的速度依赖 TransferWorker.report_bytes()，
// 而该方法从未被调用（速度恒 0）；Go 端由绑定层把队列的累计完成字节
// （Queue.Aggregate 的 done）注入为 doneGetter，速度按两次采样差分计算。
//
// 本包不持有定时器：绑定层（Wails）按 1s 周期调用 Sample() 并把结果推给
// 前端状态栏，职责与 Qt 的 QTimer 解耦。
package perf

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/platform/win"
)

// Interval 采集间隔（绑定层 ticker 用；与 Python INTERVAL_MS=1000 对齐）。
const Interval = time.Second

// Stats 一次采样的三项指标。
type Stats struct {
	SpeedMBps  float64 // 传输速度（MiB/s）
	CacheBytes int64   // 缓存目录占用（字节）
	CPUPercent float64 // 进程 CPU 占用（0~100，单核语义）
}

// Monitor 采集三项性能指标（并发安全）。
type Monitor struct {
	cachePath string
	// doneGetter 返回传输累计完成字节；由绑定层注入
	// （transfer.Queue.Aggregate 的 done）。nil 时速度恒 0。
	doneGetter func() int64

	mu          sync.Mutex
	prevDone    int64
	prevWall    time.Time
	prevCPUTime float64
	initialized bool
}

// New 构造监控器；构造即建立基线（首次 Sample 即有差分值，
// 对齐 Python start() 里重置基线的行为）。
func New() *Monitor {
	m := &Monitor{}
	m.mu.Lock()
	m.resetBaseLocked()
	m.mu.Unlock()
	return m
}

// SetCachePath 设置缓存目录路径（设置页变更时调用）。
func (m *Monitor) SetCachePath(path string) {
	m.mu.Lock()
	m.cachePath = path
	m.mu.Unlock()
}

// SetDoneGetter 注入传输累计字节来源（绑定层在队列创建后调用一次）。
func (m *Monitor) SetDoneGetter(getter func() int64) {
	m.mu.Lock()
	m.doneGetter = getter
	m.mu.Unlock()
}

// Sample 采样一次三项指标并推进基线。速度按 (Δdone)/(Δ墙钟) 计算；
// 无增量时速度为 0。
func (m *Monitor) Sample() Stats {
	now := time.Now()
	cpuNow := win.ProcessCPUTime()

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.initialized {
		m.resetBaseLocked()
		return Stats{}
	}

	var done int64
	if m.doneGetter != nil {
		done = m.doneGetter()
	}
	elapsed := now.Sub(m.prevWall).Seconds()
	if elapsed <= 0 {
		elapsed = 1.0
	}

	// 速度：区间内完成字节 / 墙钟（对齐 Python _sample:81 公式）
	speed := float64(done-m.prevDone) / (1024 * 1024) / elapsed
	if done-m.prevDone <= 0 {
		speed = 0
	}

	// CPU：进程 CPU 时间差 / 墙钟差 * 100，夹在 [0,100]
	// （os.times 聚合 user+system，单核百分比语义，Python clamp 一致）
	pct := (cpuNow - m.prevCPUTime) / elapsed * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	m.prevDone = done
	m.prevWall = now
	m.prevCPUTime = cpuNow

	return Stats{
		SpeedMBps:  speed,
		CacheBytes: m.calcCacheSize(),
		CPUPercent: pct,
	}
}

// resetBaseLocked 重建采样基线（调用方须持有 mu）。
func (m *Monitor) resetBaseLocked() {
	m.prevDone = 0
	if m.doneGetter != nil {
		m.prevDone = m.doneGetter()
	}
	m.prevWall = time.Now()
	m.prevCPUTime = win.ProcessCPUTime()
	m.initialized = true
}

// calcCacheSize 递归统计缓存目录磁盘占用（字节）；目录不存在或条目读取
// 失败按 0/跳过处理（对齐 Python _calc_cache_size 的 OSError 吞异常）。
func (m *Monitor) calcCacheSize() int64 {
	if m.cachePath == "" {
		return 0
	}
	var total int64
	_ = filepath.Walk(m.cachePath, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 无权限/已删除的条目跳过
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ---------------------------------------------------------------------------
// 展示格式化（对齐 perf_monitor.py:134-161，供绑定层透传或前端直用）
// ---------------------------------------------------------------------------

// FormatSpeed 格式化传输速度显示。
func FormatSpeed(speedMBps float64) string {
	switch {
	case speedMBps < 0.01:
		return "速度: --"
	case speedMBps < 1:
		return "速度: " + strconv.Itoa(int(speedMBps*1024)) + " KB/s"
	default:
		return "速度: " + strconv.FormatFloat(speedMBps, 'f', 1, 64) + " MB/s"
	}
}

// FormatCache 格式化缓存占用显示。
func FormatCache(sizeBytes int64) string {
	if sizeBytes <= 0 {
		return "缓存: --"
	}
	size := float64(sizeBytes)
	for _, u := range []string{"B", "KB", "MB", "GB"} {
		if size < 1024 || u == "GB" {
			if u == "B" {
				return "缓存: " + strconv.FormatInt(int64(size), 10) + " B"
			}
			return "缓存: " + strconv.FormatFloat(size, 'f', 1, 64) + " " + u
		}
		size /= 1024
	}
	return "缓存: --"
}

// FormatCPU 格式化 CPU 占用显示。
func FormatCPU(pct float64) string {
	if pct < 0.1 {
		return "CPU: --"
	}
	return "CPU: " + strconv.Itoa(int(pct+0.5)) + "%"
}
