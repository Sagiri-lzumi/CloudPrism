// Package loggingx 提供应用日志设施：文件轮转输出 + 敏感值脱敏。
//
// 对应 Python 端 logging + faulthandler crash.log 的职责（差异清单 ④）：
//   - 轮转：单文件上限 2MB，保留 2 份旧文件（盘上共 3 份），防止日志无限增长；
//   - 脱敏：主密码 / 令牌 / 恢复码等敏感属性值一律打 ****，
//     避免「密码错误重试」类日志把用户秘密写进明文文件；
//   - 降级：data/ 不可写时日志写进 io.Discard，绝不让日志故障拖垮启动。
package loggingx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// 轮转参数。
const (
	// MaxSize 单文件字节上限。
	MaxSize = 2 << 20
	// Keep 盘上保留的旧文件份数（不含当前文件）。
	Keep = 2
	// FileName 日志文件名（轮转产物为 FileName.1 / FileName.2）。
	FileName = "cloudprism.log"
)

// sensitiveKeys 属性键命中任一子串（大小写不敏感）即整体打码。
var sensitiveKeys = []string{
	"password", "passwd", "token", "secret",
	"recovery", "master", "credential",
}

// New 在 dataDir/logs/ 下构造应用日志器，返回 (logger, 关闭函数)。
//
// 目录创建或文件打开失败时降级为静默日志器（写 Discard），调用方无需
// 感知失败——便携目录不可写属于「降级不崩溃」范畴。关闭函数在进程退出
// 前调用以释放文件句柄；降级路径返回的关闭函数为空操作。
func New(dataDir string) (*slog.Logger, func()) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return slog.New(slog.NewTextHandler(io.Discard, opts)), func() {}
	}
	w, err := newRotator(filepath.Join(dir, FileName), MaxSize, Keep)
	if err != nil {
		return slog.New(slog.NewTextHandler(io.Discard, opts)), func() {}
	}
	return slog.New(&redactHandler{Handler: slog.NewTextHandler(w, opts)}), func() { _ = w.Close() }
}

// ---------------------------------------------------------------------------
// 脱敏 handler
// ---------------------------------------------------------------------------

// redactHandler 包装底层 handler：Handle 前遍历记录属性，命中敏感键名
// 的属性值替换为固定掩码。不修改原记录（slog 约定记录不可变），而是
// 复制出一份清洗后的新记录再下传。
type redactHandler struct {
	slog.Handler
}

// Handle 清洗记录属性后交给被包装 handler。
func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redact(a))
		return true
	})
	return h.Handler.Handle(ctx, clean)
}

// redact 判定属性是否需要打码。
//
// 键名按小写做子串匹配：调用方习惯各异（masterPassword / master_pw /
// password 等），子串匹配最稳；误伤风险极小（日志键名都由本仓代码控制）。
func redact(a slog.Attr) slog.Attr {
	k := strings.ToLower(a.Key)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return slog.Attr{Key: a.Key, Value: slog.StringValue("****")}
		}
	}
	return a
}

// ---------------------------------------------------------------------------
// 轮转 writer
// ---------------------------------------------------------------------------

// rotator 是写日志文件的 io.Writer：累计大小超过 max 时把旧文件顺延改名
// （.2 丢弃），当前文件重开。并发安全（slog 可能多 goroutine 调用）。
type rotator struct {
	mu   sync.Mutex
	path string // 当前日志文件完整路径
	keep int    // 保留旧文件份数
	max  int64  // 单文件字节上限
	file *os.File
	size int64 // 当前文件已写字节
}

// newRotator 打开（或创建）日志文件并构造轮转器。
func newRotator(path string, max int64, keep int) (*rotator, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &rotator{path: path, keep: keep, max: max, file: f, size: st.Size()}, nil
}

// Write 写日志；若追加将越过上限则先轮转再写。
func (r *rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate 顺延旧文件并重开当前文件：
//
//	base.keep-1 → 删除；base.i → base.(i+1)（倒序，避免覆盖）；base → base.1
//
// Windows 的 rename 不覆盖已存在目标，故每次改名先删目标（幂等无害）。
func (r *rotator) rotate() error {
	if err := r.file.Close(); err != nil {
		return err
	}
	for i := r.keep - 1; i >= 1; i-- {
		old := numberedPath(r.path, i)
		if _, err := os.Stat(old); err == nil {
			next := numberedPath(r.path, i+1)
			_ = os.Remove(next) // 目标已存在（keep 边界）先删除
			if err := os.Rename(old, next); err != nil {
				return err
			}
		}
	}
	first := numberedPath(r.path, 1)
	_ = os.Remove(first)
	if err := os.Rename(r.path, first); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	r.file = f
	r.size = 0
	return nil
}

// numberedPath 计算第 n 份轮转文件的路径（n 从 1 起）。
func numberedPath(path string, n int) string {
	return path + "." + strconv.Itoa(n)
}

// Close 关闭当前日志文件。
func (r *rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.file.Close()
}
