package streaming

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/pipeline"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

const testPW = "proxy_test_pw"

// testPlain 构造确定性明文（bytes(range(256))*n，Python 集成测试同款）。
func testPlain(n int) []byte {
	p := make([]byte, 256*n)
	for i := range p {
		p[i] = byte(i % 256)
	}
	return p
}

// uploadPlain 把明文加密上传到本地后端（media/ 子目录）。
func uploadPlain(t *testing.T, sess *session.Session, backend storage.Backend, plain []byte, remote string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "plain.bin")
	if err := os.WriteFile(src, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	encTmp := filepath.Join(t.TempDir(), "enc.bin")
	f, err := os.Create(encTmp)
	if err != nil {
		t.Fatal(err)
	}
	enc := &pipeline.Encryptor{Sess: sess}
	if _, err := enc.EncryptFile(context.Background(), src, f, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backend.Mkdir(context.Background(), "media"); err != nil {
		t.Fatal(err)
	}
	if err := backend.UploadChunked(context.Background(), encTmp, remote, 0, nil); err != nil {
		t.Fatal(err)
	}
}

// env 是集成测试环境：本地后端 + 会话 + 代理 + httptest 服务。
type env struct {
	sess    *session.Session
	backend *storage.Local
	root    string
	srv     *Server
	ts      *httptest.Server
	entry   *Entry // 已注册的流端点（media/video.cpenc）
}

// newEnv 加密上传 5120 字节明文并注册流令牌（镜像 Python fixture）。
func newEnv(t *testing.T, plain []byte) *env {
	t.Helper()
	root := t.TempDir()
	backend, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New(testPW)
	uploadPlain(t, sess, backend, plain, "media/video.cpenc")

	srv := NewServer(sess, backend)
	entry, err := srv.RegisterStream(context.Background(), "media/video.cpenc", "视频 测试.mp4")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &env{sess: sess, backend: backend, root: root, srv: srv, ts: ts, entry: entry}
}

func (e *env) url() string { return e.ts.URL + e.entry.URLPath() }

// get 发起 GET 返回 (状态, 体, 响应头)；4xx/5xx 也原样返回（Go 客户端
// 对非 2xx 不报错）。
func get(t *testing.T, url string, headers map[string]string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body, resp.Header
}

func head(t *testing.T, url string) (int, http.Header) {
	t.Helper()
	resp, err := http.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header
}

// ---------------------------------------------------------------------------
// Range 头解析（镜像 Python TestParseRangeHeader 八例）
// ---------------------------------------------------------------------------

func TestParseRangeHeader(t *testing.T) {
	cases := []struct {
		name   string
		header string
		total  int64
		want   ByteRange
		wantOK bool
	}{
		{"no-header", "", 1000, ByteRange{0, 999}, true},
		{"explicit", "bytes=100-199", 1000, ByteRange{100, 199}, true},
		{"open-ended", "bytes=100-", 1000, ByteRange{100, 999}, true},
		{"end-beyond", "bytes=900-5000", 1000, ByteRange{900, 999}, true},
		{"start-beyond", "bytes=2000-", 1000, ByteRange{}, false},
		{"start-after-end", "bytes=500-100", 1000, ByteRange{}, false},
		{"zero-total", "", 0, ByteRange{0, 0}, true},
		{"invalid-format", "items=1-5", 1000, ByteRange{0, 999}, true},
		{"suffix-form", "bytes=-100", 1000, ByteRange{0, 100}, true}, // Python 怪癖：视作 0-100
		{"huge-start-overflow", "bytes=99999999999999999999-", 1000, ByteRange{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseRangeHeader(c.header, c.total)
			if ok != c.wantOK || ok && got != c.want {
				t.Errorf("ParseRangeHeader(%q, %d) = %+v/%v，期望 %+v/%v",
					c.header, c.total, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 代理集成（镜像 Python TestProxyIntegration）
// ---------------------------------------------------------------------------

func TestGetFullFile206(t *testing.T) {
	plain := testPlain(20) // 5120 B
	e := newEnv(t, plain)
	status, data, h := get(t, e.url(), nil)
	if status != http.StatusPartialContent {
		t.Fatalf("状态应为 206，实得 %d", status)
	}
	if string(data) != string(plain) {
		t.Fatal("全文件响应与明文不一致")
	}
	if h.Get("Content-Length") != fmt.Sprint(len(plain)) {
		t.Errorf("Content-Length = %q", h.Get("Content-Length"))
	}
	if h.Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges = %q", h.Get("Accept-Ranges"))
	}
	if h.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", h.Get("Cache-Control"))
	}
}

func TestGetRange(t *testing.T) {
	plain := testPlain(20)
	e := newEnv(t, plain)
	status, data, h := get(t, e.url(), map[string]string{"Range": "bytes=100-299"})
	if status != http.StatusPartialContent {
		t.Fatalf("状态应为 206，实得 %d", status)
	}
	if string(data) != string(plain[100:300]) {
		t.Fatal("Range 响应与明文区间不一致")
	}
	if cr := h.Get("Content-Range"); cr != fmt.Sprintf("bytes 100-299/%d", len(plain)) {
		t.Errorf("Content-Range = %q", cr)
	}
}

func TestGetVariousRanges(t *testing.T) {
	plain := testPlain(20) // 5120 B
	e := newEnv(t, plain)
	cases := [][2]int{{0, 0}, {0, 15}, {0, 16}, {15, 16}, {1000, 1500}, {5119, 5119}, {5100, 5119}}
	for _, c := range cases {
		status, data, _ := get(t, e.url(), map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", c[0], c[1])})
		if status != http.StatusPartialContent {
			t.Errorf("[%d-%d] 状态应为 206，实得 %d", c[0], c[1], status)
			continue
		}
		if string(data) != string(plain[c[0]:c[1]+1]) {
			t.Errorf("[%d-%d] 响应与明文不一致", c[0], c[1])
		}
	}
}

func TestGetOpenEndedRange(t *testing.T) {
	plain := testPlain(20)
	e := newEnv(t, plain)
	status, data, _ := get(t, e.url(), map[string]string{"Range": "bytes=5000-"})
	if status != http.StatusPartialContent || string(data) != string(plain[5000:]) {
		t.Fatalf("开口区间错误：status=%d len=%d", status, len(data))
	}
}

func TestHeadReturnsPlaintextSize(t *testing.T) {
	plain := testPlain(20)
	e := newEnv(t, plain)
	status, h := head(t, e.url())
	if status != http.StatusOK {
		t.Fatalf("HEAD 状态应为 200，实得 %d", status)
	}
	if h.Get("Content-Length") != fmt.Sprint(len(plain)) {
		t.Errorf("HEAD Content-Length = %q，期望 %d", h.Get("Content-Length"), len(plain))
	}
	if h.Get("Accept-Ranges") != "bytes" {
		t.Errorf("HEAD Accept-Ranges = %q", h.Get("Accept-Ranges"))
	}
}

func TestMIMEBasedOnDisplayName(t *testing.T) {
	plain := testPlain(1)
	e := newEnv(t, plain) // 注册名「视频 测试.mp4」
	_, _, h := get(t, e.url(), nil)
	if ct := h.Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("Content-Type = %q，期望 video/mp4（按展示名推断）", ct)
	}
	if got := MIMEForDisplayName("clip.mkv"); got != "video/x-matroska" {
		t.Errorf(".mkv = %q", got)
	}
	if got := MIMEForDisplayName("song.flac"); got != "audio/flac" {
		t.Errorf(".flac = %q", got)
	}
	if got := MIMEForDisplayName("noext"); got != "application/octet-stream" {
		t.Errorf("无扩展名 = %q", got)
	}
	if got := MIMEForDisplayName("中文名.MP4"); got != "video/mp4" {
		t.Errorf("大写扩展名 = %q", got)
	}
}

func TestUnknownToken404(t *testing.T) {
	e := newEnv(t, testPlain(1))
	status, body, _ := get(t, e.ts.URL+"/s/0123456789abcdef0123456789abcdef/x.mp4", nil)
	if status != http.StatusNotFound {
		t.Fatalf("未知令牌应 404，实得 %d", status)
	}
	if string(body) != "not found" {
		t.Errorf("404 体 = %q", body)
	}
}

func TestKindMismatch404(t *testing.T) {
	e := newEnv(t, testPlain(1))
	thumb, err := e.srv.RegisterThumb("media/thumb.png", "thumb.png", []byte("jpeg"), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	// 用 thumb 令牌打 /s/ → 404
	status, _, _ := get(t, e.ts.URL+"/s/"+thumb.Token+"/t.png", nil)
	if status != http.StatusNotFound {
		t.Fatalf("类型不匹配应 404，实得 %d", status)
	}
}

func TestRangeBeyondTotal416(t *testing.T) {
	plain := testPlain(20)
	e := newEnv(t, plain)
	status, _, h := get(t, e.url(), map[string]string{"Range": fmt.Sprintf("bytes=%d-", len(plain)+100)})
	if status != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("越界 Range 应 416，实得 %d", status)
	}
	if cr := h.Get("Content-Range"); cr != fmt.Sprintf("bytes */%d", len(plain)) {
		t.Errorf("416 Content-Range = %q", cr)
	}
}

func TestEmptyFile206(t *testing.T) {
	e := newEnv(t, []byte{}) // 空明文 → 容器仅文件头
	status, data, h := get(t, e.url(), nil)
	if status != http.StatusPartialContent {
		t.Fatalf("空文件应 206，实得 %d", status)
	}
	if len(data) != 0 {
		t.Errorf("空文件体应为空，实得 %d 字节", len(data))
	}
	if cr := h.Get("Content-Range"); cr != "bytes 0-0/0" {
		t.Errorf("空文件 Content-Range = %q", cr)
	}
}

func TestNoPlaintextLeaked(t *testing.T) {
	plain := testPlain(20)
	e := newEnv(t, plain)
	// 先请求一段，确保代理实际解密过
	if _, _, _ = get(t, e.url(), map[string]string{"Range": "bytes=0-99"}); true {
	}
	// 扫描后端根：只有密文，内容不含明文片段（Python 同款断言）
	err := filepath.Walk(e.root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		content, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if string(content) == string(plain) {
			t.Errorf("发现完整明文泄露：%s", p)
		}
		if strings.Contains(string(content), string(plain[:256])) {
			t.Errorf("密文含明文前缀：%s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// 缩略图端点 /t/
// ---------------------------------------------------------------------------

func TestThumbEndpoint(t *testing.T) {
	e := newEnv(t, testPlain(1))
	payload := []byte{0xFF, 0xD8, 1, 2, 3} // 示意 JPEG
	thumb, err := e.srv.RegisterThumb("media/video.cpenc", "视频 测试.mp4", payload, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	status, data, h := get(t, e.ts.URL+thumb.URLPath(), nil)
	if status != http.StatusOK {
		t.Fatalf("缩略图应 200，实得 %d", status)
	}
	if string(data) != string(payload) {
		t.Fatal("缩略图体与注入不一致")
	}
	if h.Get("Content-Type") != "image/jpeg" {
		t.Errorf("Content-Type = %q", h.Get("Content-Type"))
	}
}

// ---------------------------------------------------------------------------
// 大文件：2MiB 截断 + 多段拼接 == 原文
// ---------------------------------------------------------------------------

// TestLargeFileCapAndStitch 明文 3MiB：开口区间单次 ≤2MiB，
// 按 Content-Range 续请逐段拼接后与原文一致（Python 两组用例合并）。
func TestLargeFileCapAndStitch(t *testing.T) {
	plain := testPlain(3 * 4096) // 3 MiB（testPlain: n×256B）
	e := newEnv(t, plain)
	url := e.url()

	// 开口区间：单段不超过 MaxResponseBytes
	status, data, h := get(t, url, map[string]string{"Range": "bytes=0-"})
	if status != http.StatusPartialContent {
		t.Fatalf("状态应为 206，实得 %d", status)
	}
	if len(data) != MaxResponseBytes {
		t.Fatalf("开口区间应截断到 %d，实得 %d", MaxResponseBytes, len(data))
	}
	if string(data) != string(plain[:MaxResponseBytes]) {
		t.Fatal("首段与明文不一致")
	}
	if cr := h.Get("Content-Range"); cr != fmt.Sprintf("bytes 0-%d/%d", MaxResponseBytes-1, len(plain)) {
		t.Errorf("Content-Range = %q", cr)
	}

	// 续请拼接：按 Content-Range 推进（播放器续请依据）
	got := make([]byte, 0, len(plain))
	pos := 0
	for pos < len(plain) {
		status, data, h := get(t, url, map[string]string{"Range": fmt.Sprintf("bytes=%d-", pos)})
		if status != http.StatusPartialContent {
			t.Fatalf("[%d-] 状态应为 206，实得 %d", pos, status)
		}
		got = append(got, data...)
		// 解析 Content-Range 的 end 段推进
		cr := h.Get("Content-Range") // bytes s-e/total
		var s, end, total int64
		if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &s, &end, &total); err != nil {
			t.Fatalf("Content-Range 解析失败 %q: %v", cr, err)
		}
		if s != int64(pos) {
			t.Fatalf("Content-Range 起点 %d ≠ 请求起点 %d", s, pos)
		}
		pos = int(end) + 1
		if len(got) != pos {
			t.Fatalf("拼接长度 %d ≠ 期望 %d", len(got), pos)
		}
	}
	if string(got) != string(plain) {
		t.Fatal("多段拼接与完整明文不一致")
	}
}

// ---------------------------------------------------------------------------
// 注册语义与后端故障
// ---------------------------------------------------------------------------

func TestRegisterIdempotentAndRevoke(t *testing.T) {
	e := newEnv(t, testPlain(1))
	// 同路径重复注册 → 同 token（幂等）
	again, err := e.srv.RegisterStream(context.Background(), "media/video.cpenc", "别的名字.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if again.Token != e.entry.Token {
		t.Errorf("幂等注册应返回同 token，实得 %s != %s", again.Token, e.entry.Token)
	}
	// Revoke 后令牌失效
	e.srv.Revoke(e.entry.Token)
	status, _, _ := get(t, e.url(), nil)
	if status != http.StatusNotFound {
		t.Fatalf("Revoke 后应 404，实得 %d", status)
	}
	if e.srv.Registry().Len() != 0 {
		t.Errorf("注册表应清空，实得 %d", e.srv.Registry().Len())
	}
}

func TestRegisterMissingFile(t *testing.T) {
	e := newEnv(t, testPlain(1))
	_, err := e.srv.RegisterStream(context.Background(), "media/nope.cpenc", "nope.mp4")
	if err == nil || !strings.Contains(err.Error(), "路径不存在") {
		t.Fatalf("缺失文件注册应报路径不存在，实得 %v", err)
	}
}

func TestRegisterNonVaultFile(t *testing.T) {
	e := newEnv(t, testPlain(1))
	// 明文伪装成密文上传 → 注册应报「不是合法加密容器」
	plain := testPlain(1)
	src := filepath.Join(t.TempDir(), "fake.bin")
	if err := os.WriteFile(src, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.backend.UploadChunked(context.Background(), src, "media/fake.cpenc", 0, nil); err != nil {
		t.Fatal(err)
	}
	_, err := e.srv.RegisterStream(context.Background(), "media/fake.cpenc", "fake.mp4")
	if err == nil || !strings.Contains(err.Error(), "不是合法的加密容器") {
		t.Fatalf("非密文注册应报容器错误，实得 %v", err)
	}
}

func TestBackendFailureAfterRegister502(t *testing.T) {
	plain := testPlain(20)
	e := newEnv(t, plain)
	// 注册后删除远端文件（模拟后端文件被移走）→ 首窗口拉取失败 → 502
	if err := e.backend.Delete(context.Background(), "media/video.cpenc"); err != nil {
		t.Fatal(err)
	}
	status, body, _ := get(t, e.url(), nil)
	if status != http.StatusBadGateway {
		t.Fatalf("后端故障应 502，实得 %d", status)
	}
	if string(body) != "backend error" {
		t.Errorf("502 体 = %q", body)
	}
}

// ---------------------------------------------------------------------------
// 并发与客户端中断
// ---------------------------------------------------------------------------

func TestConcurrentRanges(t *testing.T) {
	plain := testPlain(20)
	e := newEnv(t, plain)
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			start := seed * 400
			end := start + 700
			if end >= len(plain) {
				end = len(plain) - 1
			}
			status, data, _ := get(t, e.url(), map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", start, end)})
			if status != http.StatusPartialContent || string(data) != string(plain[start:end+1]) {
				errCh <- fmt.Errorf("[%d-%d] 并发响应错误: status=%d len=%d", start, end, status, len(data))
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// slowBackend 在下载路径注入延迟（客户端中断用例用），计数窗口请求。
type slowBackend struct {
	storage.Backend
	delay time.Duration
	calls atomic.Int64
}

func (s *slowBackend) DownloadRange(ctx context.Context, path string, start, end int64) ([]byte, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	return s.Backend.DownloadRange(ctx, path, start, end)
}

// TestClientAbortSilent 客户端中途断开：服务端写错误静默退出、不 panic、
// 后续请求不受影响（播放器 seek/切文件常态）。
func TestClientAbortSilent(t *testing.T) {
	plain := testPlain(2 * 4096) // 2 MiB → 8 个 256KiB 窗口
	backend, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New(testPW)
	uploadPlain(t, sess, backend, plain, "media/big.cpenc")
	slow := &slowBackend{Backend: backend, delay: 60 * time.Millisecond}
	srv := NewServer(sess, slow)
	entry, err := srv.RegisterStream(context.Background(), "media/big.cpenc", "big.mp4")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	addr := strings.TrimPrefix(ts.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	// 手写请求开口区间，收 1KiB 即断开
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nRange: bytes=0-\r\n\r\n", entry.URLPath(), addr)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	// 等服务端写完剩余窗口（应静默失败而非 panic/污染后续）
	deadline := time.Now().Add(3 * time.Second)
	for slow.calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	// 服务端应仍健康：正常小请求 206
	status, data, _ := get(t, ts.URL+entry.URLPath(), map[string]string{"Range": "bytes=0-99"})
	if status != http.StatusPartialContent || len(data) != 100 {
		t.Fatalf("中断后服务端不健康: status=%d len=%d", status, len(data))
	}
}
