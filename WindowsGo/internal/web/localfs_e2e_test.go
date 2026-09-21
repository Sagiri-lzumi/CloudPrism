package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本机目录浏览端点（/api/fs/*）的端到端校验：路由、JSON 结构、错误可展示性。
//
// 这一批端点替代了原先「后端弹原生目录框」的三个 choose*dir 端点，前端
// FolderPicker 完全依赖它们的字段名与语义（path/parent/dirs/hidden），
// 故这里逐个钉住，避免字段名静默漂移导致选择器空列表。

type localListing struct {
	Path   string `json:"path"`
	Parent string `json:"parent"`
	Drives []struct {
		Path  string `json:"path"`
		Label string `json:"label"`
		Kind  string `json:"kind"`
	} `json:"drives"`
	Dirs []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Hidden bool   `json:"hidden"`
	} `json:"dirs"`
}

func postJSON(t *testing.T, url string, body any) (*http.Response, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("序列化请求体失败: %v", err)
	}
	// 不带 Origin：非浏览器客户端（curl 等）的形态，闸门放行。
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	return resp, buf.Bytes()
}

func TestLocalFSEndpoints(t *testing.T) {
	addr := newUploadTestServer(t, t.TempDir())
	base := "http://" + addr + "/api"

	// --- 盘符列表（选择器的「此电脑」视图）---
	resp, body := postJSON(t, base+"/fs/drives", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET 盘符列表应 200，got %d: %s", resp.StatusCode, body)
	}
	var root localListing
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("盘符响应不是合法 JSON: %v (%s)", err, body)
	}
	if root.Path != "" || root.Parent != "" {
		t.Errorf("根视图 Path/Parent 应为空串，got %q/%q", root.Path, root.Parent)
	}
	if len(root.Drives) == 0 {
		t.Fatalf("盘符列表为空: %s", body)
	}

	// --- 列子目录：只列目录、文件不计 ---
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub-a"), 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	resp, body = postJSON(t, base+"/fs/dirs", map[string]string{"path": dir})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列目录应 200，got %d: %s", resp.StatusCode, body)
	}
	var listing localListing
	if err := json.Unmarshal(body, &listing); err != nil {
		t.Fatalf("目录响应不是合法 JSON: %v (%s)", err, body)
	}
	if listing.Path != filepath.Clean(dir) {
		t.Errorf("Path = %q，期望 %q", listing.Path, filepath.Clean(dir))
	}
	if listing.Parent == "" {
		t.Error("Parent 不应为空（临时目录必有上级）—— 前端「上一级」按钮据此启用")
	}
	if len(listing.Dirs) != 1 || listing.Dirs[0].Name != "sub-a" {
		t.Fatalf("应只列出 1 个目录 sub-a，got %+v", listing.Dirs)
	}

	// --- 新建目录并进入 ---
	resp, body = postJSON(t, base+"/fs/mkdir", map[string]string{"parent": dir, "name": "新建夹"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("新建目录应 200，got %d: %s", resp.StatusCode, body)
	}
	var created string
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("新建目录应返回路径字符串，got %s", body)
	}
	if created != filepath.Join(filepath.Clean(dir), "新建夹") {
		t.Errorf("新建目录返回 %q，期望 %q", created, filepath.Join(filepath.Clean(dir), "新建夹"))
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("目录未真正建出: %v", err)
	}

	// --- 失败必须回「可展示的中文消息」，而不是裸状态码 ---
	// 前端统一解包 {code,message}；若这里退化成纯文本，选择器只能显示
	// 「HTTP 400」——正是要杜绝的展示缺口。
	resp, body = postJSON(t, base+"/fs/dirs", map[string]string{"path": filepath.Join(dir, "nope")})
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("不存在的目录不应 200: %s", body)
	}
	var apiErr struct{ Code, Message string }
	if err := json.Unmarshal(body, &apiErr); err != nil {
		t.Fatalf("错误响应应为 {code,message} JSON: %v (%s)", err, body)
	}
	if apiErr.Message == "" || !strings.Contains(apiErr.Message, "不存在") {
		t.Errorf("错误消息应说明原因，got %q", apiErr.Message)
	}
}

// 「仅本机」拒绝：/api/ 路径必须回 JSON（否则前端只剩 HTTP 403 可显示），
// 非 API 路径保持纯文本（与历史行为一致）。
func TestLocalOnlyErrResponse(t *testing.T) {
	apiReq := httptest.NewRequest(http.MethodPost, "/api/fs/dirs", nil)
	w := httptest.NewRecorder()
	writeLocalOnlyErr(w, apiReq, "本机目录浏览")
	if w.Code != http.StatusForbidden {
		t.Fatalf("应 403，got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("API 路径应回 JSON，Content-Type = %q", ct)
	}
	var apiErr struct{ Code, Message string }
	if err := json.Unmarshal(w.Body.Bytes(), &apiErr); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, w.Body.String())
	}
	if !strings.Contains(apiErr.Message, "仅允许在本机执行") {
		t.Errorf("消息应说明仅限本机，got %q", apiErr.Message)
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/", nil)
	w2 := httptest.NewRecorder()
	writeLocalOnlyErr(w2, pageReq, "退出程序")
	if w2.Code != http.StatusForbidden || strings.HasPrefix(w2.Header().Get("Content-Type"), "application/json") {
		t.Errorf("非 API 路径应保持纯文本 403，got %d/%q", w2.Code, w2.Header().Get("Content-Type"))
	}
}
