package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// 进程内 WebDAV 假服务器
// ---------------------------------------------------------------------------

// mockWebDAV 是内存 WebDAV 服务器，语义对齐 Python 测试的
// MockWebDavAdapter（test_webdav_backend.py:25-202），两处更贴近真实服务器：
//
//   - PROPFIND 响应把「自身条目」也放进 multistatus（Nextcloud 等真实服务器
//     都这么做），href 用完整 URL —— Python mock 跳过自身且 href 是纯相对
//     路径，Go 端故意模拟真实形态，才能测出「跳过自身」的解析逻辑；
//   - DELETE/MOVE 对不存在的目标回 404（Python mock 恒 204/201 静默成功），
//     用于测 Go 后端的幂等与错误分支。
type mockWebDAV struct {
	mu      sync.Mutex
	files   map[string][]byte // key 形如 "/a/b"
	dirs    map[string]bool   // "/" 恒存在
	noRange bool              // 忽略 Range 头（模拟不支持 Range 的服务器）
	failGET bool              // GET 恒回 500（模拟服务端故障）
	putN    int               // PUT 次数统计（断言「已完整时不发请求」用）
}

func newMockWebDAV(noRange bool) *mockWebDAV {
	return &mockWebDAV{
		files:   map[string][]byte{},
		dirs:    map[string]bool{"/": true},
		noRange: noRange,
	}
}

// normalize 把 URL 路径规范成 "/x/y" 形态（已解码）。
func (m *mockWebDAV) normalize(p string) string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return "/"
	}
	return "/" + trimmed
}

// childInfo 记录 PROPFIND 子项的目录/文件身份（生成 XML 必须区分，
// 否则 resourcetype 与 getcontentlength 全空，后端解析出的都是 size=0 的文件）。
type childInfo struct {
	isDir bool
	size  int
}

// childOf 判断 sub 是否为 parent 的直接子项。
func childOf(sub, parent string) bool {
	prefix := strings.TrimSuffix(parent, "/") + "/"
	if !strings.HasPrefix(sub, prefix) {
		return false
	}
	return !strings.Contains(strings.TrimPrefix(sub, prefix), "/")
}

// serveHTTP 分发各方法。自身条目 href 用请求的 Host 拼完整 URL。
func (m *mockWebDAV) serveHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	base := "http://" + r.Host
	path := m.normalize(r.URL.Path)
	switch r.Method {
	case "PROPFIND":
		if !m.dirs[path] && !m.hasFile(path) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`)
		io.WriteString(w, `<D:multistatus xmlns:D="DAV:">`)
		// 自身条目（真实服务器形态：完整 URL href + collection 类型）
		selfRel := strings.Trim(path, "/")
		selfHref := base + "/" + quoteSegment(selfRel)
		fmt.Fprintf(w,
			`<D:response><D:href>%s</D:href><D:propstat><D:prop>`+
				`<D:resourcetype><D:collection/></D:resourcetype>`+
				`<D:getcontentlength>0</D:getcontentlength></D:prop>`+
				`<D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`,
			selfHref)
		// 直接子项：文件名与目录名合并，保留类型与大小
		children := map[string]childInfo{}
		for p, data := range m.files {
			if childOf(p, path) {
				children[strings.TrimPrefix(p, strings.TrimSuffix(path, "/")+"/")] = childInfo{size: len(data)}
			}
		}
		for p := range m.dirs {
			if childOf(p, path) && p != path {
				name := strings.TrimPrefix(p, strings.TrimSuffix(path, "/")+"/")
				if _, dup := children[name]; !dup {
					children[name] = childInfo{isDir: true}
				}
			}
		}
		for _, name := range sortedKeysOf(children) {
			info := children[name]
			href := base + "/" + quoteSegment(name)
			rtype := ""
			size := ""
			if info.isDir {
				rtype = "<D:collection/>"
			} else {
				size = strconv.Itoa(info.size)
			}
			fmt.Fprintf(w,
				`<D:response><D:href>%s</D:href><D:propstat><D:prop>`+
					`<D:resourcetype>%s</D:resourcetype>`+
					`<D:getcontentlength>%s</D:getcontentlength></D:prop>`+
					`<D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`,
				href, rtype, size)
		}
		io.WriteString(w, `</D:multistatus>`)

	case "HEAD":
		if data, ok := m.files[path]; ok {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			return
		}
		if m.dirs[path] {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)

	case "GET":
		if m.failGET {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		data, ok := m.files[path]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if m.noRange {
			r.Header.Del("Range") // 模拟不支持 Range 的服务器
		}
		if spec := r.Header.Get("Range"); spec != "" {
			body := spec[len("bytes="):]
			parts := strings.SplitN(body, "-", 2)
			s, _ := strconv.Atoi(parts[0])
			e := len(data) - 1
			if parts[1] != "" {
				e, _ = strconv.Atoi(parts[1])
			}
			e = min(e, len(data)-1)
			chunk := data[s : e+1]
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, len(data)))
			w.Header().Set("Content-Length", strconv.Itoa(len(chunk)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(chunk)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(data)

	case "PUT":
		m.putN++
		body, _ := io.ReadAll(r.Body)
		cr := r.Header.Get("Content-Range")
		m.ensureParents(path)
		if cr != "" {
			// 分块续传：bytes s-e/total，不足补零后按偏移覆写
			parts := strings.SplitN(cr[len("bytes "):], "/", 2)
			se := strings.SplitN(parts[0], "-", 2)
			s, _ := strconv.Atoi(se[0])
			cur := []byte(m.files[path])
			if s > len(cur) {
				cur = append(cur, make([]byte, s-len(cur))...)
			}
			if need := s + len(body); need > len(cur) {
				cur = append(cur, make([]byte, need-len(cur))...)
			}
			copy(cur[s:], body)
			m.files[path] = cur
		} else {
			m.files[path] = body
		}
		delete(m.dirs, path)
		w.WriteHeader(http.StatusCreated)

	case "MKCOL":
		if m.dirs[path] {
			w.WriteHeader(http.StatusMethodNotAllowed) // 405 = 已存在
			return
		}
		m.ensureParents(path)
		m.dirs[path] = true
		delete(m.files, path)
		w.WriteHeader(http.StatusCreated)

	case "MOVE":
		dest := r.Header.Get("Destination")
		du, _ := url.Parse(dest)
		newPath := m.normalize(du.Path)
		if data, ok := m.files[path]; ok {
			m.files[newPath] = data
			delete(m.files, path)
			w.WriteHeader(http.StatusCreated)
			return
		}
		if m.dirs[path] {
			delete(m.dirs, path)
			m.dirs[newPath] = true
			w.WriteHeader(http.StatusCreated)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)

	case "DELETE":
		if _, ok := m.files[path]; ok {
			delete(m.files, path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if m.dirs[path] && path != "/" {
			delete(m.dirs, path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *mockWebDAV) hasFile(p string) bool {
	_, ok := m.files[p]
	return ok
}

// ensureParents 自动补建父目录（对照 MockWebDavAdapter._ensure_parents）。
func (m *mockWebDAV) ensureParents(path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	cur := ""
	for _, part := range parts[:max(len(parts)-1, 0)] {
		cur += "/" + part
		m.dirs[cur] = true
	}
}

// newWebDAVPair 建假服务器 + 后端，返回 (后端, 假服务器状态)。
func newWebDAVPair(t *testing.T, noRange bool) (*WebDAV, *mockWebDAV) {
	t.Helper()
	m := newMockWebDAV(noRange)
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	t.Cleanup(srv.Close)
	b, err := NewWebDAV(srv.URL, "user", "pw")
	if err != nil {
		t.Fatalf("NewWebDAV 失败: %v", err)
	}
	return b, m
}

// sortedKeysOf 稳定输出 map 键序（PROPFIND 响应顺序要求确定，测试才好断言）。
func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// quoteSegment —— 与 Python urllib.parse.quote 逐字节对齐
// ---------------------------------------------------------------------------

func TestQuoteSegmentMatchesPython(t *testing.T) {
	// 期望值由 WindowsPy 环境实测 urllib.parse.quote 得到
	cases := []struct {
		in, want string
	}{
		{"plain.cpenc", "plain.cpenc"},
		{"my dir/file", "my%20dir/file"}, // '/' 是 quote 默认 safe，保留
		{"中文字符", "%E4%B8%AD%E6%96%87%E5%AD%97%E7%AC%A6"},
		{"a@b:c$d", "a%40b%3Ac%24d"}, // 保留字符必须编码（与 PathEscape 不同！）
		{"a~b_.-c", "a~b_.-c"},       // unreserved 保留
		{"100%", "100%25"},
		{"a+b", "a%2Bb"},
		{"x?y#z", "x%3Fy%23z"},
		{"line\nfeed", "line%0Afeed"},
	}
	for _, tc := range cases {
		if got := quoteSegment(tc.in); got != tc.want {
			t.Errorf("quoteSegment(%q) = %q，应为 %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 列表
// ---------------------------------------------------------------------------

func TestWebDAVListDir(t *testing.T) {
	b, m := newWebDAVPair(t, false)
	ctx := context.Background()

	t.Run("空目录", func(t *testing.T) {
		entries, err := b.ListDir(ctx, "/")
		if err != nil {
			t.Fatalf("ListDir 失败: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("空目录应返回 0 条（自身条目被跳过），实得 %v", entries)
		}
	})

	t.Run("文件与目录并含自身跳过", func(t *testing.T) {
		m.mu.Lock()
		m.files["/a.cpenc"] = []byte("hello")
		m.files["/b.cpenc"] = []byte("world!!")
		m.dirs["/sub"] = true
		m.mu.Unlock()

		entries, err := b.ListDir(ctx, "/")
		if err != nil {
			t.Fatalf("ListDir 失败: %v", err)
		}
		byName := map[string]Entry{}
		for _, e := range entries {
			byName[e.Name] = e
		}
		// 假服务器把自身（根）放进 multistatus；若「跳过自身」逻辑失效，
		// 这里会出现一条名字为空或等于主机名的垃圾条目
		if len(byName) != 3 {
			t.Fatalf("期望 3 条（不含自身），实得 %v", entries)
		}
		if a := byName["a.cpenc"]; a.IsDir || a.Size != 5 {
			t.Errorf("a.cpenc = %+v，应为文件 5 字节", a)
		}
		if b2 := byName["b.cpenc"]; b2.IsDir || b2.Size != 7 {
			t.Errorf("b.cpenc = %+v，应为文件 7 字节", b2)
		}
		if !byName["sub"].IsDir {
			t.Error("sub 应被标为目录")
		}
	})

	t.Run("子目录列表同样跳过自身", func(t *testing.T) {
		m.mu.Lock()
		m.files["/sub/deep.bin"] = []byte("xyz")
		m.mu.Unlock()
		entries, err := b.ListDir(ctx, "sub")
		if err != nil {
			t.Fatalf("ListDir(sub) 失败: %v", err)
		}
		if len(entries) != 1 || entries[0].Name != "deep.bin" {
			t.Errorf("期望仅 deep.bin，实得 %v", entries)
		}
	})

	t.Run("中文目录名往返", func(t *testing.T) {
		m.mu.Lock()
		m.dirs["/中文目录"] = true
		m.files["/中文目录/密文文件.cpenc"] = []byte("data")
		m.mu.Unlock()
		// 先列根：应出现「中文目录」（从 %XX 编码的 href 解码而来）
		entries, err := b.ListDir(ctx, "/")
		if err != nil {
			t.Fatalf("ListDir 失败: %v", err)
		}
		found := false
		for _, e := range entries {
			if e.Name == "中文目录" {
				found = true
			}
		}
		if !found {
			t.Errorf("根目录列表未含「中文目录」：%v", entries)
		}
		// 再进子目录：请求 URL 编码、服务器解码、响应再解码回来
		inner, err := b.ListDir(ctx, "中文目录")
		if err != nil {
			t.Fatalf("ListDir(中文目录) 失败: %v", err)
		}
		if len(inner) != 1 || inner[0].Name != "密文文件.cpenc" {
			t.Errorf("中文路径下列表 = %v", inner)
		}
	})

	t.Run("路径不存在", func(t *testing.T) {
		if _, err := b.ListDir(ctx, "/nope"); !errors.Is(err, ErrBackend) {
			t.Errorf("期望 ErrBackend（Python ConnectionError），实得 %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// 大小与存在性
// ---------------------------------------------------------------------------

func TestWebDAVGetSizeAndExists(t *testing.T) {
	b, m := newWebDAVPair(t, false)
	ctx := context.Background()

	t.Run("正常取大小", func(t *testing.T) {
		m.mu.Lock()
		m.files["/f.bin"] = bytes.Repeat([]byte{0}, 123)
		m.mu.Unlock()
		if n, err := b.GetSize(ctx, "f.bin"); err != nil || n != 123 {
			t.Errorf("GetSize = (%d, %v)，应为 (123, nil)", n, err)
		}
		if n, err := b.Head(ctx, "f.bin"); err != nil || n != 123 {
			t.Errorf("Head = (%d, %v)，应为 (123, nil)", n, err)
		}
	})

	// HEAD 的 404 归 ErrBackend（Python ConnectionError）：HEAD 无法区分
	// 「不存在」与「其它失败」，语义上不存在判定走 Exists
	t.Run("不存在归 ErrBackend", func(t *testing.T) {
		if _, err := b.GetSize(ctx, "nope.bin"); !errors.Is(err, ErrBackend) {
			t.Errorf("期望 ErrBackend，实得 %v", err)
		}
	})

	t.Run("目录无 Content-Length 取 0", func(t *testing.T) {
		m.mu.Lock()
		m.dirs["/d"] = true
		m.mu.Unlock()
		if n, err := b.GetSize(ctx, "d"); err != nil || n != 0 {
			t.Errorf("目录大小 = (%d, %v)，应为 (0, nil)", n, err)
		}
	})

	t.Run("exists", func(t *testing.T) {
		m.mu.Lock()
		m.files["/x.bin"] = []byte("x")
		m.mu.Unlock()
		for _, tc := range []struct {
			path string
			want bool
		}{
			{"x.bin", true}, {"d", true}, {"nope", false},
		} {
			got, err := b.Exists(ctx, tc.path)
			if err != nil {
				t.Errorf("Exists(%q) 报错: %v", tc.path, err)
				continue
			}
			if got != tc.want {
				t.Errorf("Exists(%q) = %v，应为 %v", tc.path, got, tc.want)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Range 下载
// ---------------------------------------------------------------------------

func TestWebDAVDownloadRange(t *testing.T) {
	b, m := newWebDAVPair(t, false)
	ctx := context.Background()

	full := make([]byte, 256)
	for i := range full {
		full[i] = byte(i)
	}
	m.mu.Lock()
	m.files["/f.bin"] = full
	m.mu.Unlock()

	tests := []struct {
		name       string
		start, end int64
		want       []byte
	}{
		{"整段", 0, 255, full},
		{"部分", 10, 20, full[10:21]},
		{"单字节", 2, 2, full[2:3]},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := b.DownloadRange(ctx, "f.bin", tc.start, tc.end)
			if err != nil {
				t.Fatalf("DownloadRange(%d,%d) 失败: %v", tc.start, tc.end, err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("下载内容不一致（len=%d）", len(got))
			}
		})
	}

	t.Run("非法范围", func(t *testing.T) {
		if _, err := b.DownloadRange(ctx, "f.bin", 5, 3); !errors.Is(err, ErrRange) {
			t.Errorf("期望 ErrRange，实得 %v", err)
		}
	})

	t.Run("404 映射 ErrNotFound", func(t *testing.T) {
		_, err := b.DownloadRange(ctx, "nope.bin", 0, 10)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("期望 ErrNotFound（代理据此回 404），实得 %v", err)
		}
	})

	t.Run("服务端 500 映射 ErrBackend", func(t *testing.T) {
		b2, m2 := newWebDAVPair(t, false)
		m2.mu.Lock()
		m2.failGET = true
		m2.files["/f.bin"] = full
		m2.mu.Unlock()
		if _, err := b2.DownloadRange(ctx, "f.bin", 0, 10); !errors.Is(err, ErrBackend) {
			t.Errorf("期望 ErrBackend，实得 %v", err)
		}
	})
}

// TestWebDAVDownloadRange200Fallback 覆盖「服务器不支持 Range」的两条分支。
func TestWebDAVDownloadRange200Fallback(t *testing.T) {
	full := make([]byte, 256)
	for i := range full {
		full[i] = byte(i)
	}

	// 对照 test_webdav_backend.py 的 NoRangeAdapter：忽略 Range 头回 200 整文件
	t.Run("回退切片成功", func(t *testing.T) {
		b, m := newWebDAVPair(t, true) // noRange=true
		m.mu.Lock()
		m.files["/f.bin"] = full
		m.mu.Unlock()
		got, err := b.DownloadRange(context.Background(), "f.bin", 10, 20)
		if err != nil {
			t.Fatalf("失败: %v", err)
		}
		if !bytes.Equal(got, full[10:21]) {
			t.Error("200 回退未正确切片 —— 若原样返回整文件，调用方按请求偏移取密文会错位解密")
		}
	})

	// 文件比请求区间短：必须报错而非静默返回短段（否则错位解密）
	t.Run("回退但长度不足必须报错", func(t *testing.T) {
		b, m := newWebDAVPair(t, true)
		m.mu.Lock()
		m.files["/short.bin"] = full[:64]
		m.mu.Unlock()
		if _, err := b.DownloadRange(context.Background(), "short.bin", 50, 100); !errors.Is(err, ErrBackend) {
			t.Errorf("期望 ErrBackend（切片不足），实得 %v", err)
		}
	})

	// 大文件场景：NoRange 服务器 200 整文件回退时 Go 端只读 end+1 字节。
	// 用比请求区间大得多的文件验证正确性与不越界。
	t.Run("大文件回退只切所需段", func(t *testing.T) {
		big := bytes.Repeat([]byte{7}, 5<<20) // 5 MiB
		b, m := newWebDAVPair(t, true)
		m.mu.Lock()
		m.files["/big.bin"] = big
		m.mu.Unlock()
		got, err := b.DownloadRange(context.Background(), "big.bin", 1000, 1999)
		if err != nil {
			t.Fatalf("失败: %v", err)
		}
		if len(got) != 1000 || !bytes.Equal(got, bytes.Repeat([]byte{7}, 1000)) {
			t.Errorf("大文件回退切片错误（len=%d）", len(got))
		}
	})
}

// ---------------------------------------------------------------------------
// 上传
// ---------------------------------------------------------------------------

func TestWebDAVUploadChunked(t *testing.T) {
	b, m := newWebDAVPair(t, false)
	ctx := context.Background()
	tmp := t.TempDir()
	full := make([]byte, 256)
	for i := range full {
		full[i] = byte(i)
	}

	t.Run("整文件分块上传", func(t *testing.T) {
		src := writeRoot(t, tmp, "src1.bin", full)
		var seq []float64
		if err := b.UploadChunked(ctx, src, "dst1.bin", 64, func(p float64) {
			seq = append(seq, p)
		}); err != nil {
			t.Fatalf("上传失败: %v", err)
		}
		m.mu.Lock()
		got := m.files["/dst1.bin"]
		m.mu.Unlock()
		if !bytes.Equal(got, full) {
			t.Errorf("远端内容不一致（len=%d）", len(got))
		}
		if seq[len(seq)-1] != 1.0 {
			t.Errorf("末值应为 1.0，实得 %v", seq)
		}
		for i := 1; i < len(seq); i++ {
			if seq[i] < seq[i-1] {
				t.Errorf("进度回退：%v", seq)
				break
			}
		}
		// 256 字节 / 64 分块 = 4 次 PUT
		if seq != nil && len(seq) != 4 {
			t.Errorf("期望 4 次进度，实得 %d：%v", len(seq), seq)
		}
	})

	t.Run("空文件单次 PUT", func(t *testing.T) {
		src := writeRoot(t, tmp, "empty.bin", nil)
		var seq []float64
		if err := b.UploadChunked(ctx, src, "empty.bin", 0, func(p float64) {
			seq = append(seq, p)
		}); err != nil {
			t.Fatalf("上传失败: %v", err)
		}
		if len(seq) != 1 || seq[0] != 1.0 {
			t.Errorf("空文件应回调一次 1.0，实得 %v", seq)
		}
		m.mu.Lock()
		got := m.files["/empty.bin"]
		m.mu.Unlock()
		if len(got) != 0 {
			t.Errorf("远端应存空文件，实得 %d 字节", len(got))
		}
	})

	t.Run("断点续传", func(t *testing.T) {
		src := writeRoot(t, tmp, "src2.bin", full)
		m.mu.Lock()
		m.files["/resume.bin"] = append([]byte(nil), full[:100]...)
		m.mu.Unlock()
		if err := b.UploadChunked(ctx, src, "resume.bin", 64, nil); err != nil {
			t.Fatalf("续传失败: %v", err)
		}
		m.mu.Lock()
		got := m.files["/resume.bin"]
		m.mu.Unlock()
		if !bytes.Equal(got, full) {
			t.Errorf("续传结果与原文不一致（len=%d）", len(got))
		}
	})

	// 修正点（Python 缺陷）：远端已完整时 Python 不 yield，任务永不结束；
	// Go 端必须回调 1.0，且**不发任何请求**
	t.Run("远端已完整仅回调 1.0", func(t *testing.T) {
		src := writeRoot(t, tmp, "src3.bin", full)
		m.mu.Lock()
		m.files["/done.bin"] = append([]byte(nil), full...)
		before := m.putN
		m.mu.Unlock()
		var seq []float64
		if err := b.UploadChunked(ctx, src, "done.bin", 64, func(p float64) {
			seq = append(seq, p)
		}); err != nil {
			t.Fatalf("失败: %v", err)
		}
		if len(seq) != 1 || seq[0] != 1.0 {
			t.Errorf("应恰好回调一次 1.0，实得 %v", seq)
		}
		m.mu.Lock()
		after := m.putN
		m.mu.Unlock()
		if after != before {
			t.Errorf("远端已完整时不该发任何 PUT（%d → %d）", before, after)
		}
	})

	t.Run("自动创建父目录", func(t *testing.T) {
		src := writeRoot(t, tmp, "src4.bin", []byte("nested"))
		if err := b.UploadChunked(ctx, src, "a/b/c/deep.bin", 4, nil); err != nil {
			t.Fatalf("上传失败: %v", err)
		}
		m.mu.Lock()
		got := m.files["/a/b/c/deep.bin"]
		dirOK := m.dirs["/a"] && m.dirs["/a/b"] && m.dirs["/a/b/c"]
		m.mu.Unlock()
		if string(got) != "nested" || !dirOK {
			t.Errorf("嵌套上传失败：content=%q dirs=%v", got, dirOK)
		}
	})

	t.Run("进度含续传命中部分", func(t *testing.T) {
		src := writeRoot(t, tmp, "src5.bin", full)
		m.mu.Lock()
		m.files["/pct.bin"] = append([]byte(nil), full[:100]...)
		m.mu.Unlock()
		var seq []float64
		if err := b.UploadChunked(ctx, src, "pct.bin", 64, func(p float64) {
			seq = append(seq, p)
		}); err != nil {
			t.Fatalf("上传失败: %v", err)
		}
		// 首次回调应 > 100/256（含续传命中部分），Python 的
		// (offset+written)/total 语义；从 0 开始报会让进度条先倒退
		if len(seq) == 0 || seq[0] <= 100.0/256.0 {
			t.Errorf("首次进度应 > %f，实得 %v", 100.0/256.0, seq)
		}
	})

	t.Run("源文件不存在", func(t *testing.T) {
		err := b.UploadChunked(ctx, filepath.Join(tmp, "ghost.bin"), "x.bin", 64, nil)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("期望 ErrNotFound，实得 %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// MKCOL / MOVE / DELETE
// ---------------------------------------------------------------------------

func TestWebDAVManage(t *testing.T) {
	b, m := newWebDAVPair(t, false)
	ctx := context.Background()

	t.Run("MKCOL 多级且幂等", func(t *testing.T) {
		if err := b.Mkdir(ctx, "a/b/c"); err != nil {
			t.Fatalf("Mkdir 失败: %v", err)
		}
		m.mu.Lock()
		ok := m.dirs["/a/b/c"]
		m.mu.Unlock()
		if !ok {
			t.Fatal("MKCOL 后目录不存在")
		}
		// 405=已存在按幂等成功处理：同步任务重复 Mkdir 不应报错
		if err := b.Mkdir(ctx, "a/b/c"); err != nil {
			t.Errorf("重复 Mkdir 应幂等成功，实得 %v", err)
		}
	})

	t.Run("MOVE 重命名文件", func(t *testing.T) {
		m.mu.Lock()
		m.files["/old.bin"] = []byte("data")
		m.mu.Unlock()
		if err := b.Rename(ctx, "old.bin", "new.bin"); err != nil {
			t.Fatalf("Rename 失败: %v", err)
		}
		m.mu.Lock()
		_, oldGone := m.files["/old.bin"]
		got := m.files["/new.bin"]
		m.mu.Unlock()
		if oldGone {
			t.Error("源文件仍存在")
		}
		if string(got) != "data" {
			t.Errorf("目标内容 = %q", got)
		}
	})

	t.Run("MOVE 源不存在映射 ErrBackend", func(t *testing.T) {
		if err := b.Rename(ctx, "ghost", "x"); !errors.Is(err, ErrBackend) {
			t.Errorf("期望 ErrBackend，实得 %v", err)
		}
	})

	t.Run("DELETE 幂等", func(t *testing.T) {
		m.mu.Lock()
		m.files["/f.bin"] = []byte("x")
		m.mu.Unlock()
		if err := b.Delete(ctx, "f.bin"); err != nil {
			t.Fatalf("Delete 失败: %v", err)
		}
		m.mu.Lock()
		_, still := m.files["/f.bin"]
		m.mu.Unlock()
		if still {
			t.Error("文件未被删除")
		}
		// 404=已不存在按幂等成功处理（Python delete_missing_ok 语义）
		if err := b.Delete(ctx, "f.bin"); err != nil {
			t.Errorf("重复 Delete 应幂等成功，实得 %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// URL 构造（对齐 test_webdav_backend.py 的 TestWebDavBackendUrlEncoding）
// ---------------------------------------------------------------------------

func TestWebDAVRemoteURL(t *testing.T) {
	b, err := NewWebDAV("https://dav.example.com/remote.php/dav/files/user/", "u", "p")
	if err != nil {
		t.Fatalf("NewWebDAV 失败: %v", err)
	}

	t.Run("去掉尾斜杠", func(t *testing.T) {
		if b.BaseURL() != "https://dav.example.com/remote.php/dav/files/user" {
			t.Errorf("BaseURL = %q", b.BaseURL())
		}
	})

	t.Run("空格编码保留分隔", func(t *testing.T) {
		u := b.remoteURL("my dir/file name.cpenc")
		if strings.Contains(u, " ") {
			t.Errorf("URL 含未编码空格: %s", u)
		}
		if !strings.Contains(u, "%20") {
			t.Errorf("URL 未编码空格: %s", u)
		}
		// 段分隔符保留：期望形态为 …/my%20dir/file%20name.cpenc
		if !strings.HasSuffix(u, "/my%20dir/file%20name.cpenc") {
			t.Errorf("路径分隔符未保留: %s", u)
		}
		if !strings.HasPrefix(u, b.BaseURL()+"/") {
			t.Errorf("URL 前缀错误: %s", u)
		}
	})

	t.Run("空路径指向根", func(t *testing.T) {
		if got := b.remoteURL(""); got != b.BaseURL()+"/" {
			t.Errorf("remoteURL(\"\") = %q", got)
		}
		if got := b.remoteURL("/"); got != b.BaseURL()+"/" {
			t.Errorf("remoteURL(\"/\") = %q", got)
		}
	})

	t.Run("首尾斜杠可有可无", func(t *testing.T) {
		if a, c := b.remoteURL("a/b"), b.remoteURL("/a/b/"); a != c {
			t.Errorf("路径规范化不一致: %q vs %q", a, c)
		}
	})

	t.Run("非法地址被拒", func(t *testing.T) {
		for _, bad := range []string{"", "   ", "ftp://x", "notaurl", "http://"} {
			if _, err := NewWebDAV(bad, "u", "p"); !errors.Is(err, ErrBackend) {
				t.Errorf("NewWebDAV(%q) 期望 ErrBackend，实得 %v", bad, err)
			}
		}
	})
}
