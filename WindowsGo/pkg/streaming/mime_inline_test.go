package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// TestStreamNeverInlinesScriptableTypes 钉死 /s/ 的类型白名单。
//
// /s/ 与主界面同源、不带 Content-Disposition，而文件名完全由上传者决定。
// 一旦按扩展名内联渲染 .html / .svg，脚本就运行在应用源上，而回环来源本就
// 不校验访问令牌 —— 脚本可直接调 /api/*（解密下载、删文件、改设置），
// 构成一条完整的存储型 XSS。本测试同时覆盖单元口径（白名单判定）与端到端
// 口径（响应头），防止后续有人把 mime.TypeByExtension 那种「问注册表」的
// 兜底加回来。
func TestStreamNeverInlinesScriptableTypes(t *testing.T) {
	// 单元口径：可执行文档与未知扩展名不得内联
	for _, name := range []string{
		"evil.html", "evil.HTM", "evil.svg", "evil.xml", "evil.xhtml",
		"evil.js", "evil.mhtml", "evil.shtml", "无扩展名",
	} {
		if ct, inline := StreamContentType(name); inline {
			t.Errorf("%s 不应内联渲染，实得 Content-Type=%s", name, ct)
		}
		if ct := MIMEForDisplayName(name); ct != "application/octet-stream" {
			t.Errorf("%s 的类型应为 octet-stream，实得 %s", name, ct)
		}
	}
	// 单元口径：预览依赖的类型必须仍然内联
	for _, name := range []string{"a.mp4", "b.mkv", "c.mp3", "d.flac", "e.png", "f.jpg", "g.pdf", "h.txt"} {
		if _, inline := StreamContentType(name); !inline {
			t.Errorf("%s 应保持内联渲染（预览依赖）", name)
		}
	}

	// 端到端口径：扩展名全程由展示名决定，响应头必须落地
	backend, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New(testPW)
	uploadPlain(t, sess, backend, testPlain(4), "media/evil.cpenc")

	srv := NewServer(sess, backend)
	entry, err := srv.RegisterStream(context.Background(), "media/evil.cpenc", "报告.html")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/s/" + entry.Token + "/%E6%8A%A5%E5%91%8A.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("html 展示名的 /s/ 响应类型应为 octet-stream，实得 %s", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("html 展示名的 /s/ 必须按附件下载，实得 Content-Disposition=%q", cd)
	}
}
