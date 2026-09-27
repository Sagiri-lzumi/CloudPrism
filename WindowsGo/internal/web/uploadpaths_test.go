package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
)

// 本文件是「直读本机路径上传」（按钮通路）的端到端守卫。
//
// 起因（用户 2026-09-27）：浏览器界面导入文件夹时，文件内容必须先经浏览器
// 读到暂存目录 —— 用户机器上就是 C 盘，等于把整个文件夹再抄一份。
// 而本程序本来就跑在同一台机器上，后端完全可以直接打开这些文件。
//
// 于是分成两条通路：
//   - **拖放**：浏览器只给 File 对象（拿不到绝对路径），照旧 multipart +
//     暂存目录 —— 物理上无法避免，行为保持原样；
//   - **按钮**（上传文件 / 上传文件夹）：走网页版路径选择器拿绝对路径，
//     直接交给 appstate.UploadPaths，全程不经过浏览器。
//
// 这组测试钉住三件事：
//  1. 直读通路确实**不产生任何暂存目录** —— 否则等于没省下那份 C 盘副本，
//     整个改动就白做了；
//  2. 目录结构与浏览器通路一致（两条通路共用 expandLocalPaths，不能各自为政）；
//  3. 上传前清单（scanpaths）与实际入队的东西**同源** —— 预览数字不能骗人。

// TestDirectPathEndpointsAreLocalOnly 是最硬的一道边界，单列出来是为了让
// 「谁把它从 localOnlyPaths 里删了」立刻红。
//
// 这两个端点能让后端读取主机上**任意路径的文件内容**（比 /api/fs/* 只泄露
// 目录名更强）。一旦对局域网放开，同一网段任何拿到令牌的人都能把宿主机的
// 文件搬到远端存储 —— 那是远程文件窃取。
func TestDirectPathEndpointsAreLocalOnly(t *testing.T) {
	for _, p := range []string{"/api/transfer/scanpaths", "/api/transfer/uploadpaths"} {
		if _, ok := localOnlyPaths[p]; !ok {
			t.Errorf("%s 必须登记在 localOnlyPaths（仅回环）—— 它能读主机任意路径的内容", p)
		}
	}
}

// TestUploadPathsDirectReadsWithoutStaging 是本次改动的核心断言：
// 直读上传后，暂存目录一个都不该出现。
func TestUploadPathsDirectReadsWithoutStaging(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLOUDPRISM_DATA_DIR", dataDir)

	vaultDir := filepath.Join(dataDir, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("建密库目录失败: %v", err)
	}
	// 待上传的源目录放在密库之外，避免与「密库内容」互相干扰
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"root.txt":         "root-bytes",
		"alpha/a.txt":      "alpha-bytes",
		"alpha/deep/b.txt": "deep-bytes",
		"beta/cover.jpg":   "cover-bytes",
		"beta/unused.txt":  "unused",
	})

	addr := newUploadTestServer(t, dataDir)
	openLocalVault(t, addr, vaultDir, "e2e-direct")

	resp, body := postJSON(t, "http://"+addr+"/api/transfer/uploadpaths", map[string]any{
		"paths":     []string{src},
		"remoteDir": "",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("直读上传应 200，实得 %d：%s", resp.StatusCode, body)
	}

	// —— 断言 1（核心）：全程没有暂存目录 ——
	//
	// 这里是「立刻查」而不是「等它消失」：浏览器通路的暂存目录在**响应返回时
	// 必然存在**（handler 同步写完 stage 才入队，回收要等任务终态）。所以
	// 「返回后为 0」足以区分两条通路，不是时序巧合。
	tmp, ok := paths.TempDir(true)
	if !ok {
		t.Fatal("取暂存根失败")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(tmp, appstate.StagedUploadDirPrefix+"*")); len(leftovers) != 0 {
		t.Errorf("直读通路不该产生暂存目录（正是本次要消除的 C 盘副本）：%v", leftovers)
	}

	// —— 断言 2：目录结构与密文落盘 ——
	// 与浏览器通路（TestUploadFolderKeepsStructure）同一套期望。
	for _, rel := range []string{
		"root.txt.cpenc",
		filepath.Join("alpha", "a.txt.cpenc"),
		filepath.Join("alpha", "deep", "b.txt.cpenc"),
		filepath.Join("beta", "cover.jpg.cpenc"),
	} {
		waitForCiphertext(t, filepath.Join(vaultDir, rel), 1, 20*time.Second)
	}

	// 拍平的旧行为会把子目录里的文件也写到密库根：显式确认根下只有真正在根的那个。
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

	// 收尾再确认一次：跑完全程也没有留下暂存目录。
	waitForNoStage(t, dataDir, 10*time.Second)
}

// TestScanPathsMatchesUploadSet 钉住「清单与实际入队同源」。
//
// scanpaths 只扫描、不入队，是给用户确认用的预览。它若与实际传输的文件集合
// 有任何偏差，用户看到的数字就是骗人的 —— 那比不给清单更糟。两者共用
// appstate.expandLocalPaths，这条测试是那个共用关系的守卫。
func TestScanPathsMatchesUploadSet(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLOUDPRISM_DATA_DIR", dataDir)

	vaultDir := filepath.Join(dataDir, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("建密库目录失败: %v", err)
	}
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"root.txt":           "0123456789", // 10 字节
		"sub/one.bin":        "01234",      // 5
		"sub/deeper/two.bin": "0123456",    // 7
		"other/three.bin":    "01",         // 2
	})

	// 额外再选一个**单文件**：它的 rel 应退化为文件名（平铺进目标目录），
	// 这是与目录展开不同的一条分支，必须一起验。
	loose := filepath.Join(t.TempDir(), "loose.txt")
	if err := os.WriteFile(loose, []byte("abc"), 0o644); err != nil {
		t.Fatalf("准备单文件失败: %v", err)
	}
	pathsArg := []string{src, loose}

	addr := newUploadTestServer(t, dataDir)

	// —— 清单可以在**未连接密库**时使用：它是纯本地扫描，不该依赖连接态 ——
	resp, body := postJSON(t, "http://"+addr+"/api/transfer/scanpaths", map[string]any{"paths": pathsArg})
	plan := decodePlan(t, resp, body)

	const wantFiles = 5 // 4 个树内文件 + 1 个单文件
	if plan.TotalFiles != wantFiles {
		t.Errorf("TotalFiles = %d，期望 %d", plan.TotalFiles, wantFiles)
	}
	if plan.TotalDirs != 3 { // sub, sub/deeper, other
		t.Errorf("TotalDirs = %d，期望 3", plan.TotalDirs)
	}
	if wantBytes := int64(10 + 5 + 7 + 2 + 3); plan.TotalBytes != wantBytes {
		t.Errorf("TotalBytes = %d，期望 %d", plan.TotalBytes, wantBytes)
	}
	if plan.Truncated {
		t.Error("小目录不该被截断")
	}
	if len(plan.Items) != wantFiles {
		t.Errorf("明细条数 = %d，期望 %d", len(plan.Items), wantFiles)
	}
	// 明细里的相对路径必须覆盖目录段与平铺两种形态
	got := map[string]bool{}
	for _, it := range plan.Items {
		got[it.Rel] = true
	}
	for _, want := range []string{"root.txt", "sub/one.bin", "sub/deeper/two.bin", "other/three.bin", "loose.txt"} {
		if !got[want] {
			t.Errorf("清单缺少 %q（实得 %v）", want, got)
		}
	}

	// —— 真正入队：应与清单是同一批文件 ——
	openLocalVault(t, addr, vaultDir, "e2e-scan")
	resp2, body2 := postJSON(t, "http://"+addr+"/api/transfer/uploadpaths", map[string]any{
		"paths":     pathsArg,
		"remoteDir": "",
	})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("直读上传应 200，实得 %d：%s", resp2.StatusCode, body2)
	}

	// 清单里每一条都该在密库里找到对应的密文 —— 一条不少。
	for _, rel := range []string{"root.txt", "sub/one.bin", "sub/deeper/two.bin", "other/three.bin", "loose.txt"} {
		waitForCiphertext(t, filepath.Join(vaultDir, filepath.FromSlash(rel)+".cpenc"), 1, 20*time.Second)
	}
	// 也不能多出别的：根下只应有 2 个根级密文 + 密库标记（其余都在子目录里）。
	roots, err := os.ReadDir(vaultDir)
	if err != nil {
		t.Fatalf("读密库目录失败: %v", err)
	}
	var rootFiles []string
	for _, e := range roots {
		if !e.IsDir() {
			rootFiles = append(rootFiles, e.Name())
		}
	}
	// 期望：root.txt.cpenc / loose.txt.cpenc / .cloudprism_vault
	if len(rootFiles) != 3 {
		t.Errorf("密库根下文件 = %v，期望 3 项（清单与实际传输不一致）", rootFiles)
	}
}

// TestScanPathsRejectsEmptySelection 空选应当场拒绝，而不是返回一份 0 文件的
// 「成功」清单 —— 那会让用户以为操作生效了。
func TestScanPathsRejectsEmptySelection(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLOUDPRISM_DATA_DIR", dataDir)
	addr := newUploadTestServer(t, dataDir)

	for _, path := range []string{"/api/transfer/scanpaths", "/api/transfer/uploadpaths"} {
		resp, body := postJSON(t, "http://"+addr+path, map[string]any{"paths": []string{}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s 空选应 400，实得 %d：%s", path, resp.StatusCode, body)
		}
	}
}

// TestScanPathsReportsMissingPath 不存在的路径必须报错而不是静默跳过：
// 用户可能选了一个刚被删掉/拔掉的盘，静默成功等于假装传了。
func TestScanPathsReportsMissingPath(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLOUDPRISM_DATA_DIR", dataDir)
	addr := newUploadTestServer(t, dataDir)

	missing := filepath.Join(t.TempDir(), "nope-does-not-exist")
	resp, body := postJSON(t, "http://"+addr+"/api/transfer/scanpaths", map[string]any{"paths": []string{missing}})
	if resp.StatusCode == http.StatusOK {
		t.Error("不存在的路径应报错，而不是返回空清单")
	}
	if !strings.Contains(string(body), "nope-does-not-exist") {
		t.Errorf("错误信息应指出是哪条路径：%s", body)
	}
}

// TestUploadPathsRequiresVault 直读上传必须已有连接；未连接时返回业务错误
// 而不是 panic（拖放通路同样如此，这里只是把边界钉在明面上）。
func TestUploadPathsRequiresVault(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLOUDPRISM_DATA_DIR", dataDir)
	addr := newUploadTestServer(t, dataDir)

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	resp, body := postJSON(t, "http://"+addr+"/api/transfer/uploadpaths", map[string]any{"paths": []string{src}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("未连接密库时直读上传应 400，实得 %d：%s", resp.StatusCode, body)
	}
}

/* ------------------------------------------------------------------ 辅助 */

// writeTree 在 root 下按相对路径建文件（自动建父目录）。
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("建目录失败 %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("写文件失败 %s: %v", rel, err)
		}
	}
}

// decodePlan 解析 scanpaths 的清单响应。
func decodePlan(t *testing.T, resp *http.Response, body []byte) appstate.UploadPlan {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("扫描应 200，实得 %d：%s", resp.StatusCode, body)
	}
	var plan appstate.UploadPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatalf("清单不是合法 JSON: %v（%s）", err, body)
	}
	return plan
}

// openLocalVault 在 addr 上打开（或新建）一个本地文件夹密库。
func openLocalVault(t *testing.T, addr, vaultDir, name string) {
	t.Helper()
	resp, body := postJSON(t, "http://"+addr+"/api/vault/open", map[string]any{
		"kind":           "local",
		"localDir":       vaultDir,
		"masterPassword": "test-master-password",
		"filenameEnc":    false,
		"create":         true,
		"vaultName":      name,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("建库应 200，实得 %d：%s", resp.StatusCode, body)
	}
}
