package storage

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 进程内百度 XPAN 假服务器
// ---------------------------------------------------------------------------

// bfile 是假服务器里的一个条目（文件与目录同构，目录 data 为空）。
type bfile struct {
	isdir bool
	fsid  int64
	data  []byte
}

// baiduMock 是内存版百度网盘开放平台，路由对齐真实 XPAN 接口：
//
//	POST/GET /rest/2.0/xpan/file?method=list|create|rename|filemanager|upload|precreate
//	POST /rest/2.0/xpan/superfile2?method=upload&type=tmpfile   （分片）
//	GET  /rest/2.0/xpan/multimedia?method=filemetas             （取 dlink）
//	GET  /dl/<fsid>                                             （直链下载，可 Range）
//	GET  /oauth/2.0/token                                       （刷新 token）
//
// 与真实服务的关键一致性：
//   - 所有接口请求必须带当前有效的 access_token，否则回 errno=111 ——
//     靠它驱动后端的「111 → 刷新 → 重试」链路（刷新会轮换 refresh_token，
//     旧 refresh_token 立即失效，与百度一致）；
//   - dlink 下载必须携带 User-Agent: pan.baidu.com，否则 403；
//   - mkdir 已存在回 -8；delete/rename/move 目标不存在回 -9/-12。
type baiduMock struct {
	mu sync.Mutex

	entries  map[string]*bfile // key 形如 "/a/b"；"/" 恒在
	nextFsid int64

	nextUploadID int
	tmpParts     map[string]map[int][]byte // uploadid → partseq → 分片数据

	// token 状态机：刷新轮换两边 token
	curAccess     string
	curRefresh    string
	tokenHits     int
	denyRefresh   bool     // token 端点恒失败（测「无 refresh 可用」）
	accessUsed    []string // 各 API 请求携带的 access_token（断言重试用新 token）
	listHits      int      // file?method=list 命中次数（断言缓存生效）
	uploadHits    int      // 简单上传命中次数
	superHits     int      // superfile2 分片命中次数
	metasHits     int      // multimedia/filemetas 命中次数（断言 dlink 缓存）
	failRange     bool     // dlink 忽略 Range 恒回 200 整文件
	dropUAReject  bool     // 关闭 UA 强制（无需测 UA 时用）
	noUARejects   int      // UA 校验拒绝次数
	rateLimitLeft int      // 剩余 429 次数（首个请求先被限流）
	badJSON       bool     // API 回非 JSON 响应（测解析错误）
	partFailSeq   int      // 该 partseq 第一次上传失败（errno 1），重试成功
	partFailArmed bool
	partLog       []string // superfile2 请求轨迹（失败注入调试用）
}

func newBaiduMock() *baiduMock {
	return &baiduMock{
		entries:    map[string]*bfile{"/": {isdir: true, fsid: 1}},
		nextFsid:   2,
		tmpParts:   map[string]map[int][]byte{},
		curAccess:  "access-1",
		curRefresh: "refresh-1",
	}
}

// addFile 放一个文件条目（自动补父目录，便于测试聚焦业务分支）。
func (m *baiduMock) addFile(path string, data []byte) {
	m.ensureParents(path)
	if e, ok := m.entries[path]; ok {
		e.data = data
		e.isdir = false
		return
	}
	m.entries[path] = &bfile{fsid: m.nextFsid, data: data}
	m.nextFsid++
}

// addDir 放一个目录条目。
func (m *baiduMock) addDir(path string) {
	m.ensureParents(path)
	if _, ok := m.entries[path]; !ok {
		m.entries[path] = &bfile{isdir: true, fsid: m.nextFsid}
		m.nextFsid++
	}
}

func (m *baiduMock) ensureParents(path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	cur := ""
	for _, part := range parts[:max(len(parts)-1, 0)] {
		cur += "/" + part
		if _, ok := m.entries[cur]; !ok {
			m.entries[cur] = &bfile{isdir: true, fsid: m.nextFsid}
			m.nextFsid++
		}
	}
}

func (m *baiduMock) exists(path string) bool {
	_, ok := m.entries[path]
	return ok
}

func (m *baiduMock) entryByFsid(fsid int64) (string, *bfile) {
	for p, e := range m.entries {
		if e.fsid == fsid {
			return p, e
		}
	}
	return "", nil
}

// writeJSON 统一 JSON 响应。
func (m *baiduMock) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// serveHTTP 主路由（锁内处理，保证计数与状态断言互不干扰）。
func (m *baiduMock) serveHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case r.URL.Path == "/oauth/2.0/token":
		m.handleToken(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/dl/"):
		m.handleDlink(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/rest/2.0/xpan/"):
		m.handleXpan(w, r)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// requireToken 校验 access_token；无效回 errno=111（驱动刷新链路）。
func (m *baiduMock) requireToken(w http.ResponseWriter, r *http.Request) bool {
	tok := r.URL.Query().Get("access_token")
	m.accessUsed = append(m.accessUsed, tok)
	if tok != m.curAccess {
		m.writeJSON(w, map[string]any{"errno": 111})
		return false
	}
	return true
}

func (m *baiduMock) handleToken(w http.ResponseWriter, r *http.Request) {
	rt := r.URL.Query().Get("refresh_token")
	if m.denyRefresh || rt != m.curRefresh {
		// 刷新失败形态：无 access_token 字段（后端按此识别失败）
		m.writeJSON(w, map[string]any{"error": "invalid_grant", "error_description": "refresh token 无效或已轮换"})
		return
	}
	m.tokenHits++
	m.curAccess = fmt.Sprintf("access-%d", m.tokenHits+1)
	m.curRefresh = fmt.Sprintf("refresh-%d", m.tokenHits+1)
	m.writeJSON(w, map[string]any{
		"access_token":  m.curAccess,
		"refresh_token": m.curRefresh,
		"expires_in":    2592000,
	})
}

func (m *baiduMock) handleDlink(w http.ResponseWriter, r *http.Request) {
	fsid, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/dl/"), 10, 64)
	if err != nil {
		http.Error(w, "bad fsid", http.StatusBadRequest)
		return
	}
	_, e := m.entryByFsid(fsid)
	if e == nil || e.isdir {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !m.dropUAReject && r.Header.Get("User-Agent") != baiduUA {
		m.noUARejects++
		http.Error(w, "ua rejected", http.StatusForbidden)
		return
	}
	// 200 整文件回退模式：忽略 Range
	spec := r.Header.Get("Range")
	if m.failRange {
		spec = ""
	}
	if spec != "" {
		parts := strings.SplitN(strings.TrimPrefix(spec, "bytes="), "-", 2)
		s, _ := strconv.Atoi(parts[0])
		eIdx := len(e.data) - 1
		if parts[1] != "" {
			eIdx, _ = strconv.Atoi(parts[1])
		}
		eIdx = min(eIdx, len(e.data)-1)
		chunk := e.data[s : eIdx+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, eIdx, len(e.data)))
		w.Header().Set("Content-Length", strconv.Itoa(len(chunk)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(chunk)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(e.data)
}

func (m *baiduMock) handleXpan(w http.ResponseWriter, r *http.Request) {
	if !m.requireToken(w, r) {
		return
	}
	// 限流注入：首个请求回 429，随后正常
	if m.rateLimitLeft > 0 {
		m.rateLimitLeft--
		http.Error(w, "slow down", http.StatusTooManyRequests)
		return
	}
	if m.badJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `这不是 JSON{{{`)
		return
	}

	endpoint := strings.TrimPrefix(r.URL.Path, "/rest/2.0/xpan/")
	switch endpoint {
	case "file":
		m.handleFileAPI(w, r)
	case "multimedia":
		m.handleFilemetas(w, r)
	case "superfile2":
		m.handleSuperfile2(w, r)
	default:
		http.Error(w, "bad endpoint", http.StatusNotFound)
	}
}

// parseForm 一次性解析请求体：multipart 返回字段表与 file 字段数据，
// urlencoded 只有字段表。必须单次遍历——MultipartReader 是流式，
// 分两次读第二次必然为空（曾导致上传内容全空）。
func (m *baiduMock) parseForm(r *http.Request) (vals map[string]string, file []byte) {
	vals = map[string]string{}
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err != nil {
			return vals, nil
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return vals, file
			}
			b, _ := io.ReadAll(part)
			if part.FormName() == "file" {
				file = b
				continue
			}
			vals[part.FormName()] = string(b)
		}
		return vals, file
	}
	r.ParseForm()
	for k, vs := range r.PostForm {
		if len(vs) > 0 {
			vals[k] = vs[0]
		}
	}
	return vals, nil
}

func (m *baiduMock) handleFileAPI(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Query().Get("method")
	switch method {
	case "list":
		m.methodList(w, r)
	case "create":
		m.methodCreate(w, r)
	case "rename":
		m.methodRename(w, r)
	case "filemanager":
		m.methodFilemanager(w, r)
	case "upload":
		m.methodSimpleUpload(w, r)
	case "precreate":
		m.methodPrecreate(w, r)
	default:
		m.writeJSON(w, map[string]any{"errno": -12, "errmsg": "unknown method"})
	}
}

// itemJSON 生成 list/filemetas 里的条目 JSON。
func itemJSON(name string, e *bfile) map[string]any {
	size := 0
	if !e.isdir {
		size = len(e.data)
	}
	isdir := 0
	if e.isdir {
		isdir = 1
	}
	return map[string]any{
		"server_filename": name,
		"isdir":           isdir,
		"size":            size,
		"fs_id":           e.fsid,
	}
}

func (m *baiduMock) methodList(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	m.listHits++
	e, ok := m.entries[dir]
	if !ok || !e.isdir {
		m.writeJSON(w, map[string]any{"errno": -9, "errmsg": "dir not found"})
		return
	}
	items := []any{}
	for _, p := range sortedKeysOf(m.entries) {
		if childOf(p, dir) && p != dir {
			name := strings.TrimPrefix(p, strings.TrimSuffix(dir, "/")+"/")
			items = append(items, itemJSON(name, m.entries[p]))
		}
	}
	m.writeJSON(w, map[string]any{"errno": 0, "list": items})
}

func (m *baiduMock) methodCreate(w http.ResponseWriter, r *http.Request) {
	form, _ := m.parseForm(r)
	path := form["path"]
	if m.exists(path) {
		m.writeJSON(w, map[string]any{"errno": -8, "errmsg": "already exists"})
		return
	}
	// 分片合并：带 uploadid 即 precreate 后的 create 收尾
	if uploadid := form["uploadid"]; uploadid != "" {
		parts := m.tmpParts[uploadid]
		delete(m.tmpParts, uploadid)
		seqs := make([]int, 0, len(parts))
		for seq := range parts {
			seqs = append(seqs, seq)
		}
		sort.Ints(seqs)
		merged := []byte{}
		for _, seq := range seqs {
			merged = append(merged, parts[seq]...)
		}
		m.addFile(path, merged)
		m.writeJSON(w, map[string]any{"errno": 0})
		return
	}
	// 目录创建（后端 mkdir 只用 isdir=1）
	if form["isdir"] == "1" {
		m.addDir(path)
		m.writeJSON(w, map[string]any{"errno": 0})
		return
	}
	m.writeJSON(w, map[string]any{"errno": -9, "errmsg": "unsupported"})
}

func (m *baiduMock) methodRename(w http.ResponseWriter, r *http.Request) {
	form, _ := m.parseForm(r)
	old := form["path"]
	e, ok := m.entries[old]
	if !ok {
		m.writeJSON(w, map[string]any{"errno": -9, "errmsg": "src not found"})
		return
	}
	newPath := old[:strings.LastIndex(old, "/")] + "/" + form["newname"]
	if m.exists(newPath) {
		m.writeJSON(w, map[string]any{"errno": -8, "errmsg": "dest exists"})
		return
	}
	m.ensureParents(newPath)
	delete(m.entries, old)
	m.entries[newPath] = e
	m.writeJSON(w, map[string]any{"errno": 0})
}

// removeTree 递归删除（目录含全部子树）。
func (m *baiduMock) removeTree(path string) bool {
	deleted := false
	for p := range m.entries {
		if p == path || strings.HasPrefix(p, strings.TrimSuffix(path, "/")+"/") {
			delete(m.entries, p)
			deleted = true
		}
	}
	return deleted
}

func (m *baiduMock) methodFilemanager(w http.ResponseWriter, r *http.Request) {
	form, _ := m.parseForm(r)
	opera := r.URL.Query().Get("opera")
	switch opera {
	case "delete":
		var list []string
		if err := json.Unmarshal([]byte(form["filelist"]), &list); err != nil {
			m.writeJSON(w, map[string]any{"errno": -12, "errmsg": "bad filelist"})
			return
		}
		anyDeleted := false
		for _, p := range list {
			if m.removeTree(p) {
				anyDeleted = true
			}
		}
		if !anyDeleted {
			// 全部不存在：-9/12（后端对两者都幂等）
			m.writeJSON(w, map[string]any{"errno": 12, "errmsg": "target missing"})
			return
		}
		m.writeJSON(w, map[string]any{"errno": 0})

	case "move":
		var list []map[string]string
		if err := json.Unmarshal([]byte(form["filelist"]), &list); err != nil || len(list) != 1 {
			m.writeJSON(w, map[string]any{"errno": -12, "errmsg": "bad filelist"})
			return
		}
		old, dest, newName := list[0]["path"], list[0]["dest"], list[0]["newname"]
		destEntry, destOK := m.entries[dest]
		if !destOK || !destEntry.isdir {
			m.writeJSON(w, map[string]any{"errno": 12, "errmsg": "dest missing"})
			return
		}
		if !m.exists(old) {
			m.writeJSON(w, map[string]any{"errno": 12, "errmsg": "src missing"})
			return
		}
		// 子树整体搬迁：/old 前缀下的键全部重挂到新前缀
		type pair struct{ from, to string }
		var moves []pair
		prefix := strings.TrimSuffix(old, "/")
		for p := range m.entries {
			if p == old {
				moves = append(moves, pair{p, dest + "/" + newName})
			} else if strings.HasPrefix(p, prefix+"/") {
				moves = append(moves, pair{p, dest + "/" + newName + strings.TrimPrefix(p, prefix)})
			}
		}
		for _, mv := range moves {
			m.entries[mv.to] = m.entries[mv.from]
			delete(m.entries, mv.from)
		}
		m.writeJSON(w, map[string]any{"errno": 0})

	default:
		m.writeJSON(w, map[string]any{"errno": -12, "errmsg": "bad opera"})
	}
}

func (m *baiduMock) methodSimpleUpload(w http.ResponseWriter, r *http.Request) {
	form, data := m.parseForm(r)
	m.uploadHits++
	m.addFile(form["path"], data)
	m.writeJSON(w, map[string]any{"errno": 0})
}

func (m *baiduMock) methodPrecreate(w http.ResponseWriter, r *http.Request) {
	form, _ := m.parseForm(r)
	// 简化校验：非空 path/size 即可（block_list 内容由 create 阶段比对数据承担）
	if form["path"] == "" || form["size"] == "" {
		m.writeJSON(w, map[string]any{"errno": -12, "errmsg": "bad precreate"})
		return
	}
	m.nextUploadID++
	uploadid := fmt.Sprintf("up-%d", m.nextUploadID)
	m.tmpParts[uploadid] = map[int][]byte{}
	m.writeJSON(w, map[string]any{"errno": 0, "uploadid": uploadid})
}

func (m *baiduMock) handleSuperfile2(w http.ResponseWriter, r *http.Request) {
	form, data := m.parseForm(r)
	m.superHits++
	seq, err := strconv.Atoi(form["partseq"])
	parts, ok := m.tmpParts[form["uploadid"]]
	m.partLog = append(m.partLog, fmt.Sprintf("seq=%d armed=%v", seq, m.partFailArmed))
	if err != nil || !ok || parts == nil {
		m.writeJSON(w, map[string]any{"errno": -12, "errmsg": "bad uploadid"})
		return
	}
	// 注入分片失败：指定 partseq 第一次上传回 errno 1（后端应重试成功）
	if m.partFailArmed && seq == m.partFailSeq {
		m.partFailArmed = false
		m.writeJSON(w, map[string]any{"errno": 1, "errmsg": "injected part failure"})
		return
	}
	parts[seq] = data
	m.writeJSON(w, map[string]any{"errno": 0})
}

func (m *baiduMock) handleFilemetas(w http.ResponseWriter, r *http.Request) {
	m.metasHits++
	fsids := r.URL.Query().Get("fsids") // 形如 "[42]"
	var ids []int64
	json.Unmarshal([]byte(fsids), &ids)
	items := []any{}
	for _, id := range ids {
		if path, e := m.entryByFsid(id); e != nil && !e.isdir {
			item := itemJSON(path[strings.LastIndex(path, "/")+1:], e)
			item["dlink"] = "http://" + r.Host + "/dl/" + strconv.FormatInt(id, 10)
			items = append(items, item)
		}
	}
	m.writeJSON(w, map[string]any{"errno": 0, "list": items})
}

// newBaiduPair 建假服务器 + 后端，端点/退避全部指向本地以便断言。
func newBaiduPair(t *testing.T, d BaiduCredData) (*Baidu, *baiduMock) {
	t.Helper()
	m := newBaiduMock()
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	t.Cleanup(srv.Close)
	b := NewBaidu(d, nil)
	b.xpanBase = srv.URL + "/rest/2.0/xpan"
	b.oauthTokenURL = srv.URL + "/oauth/2.0/token"
	b.retryDelay = 10 * time.Millisecond
	return b, m
}
