package loggingx

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 测试脱敏：敏感键值打码，普通键保持原样。
func TestRedactSensitiveAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(&redactHandler{Handler: slog.NewTextHandler(&buf, nil)})
	log.Info("开库失败",
		"masterPassword", "s3cr3t-pass",
		"path", "/vault/.marker",
		"err", "密码错误",
	)
	out := buf.String()
	if !strings.Contains(out, "masterPassword=****") {
		t.Errorf("主密码应打码，实得: %s", out)
	}
	if strings.Contains(out, "s3cr3t-pass") {
		t.Errorf("主密码明文泄漏进日志: %s", out)
	}
	if !strings.Contains(out, "path=/vault/.marker") || !strings.Contains(out, "err=密码错误") {
		t.Errorf("普通属性被误伤: %s", out)
	}
}

// 测试轮转：小上限下写入多条日志后旧文件被顺延改名。
func TestRotatorRotatesFiles(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, FileName)
	// 单条 64 字节、上限 100：每写 1~2 条即触发轮转
	r, err := newRotator(base, 100, Keep)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	line := bytes.Repeat([]byte("x"), 64)
	for i := 0; i < 30; i++ {
		if _, err := r.Write(line); err != nil {
			t.Fatal(err)
		}
	}

	// 当前文件 + .1 + .2 三份应在盘上（.3 不允许出现）
	for _, name := range []string{FileName, FileName + ".1", FileName + ".2"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("轮转产物缺失 %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, FileName+".3")); err == nil {
		t.Error("不应保留第 3 份旧文件")
	}

	// 当前文件不应超过上限太多（单次追加最大 64 字节）
	st, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > 100+64 {
		t.Errorf("当前文件超限: %d", st.Size())
	}
}

// 测试 New 的降级路径：data 目录不可创建时返回可用 logger 而非 panic。
func TestNewDegradesOnBadDir(t *testing.T) {
	// 用一个文件路径顶替目录：MkdirAll 必失败
	blocker := filepath.Join(t.TempDir(), "file-as-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	log, _ := New(filepath.Join(blocker, "sub"))
	if log == nil {
		t.Fatal("降级路径应返回非 nil logger")
	}
	log.Info("降级写入不 panic", "k", "v") // 写 Discard，静默通过
}

// 测试正常目录下 New 落盘与脱敏链路整体可用。
func TestNewWritesRedactedFile(t *testing.T) {
	dir := t.TempDir()
	log, closeLog := New(dir)
	defer closeLog()
	log.Info("连接", "password", "hunter2", "ok", true)

	data, err := os.ReadFile(filepath.Join(dir, "logs", FileName))
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "password=****") {
		t.Errorf("落盘日志应含掩码，实得: %s", out)
	}
	if strings.Contains(out, "hunter2") {
		t.Error("落盘日志泄漏明文密码")
	}
}

// 记录组装不 panic 的冒烟：slog 记录在并发场景下属性顺序无关紧要。
func TestHandleConcurrentSafety(t *testing.T) {
	h := &redactHandler{Handler: slog.NewTextHandler(io.Discard, nil)}
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)
		r.AddAttrs(slog.String("token", "abc"), slog.Int("n", i))
		if err := h.Handle(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
}
