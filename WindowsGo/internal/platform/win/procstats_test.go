//go:build windows

package win

import (
	"testing"
	"time"
)

// TestProcessCPUTime 进程 CPU 时间应随忙碌增长（冒烟：syscall 通路可用）。
func TestProcessCPUTime(t *testing.T) {
	before := ProcessCPUTime()
	deadline := time.Now().Add(200 * time.Millisecond)
	x := 0
	for time.Now().Before(deadline) {
		x++
	}
	_ = x
	after := ProcessCPUTime()

	if after < before {
		t.Errorf("CPU 时间不应回退：before=%.4f after=%.4f", before, after)
	}
	if after <= 0 {
		t.Error("ProcessCPUTime 应返回正值")
	}
}
