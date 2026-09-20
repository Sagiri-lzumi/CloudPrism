package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFrameKeepsTerminalTasks 钉住「传输页任务列表在传输结束后瞬间清空」这个缺陷。
//
// 现象：上传一完成，传输页列表立刻变空，「取消全部 / 清空已结束」双双置灰，
// 而队列里那些任务其实都还在（终态任务按设计保留，供重试与清空）。
//
// 根因：状态帧只在 TransferActive 为真时才带 Tasks，而终态任务不计入
// TransferActive（done == total 且无在飞）—— 前端 ui.tasks 于是被逐帧刷成空。
//
// 断言分两段，正好是组帧条件的两侧：
//  1. 上传完成、TransferActive 已为假时，帧仍必须带终态任务（缺陷点）；
//  2. 用户点「清空已结束」后，帧停发任务明细（每任务约 200B × 10Hz 的体积契约，
//     不能为了修 (1) 就永远背着任务列表空转）。
//
// 走的是真实 SSE 端点而不是直接调 buildFrame：前端拿到的就是 SSE 首帧，
// 只测方法本身挡不住「handleSSE 又改回自己那套判定」的回归。
func TestFrameKeepsTerminalTasks(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLOUDPRISM_DATA_DIR", dataDir)
	vaultDir := filepath.Join(dataDir, "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("建密库目录失败: %v", err)
	}
	addr, srv := newUploadTestServerWithSrv(t, dataDir)

	if _, err := http.Post(
		"http://"+addr+"/api/vault/open",
		"application/json",
		jsonBody(t, map[string]any{
			"kind": "local", "localDir": vaultDir,
			"masterPassword": "test-master-password", "create": true, "vaultName": "e2e-frame",
		}),
	); err != nil {
		t.Fatalf("建库请求失败: %v", err)
	}

	// —— 上传一个小文件，等它跑到终态 ——
	body, ctype := multipartBody(t, []multipartFile{{rel: "note.txt", content: "hello"}}, "")
	resp, err := http.Post("http://"+addr+"/api/transfer/upload", ctype, body)
	if err != nil {
		t.Fatalf("上传请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("上传应 200，实得 %d", resp.StatusCode)
	}

	// 终态的判据用 TransferActive 变假（入队后 done(=0) < total(=1) 为真，
	// 完成后为假），比文件落盘更贴近组帧条件；文件已落盘则再确认一次。
	waitForCond(t, "上传任务进入终态", 20*time.Second, func() bool {
		return !srv.buildFrame().Snap.TransferActive
	})
	waitForFile(t, filepath.Join(vaultDir, "note.txt.cpenc"), 5*time.Second)

	// —— 断言 1：此时帧必须仍带终态任务（缺陷点）——
	f := readFirstFrame(t, addr)
	if f.Snap.TransferActive {
		t.Fatalf("前置条件不成立：此刻不应还有在飞传输")
	}
	if len(f.Tasks) == 0 {
		t.Fatalf("上传完成后状态帧丢失了终态任务：前端传输页会变空、重试/清空永远点不到")
	}
	if got := f.Tasks[0].State; got != "done" {
		t.Errorf("任务应为终态 done，实得 %q", got)
	}

	// —— 断言 2：清空已结束后停发任务明细（体积契约）——
	if resp, err := http.Post(
		"http://"+addr+"/api/transfer/clearfinished", "application/json", nil,
	); err != nil {
		t.Fatalf("清空已结束请求失败: %v", err)
	} else {
		resp.Body.Close()
	}
	waitForCond(t, "队列被清空后帧不再携带任务", 5*time.Second, func() bool {
		return len(srv.buildFrame().Tasks) == 0
	})
	if f := readFirstFrame(t, addr); len(f.Tasks) != 0 {
		t.Errorf("清空已结束后帧仍带 %d 个任务，任务明细未停发", len(f.Tasks))
	}
}

/* --------------------------------------------------------------- 测试辅助 */

// readFirstFrame 连一次事件流，取回服务端在连接建立时立刻发出的首帧。
//
// 首帧由 handleSSE 在注册客户端之后同步写出并 flush（早于合帧循环的任何一帧），
// 因此它是「此刻的组帧结果」的忠实采样，不受 10Hz 循环与客户端缓冲影响。
func readFirstFrame(t *testing.T, addr string) frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/events", nil)
	if err != nil {
		t.Fatalf("构造事件流请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("连接事件流失败: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	var evName string
	var line string
	for {
		line, err = br.ReadString('\n')
		if err != nil {
			t.Fatalf("读事件流失败（未取到 st:frame）: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			evName = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		case strings.HasPrefix(line, "data: ") && evName == "st:frame":
			var f frame
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f); err != nil {
				t.Fatalf("解析状态帧失败: %v", err)
			}
			return f
		}
	}
}

// waitForCond 轮询等待条件成立（异步任务与合帧都有延迟，不能立即断言）。
func waitForCond(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("超时未达成：%s", what)
}
