package appstate

import (
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
)

// 自动锁时长档位（索引 → 分钟），与设置键 KeyAutoLockIndex 对齐。
var autoLockMinutesByIndex = [...]int{0, 5, 15, 30}

// tickInterval 心跳检查周期：1s 粒度与 Python 端定时器一致，空闲判定
// 精度按分钟档位，秒级轮询开销可忽略。
const tickInterval = time.Second

// Activity 记录用户活动（鼠标/键盘/界面交互），由绑定层在离散交互事件
// 时调用。自动锁以它作为空闲计时基准。未连接/从不锁定时为空操作。
func (s *State) Activity() {
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
}

// ApplyAutoLockIndex 按设置页的档位索引（0=从不 1/2/3=5/15/30 分钟）
// 重启自动锁心跳。设置保存后由绑定层调用。
func (s *State) ApplyAutoLockIndex(index int) {
	if index < 0 || index >= len(autoLockMinutesByIndex) {
		index = 0
	}
	min := autoLockMinutesByIndex[index]
	s.cfg.Store.SetInt(settings.KeyAutoLockIndex, index)

	s.stopAutoLock()
	s.mu.Lock()
	s.autoLockMin = min
	s.mu.Unlock()
	if min > 0 {
		s.startAutoLock()
	}
}

// startAutoLock 启动空闲检查心跳（幂等：已有心跳时不重复）。
func (s *State) startAutoLock() {
	s.mu.Lock()
	if s.autoLockStop != nil {
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	s.autoLockStop = stop
	s.mu.Unlock()

	go s.autoLockLoop(stop)
}

// stopAutoLock 停止心跳（幂等）。锁库/设置变更/进程退出时调用。
func (s *State) stopAutoLock() {
	s.mu.Lock()
	stop := s.autoLockStop
	s.autoLockStop = nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// autoLockLoop 定时检查空闲超时。传输进行中豁免（锁库会使会话失效
// 导致在传任务全部失败）——对照 app.py _check_auto_lock。
func (s *State) autoLockLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.checkAutoLockDue()
		}
	}
}

// checkAutoLockDue 判定并执行一次自动锁定（独立方法便于测试）。
func (s *State) checkAutoLockDue() {
	s.mu.RLock()
	min := s.autoLockMin
	connected := s.conn != nil
	idleFor := time.Since(s.lastActive)
	syncRunning := s.syncRun.Running
	s.mu.RUnlock()

	if min <= 0 || !connected {
		return
	}
	// 传输进行中或同步批次运行中：豁免（空闲计时继续走，结束后下一拍即可能锁）
	if s.cfg.Queue.HasActive() || syncRunning {
		return
	}
	if idleFor >= time.Duration(min)*time.Minute {
		s.cfg.Log.Info("空闲超时，自动锁定密库", "idleMin", min)
		s.Lock()
	}
}
