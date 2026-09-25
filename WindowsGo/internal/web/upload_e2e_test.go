package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/bind"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
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
		// 断言 1 + 断言 2：等到它出现**并且**体积真的超过明文。
		// 只等「存在」是不够的：上传端会先以 O_CREATE 建出目标文件再写内容，
		// 中间有个 0 字节空窗（实测约 20 ms），撞上就会误判成「体积不像密文」。
		waitForCiphertext(t, p, int64(len("jpeg-bytes")), 20*time.Second)
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

// TestUploadFolderDoesNotDuplicateDirs 是「上传一个文件夹却生成多个文件夹」的
// 回归守卫（v1.01 及更早的真实缺陷）。
//
// 病因：目录名加密用的是**随机 nonce**，同一个明文目录每被加密一次就得到一个新
// 密文名。一次上传里 `photos/` 至少要加密两次（遍历到目录时 Mkdir 一次、每个
// 文件的父目录段各一次），于是云端出现多个「解密后同名」的目录；用户拖一个
// 文件夹进来，得到的是好几层同名目录，还有一堆空壳。
//
// 修法：目录段改用确定性加密（cryptox.EncryptDirName），并在上传前复用远端
// 已存在的同名目录。本测试在**文件名加密开启**（这才有加密目录名）的密库上
// 断言三条不变式：
//  1. 逻辑目录数 == 密文目录数（photos + photos/2026 恰好 2 个，且都不为空）；
//  2. 同级文件真的落在那个目录里，而不是另起一个同名目录；
//  3. 同一个文件夹**再传一次**不新增目录（复用既有那棵），也不新增空壳。
func TestUploadFolderDoesNotDuplicateDirs(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLOUDPRISM_DATA_DIR", dataDir)

	vaultDir := filepath.Join(dataDir, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("建密库目录失败: %v", err)
	}
	addr := newUploadTestServer(t, dataDir)

	// 文件名加密开启：目录名也是密文，才可能撞上这个 bug
	if _, err := http.Post(
		"http://"+addr+"/api/vault/open",
		"application/json",
		jsonBody(t, map[string]any{
			"kind": "local", "localDir": vaultDir,
			"masterPassword": "test-master-password",
			"filenameEnc":    true, "create": true, "vaultName": "e2e-dirs",
		}),
	); err != nil {
		t.Fatalf("建库请求失败: %v", err)
	}

	batch := []multipartFile{
		{rel: "photos/cover.jpg", content: "cover-bytes"},
		{rel: "photos/2026/a.jpg", content: "jpeg-bytes"},
		{rel: "photos/2026/b.jpg", content: "jpeg-bytes-2"},
		{rel: "root.txt", content: "root"},
	}

	for pass := 1; pass <= 2; pass++ {
		body, ctype := multipartBody(t, batch, "")
		resp, err := http.Post("http://"+addr+"/api/transfer/upload", ctype, body)
		if err != nil {
			t.Fatalf("第 %d 次上传请求失败: %v", pass, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 次上传应 200，实得 %d", pass, resp.StatusCode)
		}
		// 文件按 pass 累积：第 1 次 4 个，第 2 次 8 个（同名文件是随机密文名，
		// 不会覆盖，属于既有设计）
		waitForLeafCount(t, vaultDir, pass*len(batch), 20*time.Second)

		dirs, files := walkVault(t, vaultDir)
		// 1) photos 与 photos/2026 恰好两个密文目录
		if len(dirs) != 2 {
			t.Fatalf("第 %d 次上传后密文目录数 = %d，应为 2（%v）—— 目录被重复创建了",
				pass, len(dirs), dirs)
		}
		// 2) 没有空壳目录：每个目录里都至少有一个文件
		for _, d := range dirs {
			if n := countDirFiles(t, filepath.Join(vaultDir, d)); n == 0 {
				t.Errorf("第 %d 次上传后出现空目录 %s（这正是旧实现留下的空壳）", pass, d)
			}
		}
		// 3) 叶子全是密文容器
		for _, f := range files {
			if !strings.HasSuffix(f, protocol.FileExtension) {
				t.Errorf("密库里出现了非 .cpenc 叶子：%s", f)
			}
		}
		// 4) 层级：根级只有一个（photos），photos 下只有一个（2026），再往下没有
		if got := countSubdirs(t, dirs, 0); got != 1 {
			t.Errorf("第 %d 次上传后根级密文目录数 = %d，应为 1", pass, got)
		}
		if got := countSubdirs(t, dirs, 1); got != 1 {
			t.Errorf("第 %d 次上传后 photos 下的密文目录数 = %d，应为 1（2026）", pass, got)
		}
		if got := countSubdirs(t, dirs, 2); got != 0 {
			t.Errorf("第 %d 次上传后三级密文目录数 = %d，应为 0", pass, got)
		}
	}
}

// TestUploadLargeFileIsPartedOnRemote 是「云端分卷」的端到端守卫。
//
// 它钉住的是一个真实存在的缺陷：几个 GB 的视频被**整份**推上云端 ——
// 分卷能力写在设置里、界面上有档位，但上传链路上根本没接，用户看到的是
// 「一个几 G 的文件直接放上去」。这类「看起来做了、实际没生效」的功能
// 最容易在下一次重构里再次静默失效，所以从设置档位到远端对象名整条链路
// 都要断言。
//
// 断言分三层：
//  1. 远端（本地文件夹后端）真的出现 big.mp4.cpenc.part-1…N，且没有整份对象；
//  2. 每卷不超过档位，卷内容按序拼起来就是完整的容器（首卷以密文魔数开头）；
//  3. 目录列表里用户看到的**仍是一个** big.mp4，大小是各卷之和 ——
//     分卷对上层完全透明。
func TestUploadLargeFileIsPartedOnRemote(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLOUDPRISM_DATA_DIR", dataDir)

	// 把分卷尺寸压到最小档（4 MB），否则要造 64 MB 以上的测试数据。
	// 键名与 Go 端 settings.KeyChunkIndex 一致（transfer/chunk_index）。
	if err := os.WriteFile(
		filepath.Join(dataDir, "config.json"),
		[]byte(`{"transfer/chunk_index":"0"}`),
		0o644,
	); err != nil {
		t.Fatalf("预置设置失败: %v", err)
	}

	vaultDir := filepath.Join(dataDir, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("建密库目录失败: %v", err)
	}
	addr := newUploadTestServer(t, dataDir)

	// 关闭文件名加密：本次验证的是「切不切卷」，不是「名字怎么加密」
	if resp, err := http.Post(
		"http://"+addr+"/api/vault/open",
		"application/json",
		jsonBody(t, map[string]any{
			"kind": "local", "localDir": vaultDir,
			"masterPassword": "test-master-password",
			"filenameEnc":    false, "create": true, "vaultName": "e2e-parts",
		}),
	); err != nil {
		t.Fatalf("建库请求失败: %v", err)
	} else {
		resp.Body.Close()
	}

	const partSize = 4 << 20
	// 9.5 MB：整两份满卷 + 一份半卷，末卷短读是分卷最容易出错的边界
	payload := bytes.Repeat([]byte{0xA7}, 2*partSize+partSize/2)
	body, ctype := multipartBody(t, []multipartFile{
		{rel: "videos/big.mp4", content: string(payload)},
	}, "")
	resp, err := http.Post("http://"+addr+"/api/transfer/upload", ctype, body)
	if err != nil {
		t.Fatalf("上传请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("上传应 200，实得 %d", resp.StatusCode)
	}

	// —— 断言 1：远端被拆成多个分卷，且没有整份对象 ——
	remoteDir := filepath.Join(vaultDir, "videos")
	deadline := time.Now().Add(60 * time.Second)
	var parts []string
	for time.Now().Before(deadline) {
		ents, rerr := os.ReadDir(remoteDir)
		if rerr == nil {
			parts = parts[:0]
			for _, e := range ents {
				if !e.IsDir() {
					parts = append(parts, e.Name())
				}
			}
			if len(parts) >= 3 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	sort.Strings(parts)
	if len(parts) != 3 {
		t.Fatalf("远端应有 3 个分卷，实得 %d 个: %v（若只有 1 个整份对象，说明分卷没生效）",
			len(parts), parts)
	}
	wantNames := []string{
		"big.mp4.cpenc.part-1", "big.mp4.cpenc.part-2", "big.mp4.cpenc.part-3",
	}
	for i, want := range wantNames {
		if parts[i] != want {
			t.Fatalf("第 %d 个远端对象 = %q，应为 %q（全部：%v）", i+1, parts[i], want, parts)
		}
	}
	if _, err := os.Stat(filepath.Join(remoteDir, "big.mp4.cpenc")); err == nil {
		t.Error("分卷上传后不应残留整份对象 big.mp4.cpenc")
	}

	// —— 断言 2：每卷不超过档位，拼起来是一份完整容器 ——
	var joined []byte
	sizes := make([]int64, len(parts))
	for i, name := range parts {
		raw, rerr := os.ReadFile(filepath.Join(remoteDir, name))
		if rerr != nil {
			t.Fatalf("读取分卷 %s 失败: %v", name, rerr)
		}
		sizes[i] = int64(len(raw))
		if sizes[i] > partSize {
			t.Errorf("第 %d 卷 %d 字节，超过档位 %d", i+1, sizes[i], partSize)
		}
		joined = append(joined, raw...)
	}
	for i := 0; i < 2; i++ {
		if sizes[i] != partSize {
			t.Errorf("第 %d 卷应为满卷 %d 字节，实得 %d", i+1, partSize, sizes[i])
		}
	}
	if sizes[2] <= 0 || sizes[2] > partSize {
		t.Errorf("末卷大小 %d 应在 (0, %d]", sizes[2], partSize)
	}
	// 容器首字节是密文头（协议真源在 pkg/protocol，这里只校验「不是明文」）
	if len(joined) <= len(payload) {
		t.Errorf("拼回的容器只有 %d 字节，不比明文 %d 更长，不像密文",
			len(joined), len(payload))
	}
	if bytes.HasPrefix(joined, payload[:64]) {
		t.Error("远端内容是明文，加密链路没有生效")
	}

	// —— 断言 3：目录列表里仍是一个文件，大小是各卷之和 ——
	var listed []appstate.FileEntry
	for time.Now().Before(deadline) {
		lresp, lerr := http.Post(
			"http://"+addr+"/api/files/list",
			"application/json",
			jsonBody(t, map[string]any{"remote": "videos"}),
		)
		if lerr == nil {
			raw, _ := io.ReadAll(lresp.Body)
			lresp.Body.Close()
			// /api/files/list 直接回一个 JSON 数组（writeJSON 不加外壳）
			var got []appstate.FileEntry
			if json.Unmarshal(raw, &got) == nil {
				listed = got
			}
		}
		if len(listed) == 1 && listed[0].Size == int64(len(joined)) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(listed) != 1 {
		t.Fatalf("上层应只看到 1 个条目（分卷不该外露），实得 %+v", listed)
	}
	if listed[0].IsDir {
		t.Errorf("条目被当成了目录: %+v", listed[0])
	}
	if listed[0].Display != "big.mp4" {
		t.Errorf("展示名 = %q，应为 big.mp4", listed[0].Display)
	}
	if listed[0].Size != int64(len(joined)) {
		t.Errorf("列表里的大小 = %d，应为各卷之和 %d", listed[0].Size, len(joined))
	}
}

// walkVault 递归列出密库目录下的（相对路径）子目录与文件，跳过密库 Marker
// 与同步索引这类系统文件。
func walkVault(t *testing.T, root string) (dirs, files []string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, rErr := filepath.Rel(root, path)
		if rErr != nil {
			return rErr
		}
		if d.IsDir() {
			dirs = append(dirs, rel)
			return nil
		}
		if strings.HasPrefix(d.Name(), ".cloudprism") {
			return nil // Marker / 同步索引：不是用户内容
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历密库目录失败: %v", err)
	}
	sort.Strings(dirs)
	sort.Strings(files)
	return dirs, files
}

// countDirFiles 数一个目录里的直接文件数（不递归）。
func countDirFiles(t *testing.T, dir string) int {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录 %s 失败: %v", dir, err)
	}
	n := 0
	for _, e := range ents {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// countSubdirs 数深度为 depth 的目录个数（depth 按密文目录列表里的层级算）。
func countSubdirs(t *testing.T, dirs []string, depth int) int {
	t.Helper()
	n := 0
	for _, d := range dirs {
		if strings.Count(filepath.ToSlash(d), "/") == depth {
			n++
		}
	}
	return n
}

// waitForLeafCount 等待密库里的 .cpenc 叶子达到 want 个（上传是异步的）。
func waitForLeafCount(t *testing.T, root string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	got := 0
	for time.Now().Before(deadline) {
		_, files := walkVault(t, root)
		got = len(files)
		if got >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("超时：密库叶子数 %d，期望 %d", got, want)
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
	addr, _ := newUploadTestServerWithSrv(t, dataDir)
	return addr
}

// newUploadTestServerWithSrv 与 newUploadTestServer 同源，额外把 *Server 交出，
// 供需要直接观察服务端内部状态（如组帧结果）的测试使用。
func newUploadTestServerWithSrv(t *testing.T, dataDir string) (string, *Server) {
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
		bind.NewSettings(st, holder), bind.NewPreview(st, holder), bind.NewLocalFS(), lan, dist)

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
	return addr, srv
}

// waitForCiphertext 等待文件出现**且**体积超过 minSize，返回落定后的大小。
//
// 与 waitForFile 的区别在于「写完」而非「存在」：上传端先以 O_CREATE 建出目标
// 文件、再分块写入，中间存在一个 0 字节（或不足长度）的空窗。要断言「这是一份
// 密文、且比明文长」就必须跨过那个空窗，否则测试会随机红。
func waitForCiphertext(t *testing.T, path string, minSize int64, timeout time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last int64
	for time.Now().Before(deadline) {
		st, err := os.Stat(path)
		if err == nil {
			last = st.Size()
			if last > minSize {
				return last
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("超时：%s 未出现体积 > %d 的内容（最后见到 %d 字节）", path, minSize, last)
	return 0
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
