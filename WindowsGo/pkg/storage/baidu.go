package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// 百度网盘开放平台（XPAN）接口常量。
// 对照 WindowsPy/src/cloudprism/storage/baidu_backend.py:34-49
const (
	baiduOAuthTokenURL = "https://openapi.baidu.com/oauth/2.0/token"
	baiduXpanBase      = "https://pan.baidu.com/rest/2.0/xpan"

	// 百度分片上传强制要求单片 4 MiB（不能更大）
	baiduPartSize = 4 << 20
	// <= 此大小可直接简单上传
	baiduSimpleUploadMax = 4 << 20
	// dlink 官方有效期约 8 小时，缓存保守取 6 小时（对齐 Python）
	baiduDlinkTTL = 6 * time.Hour
	// entry（fsid/size）缓存的保鲜期：比 dlink 短得多，
	// 因为远端目录树的任何改动都可能使 size/fsid 失效
	baiduEntryTTL = 5 * time.Minute

	// 常见错误码
	baiduErrTokenExpired  = 111 // access_token 过期
	baiduErrFileNotExist  = -9  // 文件不存在（部分接口）
	baiduErrAlreadyExist  = -8  // 目录已存在
	baiduErrTargetMissing = 12  // 目标不存在（filemanager 语义）
)

// baiduUA 是 dlink 下载必须携带的 User-Agent：百度对直链请求校验 UA，
// 不带或带其它 UA 会被拒绝（baidu_backend.py:329-330 同款约束）。
const baiduUA = "pan.baidu.com"

// baiduAPIError 携带 errno 的百度接口错误。
//
// 对照 Python 的 BaiduApiError(ConnectionError)。errno 供上层按业务分支
// （-9/-8/12 是幂等成功或「不存在」），同时 Unwrap 到 ErrBackend ——
// 它本质上是后端故障，只有重试可能成功（刷新 token 后 111 会消失）。
type baiduAPIError struct {
	Errno int
	Msg   string
}

func (e *baiduAPIError) Error() string {
	return fmt.Sprintf("百度网盘接口错误 errno=%d %s", e.Errno, e.Msg)
}

func (e *baiduAPIError) Unwrap() error { return ErrBackend }

// Baidu 是百度网盘存储后端（实现 StorageBackend 全部方法）。
//
// 对照 baidu_backend.py:166-260。**并发模型与 Python 的差异**：Python 端
// access_token 的「errno=111 → 刷新 → 重试」在多线程同时触发时会并发刷新，
// 而百度每次刷新都会轮换 refresh_token —— 先刷成功的线程把后发的刷新请求
// 变成非法请求。Go 端用 refreshMu 把刷新串行化 + token 快照双检：
// 只有真正持有锁且发现 token 没被别人刷过的线程才发起刷新（见 api）。
//
// 本类型被传输队列多个 goroutine 并发调用（并行分片上传/下载），
// 所有可变状态（token、缓存）都受锁保护。
type Baidu struct {
	appKey    string // client_id
	secretKey string // client_secret
	appID     string // device_id（授权页用，API 不需要）

	tokenMu      sync.RWMutex
	accessToken  string
	refreshToken string
	expiresAt    float64
	refreshMu    sync.Mutex // 串行化 token 刷新（百度刷新会轮换 refresh_token）

	client *http.Client
	store  *BaiduCredStore // 刷新成功后回写；可为 nil

	cache baiduEntryCache // 路径 → {fsid,size,dlink} 组合缓存

	// 端点可注入：真实环境用默认常量，httptest 假服务测试时指向本地
	oauthTokenURL string
	xpanBase      string

	// 限流退避基数（测试注入小值避免真睡 1s/2s/4s）
	retryDelay time.Duration
}

// 编译期断言。
var _ Backend = (*Baidu)(nil)

// NewBaidu 构造百度后端。store 可为 nil（不落盘凭证）；
// accessToken 可为空串（工厂层负责在授权完成前拒绝构造）。
func NewBaidu(d BaiduCredData, store *BaiduCredStore) *Baidu {
	return &Baidu{
		appKey:        d.AppKey,
		secretKey:     d.SecretKey,
		appID:         d.AppID,
		accessToken:   d.AccessToken,
		refreshToken:  d.RefreshToken,
		expiresAt:     d.ExpiresAt,
		client:        &http.Client{Transport: sharedTransport},
		store:         store,
		cache:         newBaiduEntryCache(),
		oauthTokenURL: baiduOAuthTokenURL,
		xpanBase:      baiduXpanBase,
		retryDelay:    time.Second,
	}
}

// ---------------------------------------------------------------------------
// token 访问与刷新
// ---------------------------------------------------------------------------

// currentToken 返回当前 access_token 的快照（加锁读）。
func (b *Baidu) currentToken() string {
	b.tokenMu.RLock()
	defer b.tokenMu.RUnlock()
	return b.accessToken
}

// Credentials 导出当前凭证数据（供绑定层展示授权状态）。
func (b *Baidu) Credentials() BaiduCredData {
	b.tokenMu.RLock()
	defer b.tokenMu.RUnlock()
	return BaiduCredData{
		AppID:        b.appID,
		AppKey:       b.appKey,
		SecretKey:    b.secretKey,
		AccessToken:  b.accessToken,
		RefreshToken: b.refreshToken,
		ExpiresAt:    b.expiresAt,
	}
}

// refreshIfChanged 处理 errno=111：若进入锁时发现 token 已被其它 goroutine
// 刷新过（快照比对），说明别人刚完成了刷新，直接重试即可，不再发刷新请求。
func (b *Baidu) refreshIfChanged(ctx context.Context, oldToken string) error {
	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()

	if b.currentToken() != oldToken {
		return nil // 已由并发请求刷新，token 快照变了
	}
	return b.refreshLocked(ctx)
}

// refreshLocked 用 refresh_token 换新 token（调用方须持 refreshMu）。
// 对照 baidu_backend.py:233-260。
func (b *Baidu) refreshLocked(ctx context.Context) error {
	if b.refreshToken == "" {
		return fmt.Errorf("%w: access_token 已过期且无 refresh_token，请重新授权", ErrBackend)
	}

	q := url.Values{}
	q.Set("grant_type", "refresh_token")
	q.Set("refresh_token", b.refreshToken)
	q.Set("client_id", b.appKey)
	q.Set("client_secret", b.secretKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.oauthTokenURL+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("%w: 构造刷新 token 请求失败: %v", ErrBackend, err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: 刷新 token 请求失败: %v", ErrBackend, err)
	}
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("%w: 刷新 token 响应非 JSON: %v", ErrBackend, err)
	}
	tok, ok := out["access_token"].(string)
	if !ok || tok == "" {
		// 刷新失败会带 error/error_description 字段（错误时无 access_token）
		desc, _ := out["error_description"].(string)
		if desc == "" {
			desc = fmt.Sprint(out)
		}
		return fmt.Errorf("%w: 刷新 token 失败：%s", ErrBackend, desc)
	}

	b.tokenMu.Lock()
	b.accessToken = tok
	if rt, ok := out["refresh_token"].(string); ok && rt != "" {
		b.refreshToken = rt // 百度每次刷新会轮换 refresh_token
	}
	if ei, ok := jsonNum(out["expires_in"]); ok {
		b.expiresAt = float64(time.Now().Unix()) + ei
	}
	b.tokenMu.Unlock()

	// 回写加密存储（如有）：load 失败视为无旧数据，直接存新凭证
	if b.store != nil {
		saved := b.Credentials()
		if old := b.store.Load(); old != nil {
			saved.AppID = firstNonEmpty(saved.AppID, old.AppID)
			saved.AppKey = firstNonEmpty(saved.AppKey, old.AppKey)
			saved.SecretKey = firstNonEmpty(saved.SecretKey, old.SecretKey)
		}
		if err := b.store.Save(saved); err != nil {
			return fmt.Errorf("%w: 回写百度凭证失败: %v", ErrBackend, err)
		}
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// API 请求内核
// ---------------------------------------------------------------------------

// apiParams 描述一次百度接口调用。
type apiParams struct {
	endpoint   string     // "file" / "multimedia" / "superfile2"
	query      url.Values // method=list 等查询参数
	form       url.Values // POST 表单参数（data={...}）
	upload     []byte     // 非 nil 时以 multipart/form-data 的 file 字段提交
	uploadName string     // 简单上传时文件名（superfile2 恒 "chunk"）
}

// api 是带 token 的接口请求内核：errno=111 自动刷新重试一次、429 指数退避。
// 对照 baidu_backend.py:200-231。
func (b *Baidu) api(ctx context.Context, p apiParams) (map[string]any, error) {
	retried := false
	for attempt := 0; ; attempt++ {
		token := b.currentToken()
		out, errno, err := b.apiOnce(ctx, p, token)
		if err != nil {
			return nil, err
		}
		switch {
		case errno == baiduErrTokenExpired && !retried:
			if err := b.refreshIfChanged(ctx, token); err != nil {
				return nil, err
			}
			retried = true
			continue
		case errno != 0:
			msg := truncateJSON(out)
			return nil, &baiduAPIError{Errno: errno, Msg: msg}
		default:
			return out, nil
		}
	}
}

// apiOnce 发单次请求并解析 errno。HTTP 429/503 限流按 1s/2s/4s 退避重试。
func (b *Baidu) apiOnce(ctx context.Context, p apiParams, token string) (map[string]any, int, error) {
	var (
		lastBody []byte
		code     int // 最后一次响应的状态码（非 JSON 错误文案用）
	)
	for backoff := 0; ; backoff++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		req, err := b.buildRequest(ctx, p, token)
		if err != nil {
			return nil, 0, err
		}
		resp, err := b.client.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: 百度接口 %s 请求失败: %v", ErrBackend, p.endpoint, err)
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			// 限流退避：1s/2s/4s 最多三次（百度侧没有文档化的 Retry-After）
			resp.Body.Close()
			if backoff >= 2 {
				return nil, 0, statusErr("BAIDU", p.endpoint, resp.StatusCode)
			}
			delay := time.Duration(1<<uint(backoff)) * b.retryDelay
			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			}
		}

		code = resp.StatusCode
		lastBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, 0, fmt.Errorf("%w: 读取百度接口响应失败: %v", ErrBackend, err)
		}
		break
	}

	var out map[string]any
	if err := json.Unmarshal(lastBody, &out); err != nil {
		return nil, 0, fmt.Errorf("%w: 百度接口响应非 JSON：HTTP %d", ErrBackend, code)
	}
	// errno 缺失视为 0（Python 的 out.get("errno", 0)）
	var errno int64
	if n, ok := jsonNum(out["errno"]); ok {
		errno = int64(n)
	}
	return out, int(errno), nil
}

// buildRequest 构造百度接口请求：查询参数恒带 access_token；
// 有上传文件时用 multipart/form-data，否则普通表单（对齐 requests 行为）。
func (b *Baidu) buildRequest(ctx context.Context, p apiParams, token string) (*http.Request, error) {
	q := url.Values{}
	q.Set("access_token", token)
	for k, vs := range p.query {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	full := b.xpanBase + "/" + p.endpoint + "?" + q.Encode()

	var (
		body        io.Reader
		contentType string
	)
	switch {
	case p.upload != nil:
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, vs := range p.form {
			for _, v := range vs {
				mw.WriteField(k, v)
			}
		}
		fw, err := mw.CreateFormFile("file", p.uploadName)
		if err != nil {
			return nil, fmt.Errorf("%w: 构造上传请求失败: %v", ErrBackend, err)
		}
		if _, err := fw.Write(p.upload); err != nil {
			return nil, fmt.Errorf("%w: 构造上传请求失败: %v", ErrBackend, err)
		}
		mw.Close()
		body = &buf
		contentType = mw.FormDataContentType()
	case len(p.form) > 0:
		body = strings.NewReader(p.form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}

	method := methodFor(p)
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, fmt.Errorf("%w: 构造请求 %s 失败: %v", ErrBackend, p.endpoint, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

// methodFor 纯查询走 GET，带表单/上传走 POST（对齐 requests 的 get/post 分发）。
func methodFor(p apiParams) string {
	if p.upload == nil && len(p.form) == 0 {
		return http.MethodGet
	}
	return http.MethodPost
}

// jsonNum 把 JSON 里的数字字段读成 float64（容忍 int/float 两种解码形态）。
func jsonNum(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func jsonStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func jsonInt(m map[string]any, key string) int64 {
	if n, ok := jsonNum(m[key]); ok {
		return int64(n)
	}
	return 0
}

func jsonIsDir(m map[string]any) bool {
	// isdir 以 0/1 表示（Python item.get("isdir", 0)）
	return jsonInt(m, "isdir") != 0
}

// truncateJSON 取响应 JSON 前 200 字符做错误信息（对齐 Python 的 [:200]）。
func truncateJSON(m map[string]any) string {
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Sprint(m)
	}
	s := string(raw)
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// ---------------------------------------------------------------------------
// 路径与条目查询
// ---------------------------------------------------------------------------

// abs 相对路径 → 百度绝对路径（以 / 开头，根为 /）。
// 对照 baidu_backend.py:195-198。
func baiduAbs(path string) string {
	rel := strings.Trim(path, "/")
	if rel == "" {
		return "/"
	}
	return "/" + rel
}

// baiduEntry 是目录列表里的一个条目（仅保留本后端关心的字段）。
type baiduEntry struct {
	Name  string
	IsDir bool
	Size  int64
	FsID  int64
	Dlink string // 仅 filemetas 响应携带
}

// listByDir 调 file?method=list 列目录（limit=1000，百度单页上限）。
// 对照 baidu_backend.py:287-300 与 266-273。
func (b *Baidu) listByDir(ctx context.Context, dir string) ([]baiduEntry, error) {
	q := url.Values{}
	q.Set("method", "list")
	q.Set("dir", dir)
	q.Set("limit", "1000")
	out, err := b.api(ctx, apiParams{endpoint: "file", query: q})
	if err != nil {
		return nil, err
	}
	items, _ := out["list"].([]any)
	entries := make([]baiduEntry, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		entries = append(entries, baiduEntry{
			Name:  jsonStr(m, "server_filename"),
			IsDir: jsonIsDir(m),
			Size:  jsonInt(m, "size"),
			FsID:  jsonInt(m, "fs_id"),
		})
	}
	return entries, nil
}

// listParent 列父目录并索引 {名: 条目}。
// 对照 baidu_backend.py:266-273（_list_parent）。
func (b *Baidu) listParent(ctx context.Context, path string) (map[string]baiduEntry, error) {
	abs := baiduAbs(path)
	parent := abs
	if i := strings.LastIndex(abs, "/"); i >= 0 {
		parent = abs[:i]
	}
	if parent == "" {
		parent = "/"
	}
	entries, err := b.listByDir(ctx, parent)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]baiduEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	return byName, nil
}

// entryOf 取路径对应条目；不存在返回 ErrNotFound。
// 对照 baidu_backend.py:275-281（_entry_of）。
func (b *Baidu) entryOf(ctx context.Context, path string) (*baiduEntry, error) {
	abs := baiduAbs(path)
	name := abs[strings.LastIndex(abs, "/")+1:]
	siblings, err := b.listParent(ctx, path)
	if err != nil {
		return nil, err
	}
	e, ok := siblings[name]
	if !ok {
		return nil, fmt.Errorf("%w: 百度网盘中不存在：%s", ErrNotFound, path)
	}
	return &e, nil
}

// ---------------------------------------------------------------------------
// Backend 实现
// ---------------------------------------------------------------------------

// ListDir 列出目录条目。对照 baidu_backend.py:287-300。
func (b *Baidu) ListDir(ctx context.Context, path string) ([]Entry, error) {
	entries, err := b.listByDir(ctx, baiduAbs(path))
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, Entry{Name: e.Name, IsDir: e.IsDir, Size: e.Size})
	}
	return out, nil
}

// GetSize 取文件字节大小。对照 baidu_backend.py:302-304。
// 经 entry 缓存（fsid/size 保鲜 5 分钟），把 Python 端每次 Range 请求
// 都要付的「列父目录 + filemetas」两次 API 调用降为 0。
func (b *Baidu) GetSize(ctx context.Context, path string) (int64, error) {
	e, err := b.cache.getEntry(ctx, b, path)
	if err != nil {
		return 0, err
	}
	return e.Size, nil
}

// Head 与 GetSize 同义（断点续传基准）。对照 baidu_backend.py:306-308。
func (b *Baidu) Head(ctx context.Context, path string) (int64, error) {
	return b.GetSize(ctx, path)
}

// Exists 判断路径是否存在（含目录）。对照 baidu_backend.py:310-320。
//
// 注意错误分类：仅「确实查不到」返回 false；errno -9/-8 是百度对部分接口
// 的「不存在」表达，也折叠为 false；网络故障等仍返回 error。
func (b *Baidu) Exists(ctx context.Context, path string) (bool, error) {
	_, err := b.cache.getEntry(ctx, b, path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	var apiErr *baiduAPIError
	if errors.As(err, &apiErr) &&
		(apiErr.Errno == baiduErrFileNotExist || apiErr.Errno == baiduErrAlreadyExist) {
		return false, nil
	}
	return false, err
}

// DownloadRange 经 dlink 按字节范围下载。对照 baidu_backend.py:322-349。
//
// 状态码分支与 WebDAV 后端一致：404→ErrNotFound、206→原样、200→整文件回退
// 本地切 [start, end] 且长度不足必须报错（否则错位解密）。
func (b *Baidu) DownloadRange(ctx context.Context, path string, start, end int64) ([]byte, error) {
	if start < 0 || end < start {
		return nil, fmt.Errorf("%w: [%d, %d]", ErrRange, start, end)
	}
	fsid, dlink, err := b.cache.getDlink(ctx, b, path)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("access_token", b.currentToken())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlink+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: 构造 dlink 下载请求失败: %v", ErrBackend, err)
	}
	// 百度要求携带固定 User-Agent，否则拒绝下载（baidu_backend.py:330）
	req.Header.Set("User-Agent", baiduUA)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: dlink 下载 %s (fsid=%d) 失败: %v", ErrBackend, path, fsid, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		// dlink 指向的文件被删除：条目缓存已失效，但稳妥起见移除缓存
		b.cache.remove(path)
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	case http.StatusPartialContent:
		return io.ReadAll(resp.Body)
	case http.StatusOK:
		// 与 WebDAV 后端同款性能修正：只读 end+1 字节而非整文件
		need := end + 1
		data, err := io.ReadAll(io.LimitReader(resp.Body, need))
		if err != nil {
			return nil, fmt.Errorf("%w: 读取 %s 下载响应失败: %v", ErrBackend, path, err)
		}
		if int64(len(data)) != need {
			return nil, fmt.Errorf("%w: %s 的 200 整文件回退切片不足：需 %d 字节，实际 %d 字节",
				ErrBackend, path, need, len(data))
		}
		return data[start:], nil
	default:
		return nil, fmt.Errorf("%w: dlink 下载失败：HTTP %d", ErrBackend, resp.StatusCode)
	}
}

// Mkdir 创建目录。对照 baidu_backend.py:466-478。
// errno=-8（目录已存在）幂等成功。
func (b *Baidu) Mkdir(ctx context.Context, path string) error {
	form := url.Values{}
	form.Set("path", baiduAbs(path))
	form.Set("isdir", "1")
	form.Set("size", "0")
	q := url.Values{}
	q.Set("method", "create")
	_, err := b.api(ctx, apiParams{endpoint: "file", query: q, form: form})
	if err == nil {
		return nil
	}
	var apiErr *baiduAPIError
	if errors.As(err, &apiErr) && apiErr.Errno == baiduErrAlreadyExist {
		return nil // -8：目录已存在，幂等
	}
	return err
}

// Rename 重命名或移动。同目录走 rename 接口，跨目录走 filemanager move。
// 对照 baidu_backend.py:480-506。
func (b *Baidu) Rename(ctx context.Context, old, new string) error {
	oldAbs := baiduAbs(old)
	newAbs := baiduAbs(new)
	oldParent := oldAbs[:strings.LastIndex(oldAbs, "/")]
	if oldParent == "" {
		oldParent = "/"
	}
	newParent := newAbs[:strings.LastIndex(newAbs, "/")]
	if newParent == "" {
		newParent = "/"
	}
	newName := newAbs[strings.LastIndex(newAbs, "/")+1:]
	q := url.Values{}
	form := url.Values{}

	if oldParent == newParent {
		q.Set("method", "rename")
		form.Set("path", oldAbs)
		form.Set("newname", newName)
	} else {
		q.Set("method", "filemanager")
		q.Set("opera", "move")
		form.Set("async", "0")
		filelist, _ := json.Marshal([]map[string]string{
			{"path": oldAbs, "dest": newParent, "newname": newName},
		})
		form.Set("filelist", string(filelist))
	}
	if _, err := b.api(ctx, apiParams{endpoint: "file", query: q, form: form}); err != nil {
		return err
	}
	// 路径是 entry 缓存的键：改名后旧键失效、新键也不可预置（fsid 未变但
	// 为简单起见两键都删，下次访问现查）
	b.cache.remove(old)
	b.cache.remove(new)
	return nil
}

// Delete 删除文件或目录。对照 baidu_backend.py:508-521。
// errno=-9/-12（目标不存在）幂等成功。
func (b *Baidu) Delete(ctx context.Context, path string) error {
	q := url.Values{}
	q.Set("method", "filemanager")
	q.Set("opera", "delete")
	form := url.Values{}
	form.Set("async", "0")
	filelist, _ := json.Marshal([]string{baiduAbs(path)})
	form.Set("filelist", string(filelist))

	_, err := b.api(ctx, apiParams{endpoint: "file", query: q, form: form})
	if err == nil {
		b.cache.remove(path)
		return nil
	}
	var apiErr *baiduAPIError
	if errors.As(err, &apiErr) &&
		(apiErr.Errno == baiduErrFileNotExist || apiErr.Errno == baiduErrTargetMissing) {
		b.cache.remove(path)
		return nil // 不存在视为已删除
	}
	return err
}

// UploadChunked 上传本地文件。对照 baidu_backend.py:367-392 的分发逻辑，
// 具体上传流程见 baidu_upload.go。
func (b *Baidu) UploadChunked(
	ctx context.Context, local, remote string, chunk int, onProgress func(float64),
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = chunk // 百度分片固定 4 MiB（接口要求），忽略设置页分块大小

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
	abs := baiduAbs(remote)

	// 远端已存在且大小一致：视为已完成（断点续传命中）
	if size, err := b.GetSize(ctx, remote); err == nil && size == total {
		if onProgress != nil {
			onProgress(1.0)
		}
		return nil
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		// 除「不存在」外的失败都要上报（例如网络故障时不能盲目覆盖上传）
		var apiErr *baiduAPIError
		if !errors.As(err, &apiErr) {
			return err
		}
	}

	if total <= baiduSimpleUploadMax {
		if err := b.uploadSimple(ctx, f, abs); err != nil {
			return err
		}
	} else {
		if err := b.uploadSuperfile(ctx, f, abs, total, onProgress); err != nil {
			return err
		}
	}
	b.cache.remove(remote) // 内容已变，路径缓存键失效
	if onProgress != nil {
		onProgress(1.0)
	}
	return nil
}

// uploadSimple 简单上传（<=4MiB）：method=upload + multipart file。
// 对照 baidu_backend.py:394-405（_upload_simple）。
func (b *Baidu) uploadSimple(ctx context.Context, f *os.File, abs string) error {
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("%w: 读取本地源文件失败: %v", ErrBackend, err)
	}
	q := url.Values{}
	q.Set("method", "upload")
	form := url.Values{}
	form.Set("path", abs)
	form.Set("ondup", "overwrite")
	name := abs[strings.LastIndex(abs, "/")+1:]

	_, err = b.api(ctx, apiParams{
		endpoint: "file", query: q, form: form, upload: data, uploadName: name,
	})
	return err
}
