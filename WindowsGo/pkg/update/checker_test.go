package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNormalizeTag 对齐 Python normalize_tag（strip + lstrip("vV")）。
func TestNormalizeTag(t *testing.T) {
	cases := map[string]string{
		"v1.0.0":     "1.0.0",
		"V1.2.3":     "1.2.3",
		"  v2.0.0  ": "2.0.0",
		"vvv3.0":     "3.0",
		"1.0":        "1.0",
		"":           "",
	}
	for in, want := range cases {
		if got := NormalizeTag(in); got != want {
			t.Errorf("NormalizeTag(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestCompareVersions 点分段数字比较（缺位补 0、非数字段容错）。
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0", "1.0.0", 0},    // 缺位补 0
		{"v1.2.3", "1.2.2", 1}, // v 前缀不碍事
		{"1.2.3", "1.2.4", -1},
		{"2.0.0", "1.9.9", 1},        // 高位优先
		{"1.0.0-beta", "1.0.0", 0},   // 非数字段按 0
		{"2026.9.3", "2026.9.4", -1}, // 日期版本号
	}
	for _, c := range cases {
		if got := CompareVersions(c.cur, c.latest); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d，期望 %d", c.cur, c.latest, got, c.want)
		}
	}
}

// TestFetchLatestReleaseOK 200 返回归一化 tag 与回退字段。
func TestFetchLatestReleaseOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("Accept 头不正确")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.4.2","name":"fix 版本","html_url":"https://example.com/r"}`))
	}))
	defer srv.Close()

	info, err := FetchLatestRelease(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if info.Tag != "1.4.2" || info.Name != "fix 版本" || info.URL != "https://example.com/r" {
		t.Errorf("解析结果不符: %+v", info)
	}
}

// TestFetchLatestReleaseFallback html_url/tag_name 缺失时回退。
func TestFetchLatestReleaseFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.9.0"}`))
	}))
	defer srv.Close()

	info, err := FetchLatestRelease(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "v0.9.0" {
		t.Errorf("Name 应回退 tag_name，实得 %q", info.Name)
	}
	if info.URL != ReleasesPage {
		t.Errorf("URL 应回退下载页，实得 %q", info.URL)
	}
}

// TestFetchLatestReleaseErrors 状态码分类：404/403/5xx/非 JSON。
func TestFetchLatestReleaseErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantErr    error
		wantPrefix string
	}{
		{"no-release", http.StatusNotFound, "", ErrNoRelease, ""},
		{"rate-limit", http.StatusForbidden, "", ErrRateLimited, ""},
		{"server-error", http.StatusInternalServerError, "", nil, "GitHub 返回异常状态码"},
		{"bad-json", http.StatusOK, "not-json", nil, "GitHub 返回内容不是 JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()

			_, err := FetchLatestRelease(context.Background(), srv.URL, srv.Client())
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Errorf("错误应为 %v，实得 %v", c.wantErr, err)
				}
				return
			}
			if err == nil || len(c.wantPrefix) > 0 && err.Error()[:len(c.wantPrefix)] != c.wantPrefix {
				t.Errorf("错误应含 %q，实得 %v", c.wantPrefix, err)
			}
		})
	}
}

// TestFetchLatestReleaseNetworkErr 网络不可达包装为「网络请求失败」。
func TestFetchLatestReleaseNetworkErr(t *testing.T) {
	// 未监听端口 → 连接拒绝
	_, err := FetchLatestRelease(context.Background(), "http://127.0.0.1:1", http.DefaultClient)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if err.Error()[:len("网络请求失败")] != "网络请求失败" {
		t.Errorf("错误应含网络前缀，实得 %v", err)
	}
}
