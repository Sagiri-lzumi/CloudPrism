package storage

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// WebDAV 是 WebDAV 存储后端：PROPFIND 列目录、GET Range 下载、分块 PUT 上传。
//
// 全程手写 HTTP（不用 gowebdav 等现成库），因为库普遍不支持带 Content-Range
// 的分块 PUT —— 而断点续传语义依赖它（见 UploadChunked）。这也让状态码容错
// 可以逐字对齐 Python 端。
//
// 对照 WindowsPy/src/cloudprism/storage/webdav_backend.py
type WebDAV struct {
	baseURL string // 已去尾斜杠的 WebDAV 根地址
	user    string // Basic 认证用户名（可为空 = 匿名）
	pass    string // Basic 认证密码
	client  *http.Client
}

// 编译期断言。
var _ Backend = (*WebDAV)(nil)

// sharedTransport 是所有 WebDAV 后端共用的 HTTP 传输层。
//
// 参数对应 Python requests.Session 底层 urllib3 的常用配置。DisableCompression
// 关掉 Go 的透明 gzip：密文几乎不可压缩，压缩纯属浪费 CPU；且部分服务器对
// 带 Content-Range 的请求会在压缩时忽略 Range 返回整文件（200 回退分支
// 就是为这类服务器准备的，没必要主动制造它们）。
var sharedTransport = &http.Transport{
	MaxIdleConnsPerHost: 8, // 与 Python 契约测试对齐的保守值
	IdleConnTimeout:     90 * time.Second,
	DisableCompression:  true,
}

// NewWebDAV 构造 WebDAV 后端。
//
// baseURL 可以是「含用户路径段的根」（Nextcloud/ownCloud 的
// /remote.php/dav/files/<user>、坚果云 https://dav.jianguoyun.com/dav），
// 后端不做任何 WebDAV 能力探测 —— 那属于连接测试的职责。
func NewWebDAV(baseURL, user, pass string) (*WebDAV, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: WebDAV 地址为空", ErrBackend)
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: WebDAV 地址 %q 无法解析: %v", ErrBackend, baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: WebDAV 地址 %q 必须为 http(s)://", ErrBackend, baseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: WebDAV 地址 %q 缺少主机名", ErrBackend, baseURL)
	}

	return &WebDAV{
		baseURL: strings.TrimRight(trimmed, "/"),
		user:    user,
		pass:    pass,
		client:  &http.Client{Transport: sharedTransport},
	}, nil
}

// BaseURL 返回规范化后的 WebDAV 根地址（供连接信息展示与测试断言）。
func (b *WebDAV) BaseURL() string { return b.baseURL }

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

// quoteSegment 对路径段做百分号编码，语义与 Python 的
// urllib.parse.quote(seg) 一致：保留字母数字与 "-._~"（RFC 3986
// unreserved）**以及 '/'**（quote 的默认 safe 集合），其余字节一律编码为
// %HH（大写十六进制，UTF-8 逐字节）。
//
// 为什么不用 url.PathEscape：它按 RFC 保留 "sub-delims"（: @ & = + $ 等），
// 与 Python quote 不一致。目录条目名是密文 Base32（纯字母数字）时无差异，
// 但自建目录若含 '@' 之类字符，两端会对同一路径发出不同 URL —— 语义等价，
// 但对拍测试与「两端 URL 一致」的断言会假失败。
func quoteSegment(seg string) string {
	const hexDigits = "0123456789ABCDEF"
	var sb strings.Builder
	sb.Grow(len(seg))
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' || c == '/' {
			sb.WriteByte(c)
			continue
		}
		sb.WriteByte('%')
		sb.WriteByte(hexDigits[c>>4])
		sb.WriteByte(hexDigits[c&0x0F])
	}
	return sb.String()
}

// remoteURL 把相对路径拼成完整请求 URL。对照 webdav_backend.py:51-58。
func (b *WebDAV) remoteURL(path string) string {
	rel := strings.Trim(path, "/")
	if rel == "" {
		return b.baseURL + "/"
	}
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = quoteSegment(s)
	}
	return b.baseURL + "/" + strings.Join(segs, "/")
}

// newRequest 构造带认证与取消信号的请求。
func (b *WebDAV) newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("%w: 构造 %s 请求 %s 失败: %v", ErrBackend, method, url, err)
	}
	if b.user != "" {
		req.SetBasicAuth(b.user, b.pass)
	}
	return req, nil
}

// statusErr 包装非预期 HTTP 状态码为 ErrBackend。
func statusErr(method, url string, code int) error {
	return fmt.Errorf("%w: %s %s 失败：HTTP %d", ErrBackend, method, url, code)
}

// ---------------------------------------------------------------------------
// Backend 实现
// ---------------------------------------------------------------------------

// davResponse 等结构只为流式解析 PROPFIND 的单个 <response> 子树服务；
// 用 DecodeElement 逐元素解码，**不给整个 multistatus 建树** ——
// 目录极大（密库根下上千条目）时这是实打实的内存差异。
type davResponse struct {
	Href     string      `xml:"DAV: href"`
	Propstat davPropstat `xml:"DAV: propstat"`
}

type davPropstat struct {
	Status string  `xml:"DAV: status"`
	Prop   davProp `xml:"DAV: prop"`
}

type davProp struct {
	// 目录判定：resourcetype 存在且含 <collection/>。
	// 指针字段让「元素不存在」与「元素为空」可区分。
	ResourceType struct {
		Collection *struct{} `xml:"DAV: collection"`
	} `xml:"DAV: resourcetype"`
	ContentLength string `xml:"DAV: getcontentlength"`
}

// ListDir 用 PROPFIND Depth=1 列出目录下条目。
//
// 对照 webdav_backend.py:64-84。取**第一个** propstat（status 是否 200 不
// 检查）—— 与 Python 的 response.find 语义一致，真实服务器对存在路径只回
// 一个 200 propstat。
func (b *WebDAV) ListDir(ctx context.Context, path string) ([]Entry, error) {
	url := b.remoteURL(path)
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<D:propfind xmlns:D="DAV:">` +
		`<D:prop><D:resourcetype/><D:getcontentlength/><D:displayname/></D:prop>` +
		`</D:propfind>`

	req, err := b.newRequest(ctx, "PROPFIND", url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: PROPFIND %s 失败: %v", ErrBackend, url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		return nil, statusErr("PROPFIND", url, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: 读取 PROPFIND 响应失败: %v", ErrBackend, err)
	}
	return b.parseMultistatus(data, resp.Request.URL)
}

// parseMultistatus 解析 PROPFIND 多状态响应。
//
// 条目筛选对照 webdav_backend.py:86-123；「跳过自身」的判定是**有意的修正**：
// Python 用「解码后的 href 以 quote(请求路径) 结尾」判断（quote/unquote 域
// 不一致，且对根请求 endswith("/") 恒假），真实 Nextcloud 会把请求路径本身
// 放进 multistatus —— 于是中文路径下列出自己、根下列出根。Go 端改为比较
// 解码后的完整路径相等，两种情形都正确，且不会误伤子目录（服务器 href 是
// 完整链，仅自身会整段相等）。
func (b *WebDAV) parseMultistatus(data []byte, self *url.URL) ([]Entry, error) {
	// 自身 href 的归一化形态：解码路径 + 去尾斜杠。self 是实际请求的 URL，
	// 服务器回显的自身条目 href 与它等价（可能带尾斜杠、全 URL 或纯路径）。
	selfPath := strings.TrimSuffix(self.Path, "/")

	dec := xml.NewDecoder(strings.NewReader(string(data)))
	out := make([]Entry, 0)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: PROPFIND 响应不是合法 XML: %v", ErrBackend, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "response" {
			continue
		}

		var r davResponse
		if err := dec.DecodeElement(&r, &se); err != nil {
			return nil, fmt.Errorf("%w: PROPFIND 响应解析 <response> 失败: %v", ErrBackend, err)
		}
		if strings.TrimSpace(r.Href) == "" {
			continue
		}

		// href 可能是完整 URL、绝对路径或相对路径；取解码后的路径做归一化
		href := strings.TrimSpace(r.Href)
		hrefPath := href
		if u, perr := url.Parse(href); perr == nil && u.Path != "" {
			hrefPath = u.Path
		}
		hrefPath = strings.TrimSuffix(hrefPath, "/")

		// 跳过自身条目（多状态响应第一项通常是请求路径本身）
		if hrefPath == selfPath {
			continue
		}

		// 取最后一段作为名称（Python 同：rstrip("/").rsplit("/",1)[-1]）
		name := hrefPath
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" {
			continue
		}

		isDir := r.Propstat.Prop.ResourceType.Collection != nil
		var size int64
		if txt := strings.TrimSpace(r.Propstat.Prop.ContentLength); txt != "" {
			// Python 端 int() 解析失败会抛异常让上层崩；Go 端按 0 容错。
			// 服务器不回 getcontentlength 时 size=0 是正常形态。
			if n, perr := strconv.ParseInt(txt, 10, 64); perr == nil {
				size = n
			}
		}
		out = append(out, Entry{Name: name, IsDir: isDir, Size: size})
	}
	return out, nil
}

// GetSize 用 HEAD 取文件大小。
//
// 对照 webdav_backend.py:125-132。状态码 ≥400 一律 ErrBackend（Python 是
// ConnectionError）—— 注意**不存在 ≠ ErrNotFound**：HEAD 的 404 无法与
// 其它失败区分语义（Python 同样把 404 归 ConnectionError），且上层对
// 不存在的判定走 Exists。
func (b *WebDAV) GetSize(ctx context.Context, path string) (int64, error) {
	url := b.remoteURL(path)
	req, err := b.newRequest(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: HEAD %s 失败: %v", ErrBackend, url, err)
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		return 0, statusErr("HEAD", url, resp.StatusCode)
	}
	// Content-Length 缺失/非数字 → 0（Python int(cl) if cl else 0）
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, perr := strconv.ParseInt(cl, 10, 64); perr == nil {
			return n, nil
		}
	}
	return 0, nil
}

// Head 与 GetSize 同义（断点续传基准）。对照 webdav_backend.py:216-218。
func (b *WebDAV) Head(ctx context.Context, path string) (int64, error) {
	return b.GetSize(ctx, path)
}

// DownloadRange 用 GET Range 取密文段。
//
// 状态码分支逐字对照 webdav_backend.py:134-159：
//   - 404        → ErrNotFound（与网络故障区分，供上层回真 404）
//   - 206        → 原样返回响应体
//   - 200        → 服务器不支持 Range、返回整文件：本地切出 [start, end]，
//     长度不足必须报错 —— 否则调用方按请求偏移取密文会**错位解密**
//     （症状是「下载成功但解密出乱码」）。
func (b *WebDAV) DownloadRange(ctx context.Context, path string, start, end int64) ([]byte, error) {
	if start < 0 || end < start {
		return nil, fmt.Errorf("%w: [%d, %d]", ErrRange, start, end)
	}
	url := b.remoteURL(path)
	req, err := b.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: GET Range %s 失败: %v", ErrBackend, url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	case http.StatusPartialContent:
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("%w: 读取 %s 响应失败: %v", ErrBackend, path, err)
		}
		return data, nil
	case http.StatusOK:
		// **性能修正（对照 Python 的有意差异）**：Python 端 r.content 会把
		// 不支持 Range 的服务器整文件（可达 GB 级）全部载入内存，只为切出
		// 一段。Go 端只读到 end+1 字节即止 —— 结果语义完全一致
		// （长度不足照样报错），内存占用从 O(文件) 降到 O(请求段)。
		need := end + 1
		data, err := io.ReadAll(io.LimitReader(resp.Body, need))
		if err != nil {
			return nil, fmt.Errorf("%w: 读取 %s 响应失败: %v", ErrBackend, path, err)
		}
		if int64(len(data)) != need {
			return nil, fmt.Errorf("%w: %s 的 200 整文件回退切片不足：需 %d 字节，实际 %d 字节",
				ErrBackend, path, need, len(data))
		}
		// len(data) == end+1 时 data[start:] 恰长 end-start+1
		return data[start:], nil
	default:
		return nil, statusErr("GET", url, resp.StatusCode)
	}
}

// UploadChunked 分块 PUT 上传本地文件，支持断点续传。
//
// 对照 webdav_backend.py:161-214。续传语义：先 HEAD 远端已有大小，从该偏移
// 开始分块 PUT，每块带 `Content-Range: bytes pos-end/total`。**续传依赖
// 服务器支持 Content-Range 分块 PUT**（WebDAV 扩展，非 RFC 4918 核心）——
// 不支持的服务器会整块替换，Python 端同样不探测，这是两端共同的协议前提。
//
// **有意的行为差异（与 Local 端同一修正）**：Python 在「远端已与本地等长」
// 时一个进度都不 yield，进度条停在 99%；Go 端保证末值恒为 1.0。
func (b *WebDAV) UploadChunked(
	ctx context.Context, local, remote string, chunk int, onProgress func(float64),
) error {
	emit := func(v float64) {
		if onProgress != nil {
			onProgress(v)
		}
	}
	if chunk <= 0 {
		chunk = DefaultChunk
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	f, err := os.Open(local)
	if err != nil {
		return notFoundOrBackend(err, local)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("%w: 读取本地源文件 %s 失败: %v", ErrBackend, local, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%w: 本地源不是文件：%s", ErrNotFound, local)
	}
	total := fi.Size()
	url := b.remoteURL(remote)

	// 断点续传基准：远端已存在大小；HEAD 失败（含 404）或远端比源大 → 从头
	offset, err := b.Head(ctx, remote)
	if err != nil || offset > total {
		offset = 0
	}

	// 远端已完整：无需任何请求。Python 在这里一个进度都不发，上层任务
	// 永不结束（进度永远停在 99%）；Go 端直接报完成。
	if total > 0 && offset >= total {
		emit(1.0)
		return nil
	}

	if total == 0 {
		// 空文件：单次 PUT 空内容，无 Content-Range（空文件无从谈偏移）
		req, err := b.newRequest(ctx, http.MethodPut, url, nil)
		if err != nil {
			return err
		}
		resp, err := b.client.Do(req)
		if err != nil {
			return fmt.Errorf("%w: PUT 空文件失败: %v", ErrBackend, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 && resp.StatusCode != 201 && resp.StatusCode != 204 {
			return statusErr("PUT", url, resp.StatusCode)
		}
		emit(1.0)
		return nil
	}

	buf := make([]byte, chunk)
	pos := offset
	for pos < total {
		if err := ctx.Err(); err != nil { // 每个分块边界检查取消
			return err
		}
		n, err := f.ReadAt(buf, pos)
		if n > 0 {
			end := pos + int64(n) - 1
			req, rerr := b.newRequest(ctx, http.MethodPut, url, bytes.NewReader(buf[:n]))
			if rerr != nil {
				return rerr
			}
			req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", pos, end, total))
			resp, perr := b.client.Do(req)
			if perr != nil {
				return fmt.Errorf("%w: PUT 分块 [%d-%d] 失败: %v", ErrBackend, pos, end, perr)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 && resp.StatusCode != 201 &&
				resp.StatusCode != 204 && resp.StatusCode != 308 {
				return statusErr("PUT", url, resp.StatusCode)
			}
			pos += int64(n)
			emit(float64(pos) / float64(total))
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: 读取本地源文件 %s 失败: %v", ErrBackend, local, err)
		}
		if err == io.EOF || n == 0 {
			break
		}
	}
	return nil
}

// Mkdir 用 MKCOL 建目录。对照 webdav_backend.py:220-225。
// 405 = 已存在（幂等成功）：同步任务会重复 Mkdir，报错会让整批失败。
func (b *WebDAV) Mkdir(ctx context.Context, path string) error {
	url := b.remoteURL(path)
	req, err := b.newRequest(ctx, "MKCOL", url, nil)
	if err != nil {
		return err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: MKCOL %s 失败: %v", ErrBackend, url, err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case 200, 201, 405:
		return nil
	default:
		return statusErr("MKCOL", url, resp.StatusCode)
	}
}

// Rename 用 MOVE 重命名/移动。对照 webdav_backend.py:227-234。
// Destination 必须是与请求同源的完整 URL；Overwrite: T 允许覆盖已存在目标。
func (b *WebDAV) Rename(ctx context.Context, old, new string) error {
	src := b.remoteURL(old)
	dst := b.remoteURL(new)
	req, err := b.newRequest(ctx, "MOVE", src, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Destination", dst)
	req.Header.Set("Overwrite", "T")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: MOVE %s 失败: %v", ErrBackend, src, err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case 200, 201, 204:
		return nil
	default:
		return statusErr("MOVE", src, resp.StatusCode)
	}
}

// Delete 用 DELETE 删除文件或目录。对照 webdav_backend.py:236-241。
// 404 = 已不存在（幂等成功）。
func (b *WebDAV) Delete(ctx context.Context, path string) error {
	url := b.remoteURL(path)
	req, err := b.newRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: DELETE %s 失败: %v", ErrBackend, url, err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case 200, 204, 404:
		return nil
	default:
		return statusErr("DELETE", url, resp.StatusCode)
	}
}

// Exists 用 HEAD 判断存在性。对照 webdav_backend.py:243-247。
// 注意：状态码 ≥400 返回 (false, nil)，**网络故障返回 error** ——
// 只有后者才该向用户报「连接失败」，前者是「确实不存在」。
func (b *WebDAV) Exists(ctx context.Context, path string) (bool, error) {
	url := b.remoteURL(path)
	req, err := b.newRequest(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: HEAD %s 失败: %v", ErrBackend, url, err)
	}
	resp.Body.Close()
	return resp.StatusCode < 400, nil
}
