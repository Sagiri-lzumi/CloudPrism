package appstate

import (
	"context"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// RequestStats 后台递归遍历当前后端，统计云端占用总字节与文件数。
//
// seq 竞态防护（对照 app.py _start_stats_worker）：
//   - 上一轮遍历未结束时再触发直接跳过（压后端与 UI 回填抖动）；
//   - 锁库/切库时 statsSeq 递增，在飞结果回来后因序号过期被丢弃。
//
// 结果写入 State 并由快照帧携带，前端订阅「统计完成」无需单独事件。
func (s *State) RequestStats(ctx context.Context) {
	conn, err := s.requireConn()
	if err != nil {
		return
	}
	backend := conn.backend

	s.mu.Lock()
	if s.statsRunning {
		s.mu.Unlock()
		return
	}
	s.statsSeq++
	seq := s.statsSeq
	s.statsRunning = true
	s.statsDone = false
	s.statsFailed = false
	s.mu.Unlock()

	go func() {
		total, files, err := walkStats(ctx, backend)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.statsRunning = false
		// 仅最新一轮结果回填；期间锁库/切库后序号已变，直接丢弃
		if seq != s.statsSeq {
			return
		}
		if err != nil {
			s.statsFailed = true
			s.cfg.Log.Warn("云端占用统计失败", "err", err)
			return
		}
		s.statsDone = true
		s.statsTotal = total
		s.statsFiles = files
		s.statsFinished = time.Now()
	}()
}

// walkStats 递归遍历后端累计总字节与文件数（对目录容错：单个目录
// 列出失败跳过，对照 app.py _bg 的 except: continue）。
func walkStats(ctx context.Context, backend storage.Backend) (total int64, files int64, err error) {
	var stack []string
	stack = append(stack, "")
	for len(stack) > 0 {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return total, files, ctxErr
		}
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, listErr := backend.ListDir(ctx, cur)
		if listErr != nil {
			continue // 单目录失败不拖累整体（远端目录被删等瞬态）
		}
		for _, e := range entries {
			if e.IsDir {
				stack = append(stack, joinRemote(cur, e.Name))
				continue
			}
			total += e.Size
			files++
		}
	}
	return total, files, nil
}

// joinRemote 拼远端相对路径（根目录时不加分隔符）。
func joinRemote(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}
