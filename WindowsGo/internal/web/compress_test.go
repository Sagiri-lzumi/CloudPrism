package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

/* ------------------------------------------------------------ 纯函数单测 */

func TestCompressible(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"application/javascript", true},
		{"application/javascript; charset=utf-8", true},
		{"text/css", true},
		{"text/html; charset=utf-8", true},
		{"application/json", true},
		{"image/svg+xml", true},
		{"APPLICATION/JAVASCRIPT", true}, // 大小写不敏感
		{"text/event-stream", false},     // SSE 必须逐帧透传
		{"", false},                      // 空类型不敢妄断
		{"image/png", false},
		{"font/woff2", false}, // 已自带压缩
		{"video/mp4", false},
	}
	for _, c := range cases {
		if got := compressible(c.ct); got != c.want {
			t.Errorf("compressible(%q) = %v, want %v", c.ct, got, c.want)
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"gzip", true},
		{"gzip, deflate, br", true},
		{"deflate, gzip;q=1.0", true},
		{"*", true},
		{"GZIP", true},
		{"", false},
		{"deflate, br", false},
		{"gzip;q=0", false},          // 显式拒绝
		{"deflate, gzip;q=0", false}, // 其它编码接受但 gzip 被拒
		{"gzip;q=0.5", true},         // 只要不是 0 就接受
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		if c.header != "" {
			r.Header.Set("Accept-Encoding", c.header)
		}
		if got := acceptsGzip(r); got != c.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}

/* ------------------------------------------------------ 中间件行为（桩 handler） */

// gunzipAll 解出 gzip 正文，便于与原文逐字节比对。
func gunzipAll(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("响应不是合法 gzip 流：%v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("解压失败：%v", err)
	}
	return string(out)
}

// runGzip 用给定的响应头/请求头跑一次 withGzip，返回 recorder 与原始正文。
func runGzip(t *testing.T, contentType, acceptEncoding, rangeHdr string, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = w.Write([]byte(body))
	})
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if acceptEncoding != "" {
		r.Header.Set("Accept-Encoding", acceptEncoding)
	}
	if rangeHdr != "" {
		r.Header.Set("Range", rangeHdr)
	}
	rec := httptest.NewRecorder()
	withGzip(h).ServeHTTP(rec, r)
	return rec, body
}

func TestWithGzipCompressesText(t *testing.T) {
	body := strings.Repeat("console.log('cloudprism');\n", 500)
	rec, _ := runGzip(t, "text/javascript", "gzip", "", body)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, 应包含 Accept-Encoding", got)
	}
	// Content-Length 必须删掉：它记录的是压缩前长度，留着会让客户端截断
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, 压缩后必须删除", got)
	}
	if got := gunzipAll(t, rec.Body.Bytes()); got != body {
		t.Errorf("解压后正文与原文不一致（长度 %d vs %d）", len(got), len(body))
	}
	if rec.Body.Len() >= len(body) {
		t.Errorf("压缩后反而更大：%d >= %d", rec.Body.Len(), len(body))
	}
}

func TestWithGzipSkipsUncompressibleType(t *testing.T) {
	body := strings.Repeat("\x89PNG\r\n", 200)
	rec, _ := runGzip(t, "image/png", "gzip", "", body)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("图片不该被压缩，Content-Encoding = %q", got)
	}
	if rec.Body.String() != body {
		t.Errorf("正文被改动")
	}
}

func TestWithGzipSkipsEventStream(t *testing.T) {
	rec, _ := runGzip(t, "text/event-stream", "gzip", "", "data: {}\n\n")

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("SSE 不该被压缩，Content-Encoding = %q", got)
	}
}

func TestWithGzipSkipsWhenClientRefuses(t *testing.T) {
	body := strings.Repeat("a", 4096)

	for _, ae := range []string{"", "deflate, br", "gzip;q=0"} {
		rec, _ := runGzip(t, "text/css", ae, "", body)
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("Accept-Encoding=%q 时不该压缩，Content-Encoding = %q", ae, got)
		}
		if rec.Body.String() != body {
			t.Errorf("Accept-Encoding=%q 时正文被改动", ae)
		}
	}
}

func TestWithGzipSkipsRangeRequest(t *testing.T) {
	body := strings.Repeat("b", 4096)
	rec, _ := runGzip(t, "text/css", "gzip", "bytes=0-99", body)

	// Range 依赖原始字节偏移，压缩会破坏 Content-Range 语义，必须原样放行
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("带 Range 的请求不该压缩，Content-Encoding = %q", got)
	}
	if rec.Body.String() != body {
		t.Errorf("带 Range 的请求正文被改动")
	}
}

func TestWithGzipSkipsAlreadyEncoded(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Content-Encoding", "br")
		_, _ = w.Write([]byte("fake-brotli"))
	})
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	withGzip(h).ServeHTTP(rec, r)

	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Errorf("不得二次压缩，Content-Encoding = %q", got)
	}
}

func TestWithGzipPassesThroughNotModified(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.WriteHeader(http.StatusNotModified)
	})
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	withGzip(h).ServeHTTP(rec, r)

	// 304 无正文，加 Content-Encoding 只会让客户端误以为有压缩体
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("304 不该带 Content-Encoding，得到 %q", got)
	}
	if rec.Code != http.StatusNotModified {
		t.Errorf("状态码 = %d, want 304", rec.Code)
	}
}

/* ------------------------------------------- 集成：真实静态路由 + fstest 文件树 */

// newStaticMux 用内存文件树装配真实 registerStatic，走完整 FileServer 链路。
func newStaticMux(t *testing.T, files map[string]string) *http.ServeMux {
	t.Helper()
	m := fstest.MapFS{}
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	mux := http.NewServeMux()
	(&Server{distFS: m}).registerStatic(mux)
	return mux
}

func TestRegisterStaticGzipsBuildAssets(t *testing.T) {
	js := strings.Repeat("export const x=1;\n", 400)
	mux := newStaticMux(t, map[string]string{
		"index.html":     "<!doctype html><html><body>app</body></html>",
		"assets/app.js":  js,
		"assets/pic.png": strings.Repeat("\x89PNG", 100),
	})

	// 带 hash 的构建产物：gzip + 强缓存 + 撤掉 Range 广告
	r := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("JS 未压缩，Content-Encoding = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("带 hash 产物应强缓存，Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "" {
		t.Errorf("压缩后应撤掉 Accept-Ranges，得到 %q", got)
	}
	if got := gunzipAll(t, rec.Body.Bytes()); got != js {
		t.Errorf("JS 解压后内容不一致")
	}
}

func TestRegisterStaticHtmlIsNeverImmutable(t *testing.T) {
	mux := newStaticMux(t, map[string]string{
		"index.html": "<!doctype html><html><body>app</body></html>",
	})

	// 回归防护：.html 名字里没有内容 hash，曾被一并贴上 immutable 一年。
	// /index.html 实际会被 http.FileServer 301 到 ./，这条 301 带上 immutable
	// 就会被浏览器当永久重定向缓存；非 index 的 .html 更会被直接服务并钉死。
	for _, path := range []string{"/", "/index.html"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)

		got := rec.Header().Get("Cache-Control")
		if strings.Contains(got, "immutable") {
			t.Errorf("GET %s 的 Cache-Control = %q，HTML 绝不能 immutable", path, got)
		}
		if !strings.Contains(got, "no-store") {
			t.Errorf("GET %s 的 Cache-Control = %q，应为 no-store", path, got)
		}
	}
}

func TestRegisterStaticFallsBackToIndex(t *testing.T) {
	mux := newStaticMux(t, map[string]string{
		"index.html": "<!doctype html><html><body>spa</body></html>",
	})

	// 未知路径（SPA 历史路由）应回退 index.html，而不是 404
	r := httptest.NewRequest(http.MethodGet, "/settings/lan", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "spa") {
		t.Errorf("未回退 index.html，正文 = %q", rec.Body.String())
	}
}
