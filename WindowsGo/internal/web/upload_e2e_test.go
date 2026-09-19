package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/bind"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/secret"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/transfer"
)

// TestUploadFolderKeepsStructure 是「上传文件夹」的端到端守卫。
//
// 它钉住的是一个真实踩过的缺陷：浏览器只把每个 File 的**文件名**放进 multipart，
// 相对路径在 web 层被 filepath.Base 拍平，于是用户拖一个文件夹进来，密库里
// 得到的是一堆散落在当前目录的文件（且因为同名会被覆盖），目录结构整棵丢失，
// 全程没有任何报错。
//
// 断言分三层：
//  1. 远端（本地文件夹后端）出现 photos/2026/ 两级目录 —— 结构被保留；
//  2. 叶子是一份密文（.cpenc），即确实走了加密上传管线；
//  3. 暂存目录 data/tmp/cp-upload-* 在任务终态后被回收 —— 明文不留在盘上。
func TestUploadFolderKeepsStructure(t *testing.T) {
	dataDir := t.TempDir()
	// 让 paths.TempDir 落到本测试的沙箱里：暂存明文与"是否被回收"都可断言
	t.Setenv("CLOUDPRISM_DATA_DIR", dataDir)

	vaultDir := filepath.Join(dataDir, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("建密库目录失败: %v", err)
	}

	addr := newUploadTestServer(t, dataDir)

	// —— 建库：本地文件夹后端，关闭文件名加密以便断言目录名 ——
	if _, err := http.Post(
		"http://"+addr+"/api/vault/open",
		"application/json",
		jsonBody(t, map[string]any{
			"kind":           "local",
			"localDir":       vaultDir,
			"masterPassword": "test-master-password",
			"filenameEnc":    false,
			"create":         true,
			"vaultName":      "e2e-upload",
		}),
	); err != nil {
		t.Fatalf("建库请求失败: %v", err)
	}

	// —— 上传一个两级目录下的文件 ——
	body, ctype := multipartBody(t, []multipartFile{
		{rel: "photos/2026/a.jpg", content: "jpeg-bytes"},
		{rel: "photos/cover.jpg", content: "cover-bytes"},
		{rel: "root.txt", content: "root"},
	}, "")
	resp, err := http.Post("http://"+addr+"/api/transfer/upload", ctype, body)
	if err != nil {
		t.Fatalf("上传请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("上传应 200，实得 %d：%s", resp.StatusCode, raw)
	}

	// —— 断言 1：目录结构保留 ——
	// 三个文件是**各自独立的任务**，完成顺序不保证（walk 按字典序，root.txt 反而靠后），
	// 所以逐个轮询，不能等第一个就立刻断言其余已就位。
	for _, rel := range []string{
		filepath.Join("photos", "2026", "a.jpg.cpenc"),
		filepath.Join("photos", "cover.jpg.cpenc"),
		"root.txt.cpenc",
	} {
		p := filepath.Join(vaultDir, rel)
		waitForFile(t, p, 20*time.Second)
		// 断言 2：叶子是密文（比明文长）
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", rel, err)
		}
		if st.Size() <= int64(len("jpeg-bytes")) {
			t.Errorf("%s 体积 %d 不像密文", rel, st.Size())
		}
	}

	// 拍平的旧行为会把 photos/ 下的两个文件也写到密库根：显式确认根下的文件
	// 只有用户真正放在根的那个，以及密库自身的标记文件。
	entries, err := os.ReadDir(vaultDir)
	if err != nil {
		t.Fatalf("读密库目录失败: %v", err)
	}
	allowed := map[string]bool{"root.txt.cpenc": true, ".cloudprism_vault": true}
	for _, e := range entries {
		if e.IsDir() || allowed[e.Name()] {
			continue
		}
		t.Errorf("密库根下出现意外文件（说明目录结构被拍平）：%s", e.Name())
	}

	// —— 断言 3：暂存明文已被回收 ——
	waitForNoStage(t, dataDir, 10*time.Second)
}

// TestUploadRejectsTraversalPath 确认非法相对路径会让整次上传失败，
// 而不是静默降级成"拍平上传"。安全边界，必须钉住。
func TestUploadRejectsTraversalPath(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLOUDPRISM_DATA_DIR", dataDir)
	vaultDir := filepath.Join(dataDir, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("建密库目录失败: %v", err)
	}
	addr := newUploadTestServer(t, dataDir)

	if _, err := http.Post(
		"http://"+addr+"/api/vault/open",
		"application/json",
		jsonBody(t, map[string]any{
			"kind": "local", "localDir": vaultDir,
			"masterPassword": "test-master-password", "create": true, "vaultName": "e2e-evil",
		}),
	); err != nil {
		t.Fatalf("建库请求失败: %v", err)
	}

	body, ctype := multipartBody(t, []multipartFile{
		{rel: "../../escape.txt", content: "x"},
	}, "")
	resp, err := http.Post("http://"+addr+"/api/transfer/upload", ctype, body)
	if err != nil {
		t.Fatalf("上传请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("含上跳段的路径必须被拒绝，实得 200")
	}
	// 暂存目录同样不得残留
	waitForNoStage(t, dataDir, 5*time.Second)
}

/* --------------------------------------------------------------- 测试辅助 */

type multipartFile struct {
	rel     string
	content string
}

// multipartBody 构造与前端 Transfer.Upload 完全一致的表单：
// files 与 paths 同序平行、外加 remoteDir。
func multipartBody(t *testing.T, files []multipartFile, remoteDir string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	rels := make([]string, 0, len(files))
	for _, f := range files {
		part, err := w.CreateFormFile("files", filepath.Base(f.rel))
		if err != nil {
			t.Fatalf("构造表单文件失败: %v", err)
		}
		if _, err := part.Write([]byte(f.content)); err != nil {
			t.Fatalf("写入表单内容失败: %v", err)
		}
		rels = append(rels, f.rel)
	}
	if err := w.WriteField("paths", mustJSON(t, rels)); err != nil {
		t.Fatalf("写 paths 字段失败: %v", err)
	}
	if err := w.WriteField("remoteDir", remoteDir); err != nil {
		t.Fatalf("写 remoteDir 字段失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}
	return &buf, w.FormDataContentType()
}

func jsonBody(t *testing.T, v any) io.Reader {
	t.Helper()
	return strings.NewReader(mustJSON(t, v))
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return string(raw)
}

// newUploadTestServer 装配一套与实际运行等价的依赖图，绑回环临时端口。
// 与 lan_e2e_test.go 的差别：这里不需要局域网闸门，只要能打通 API。
func newUploadTestServer(t *testing.T, dataDir string) string {
	t.Helper()
	store, err := settings.Open(filepath.Join(dataDir, "config.json"))
	if err != nil {
		t.Fatalf("打开设置存储失败: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := appstate.New(appstate.Config{Store: store, Queue: transfer.New(), Log: logger})
	holder := bind.NewContextHolder()
	lan := bind.NewLan(st, secret.NewFile(filepath.Join(dataDir, "lan_token"), nil))

	dist := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>cp</html>")}}
	srv := New(logger, st, holder,
		bind.NewVault(st, holder), bind.NewFiles(st, holder), bind.NewTransfer(st, holder),
		bind.NewSettings(st, holder), bind.NewPreview(st, holder), lan, dist)

	addr, err := srv.Listen("127.0.0.1", 0, "")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return addr
}

// waitForFile 轮询等待文件出现（上传是异步的，不能立即断言）。
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("超时未出现：%s", path)
}

// waitForNoStage 等待暂存目录被任务终态回调回收干净。
func waitForNoStage(t *testing.T, dataDir string, timeout time.Duration) {
	t.Helper()
	tmp, _ := paths.TempDir(true)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		leftovers, _ := filepath.Glob(filepath.Join(tmp, appstate.StagedUploadDirPrefix+"*"))
		if len(leftovers) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	leftovers, _ := filepath.Glob(filepath.Join(tmp, appstate.StagedUploadDirPrefix+"*"))
	t.Fatalf("暂存明文未被回收（明文不得留盘）：%v", leftovers)
}
