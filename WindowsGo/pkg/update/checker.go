// Package update 检查 CloudPrism 的新版本（GitHub Releases 公共 API）。
//
// 对照 WindowsPy/src/cloudprism/core/update_checker.py：匿名限额 60 次/小时
// （手动检查场景足够）；仅做版本对比与下载页跳转，不自动下载。仓库未来
// 若转私有需在此补充 token 认证。
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// LatestReleaseAPI 最新 release 查询接口（匿名可用）。
const LatestReleaseAPI = "https://api.github.com/repos/Sagiri-lzumi/CloudPrism/releases/latest"

// ReleasesPage 下载页（404/无 html_url 时的引导链接）。
const ReleasesPage = "https://github.com/Sagiri-lzumi/CloudPrism/releases"

// 错误分类（对齐 Python 的 LookupError / ConnectionError 双分支）：
// 404 → ErrNoRelease；403 → ErrRateLimited；其余网络/响应问题包装返回。
var (
	ErrNoRelease   = errors.New("仓库尚未发布任何 release")
	ErrRateLimited = errors.New("GitHub 请求频率受限（限流），请稍后重试")
)

// ReleaseInfo 最新 release 的摘要信息。
type ReleaseInfo struct {
	Tag  string // 归一化版本号（去 v 前缀）
	Name string // 发布名
	URL  string // 下载页
}

// NormalizeTag 归一化版本标签：去空白与 v/V 前缀（"v1.0.0" → "1.0.0"；
// 对照 Python normalize_tag 的 strip + lstrip("vV")，可去除多个前导符）。
func NormalizeTag(tag string) string {
	return strings.TrimLeft(strings.TrimSpace(tag), "vV")
}

// CompareVersions 按点分段数字比较两个版本号。
//
// 返回 -1 / 0 / 1，分别表示 cur 小于 / 等于 / 大于 latest。缺位段补 0
// （"1.0" == "1.0.0"）；非数字段容错为 0，避免 "1.0.0-beta" 之类后缀
// 导致解析异常（对照 update_checker.py:23-44 的逐段整数比较）。
func CompareVersions(cur, latest string) int {
	a, b := parts(cur), parts(latest)
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// parts 拆版本串为数字段（对照 Python parts():36-38，坏段按 0 容错）。
func parts(v string) []int {
	out := []int{}
	for _, seg := range strings.Split(NormalizeTag(v), ".") {
		n := 0
		if _, err := fmt.Sscanf(seg, "%d", &n); err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}

// FetchLatestRelease 请求 GitHub 获取最新 release 信息。
//
// client 可注入（测试用 httptest 服务器）；nil 时使用 http.DefaultClient。
// 响应超时 8s（对齐 Python timeout=8.0）。
func FetchLatestRelease(ctx context.Context, apiURL string, client *http.Client) (ReleaseInfo, error) {
	if client == nil {
		client = http.DefaultClient
	}
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, apiURL, nil)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("网络请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return ReleaseInfo{}, ErrNoRelease
	case http.StatusForbidden:
		return ReleaseInfo{}, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return ReleaseInfo{}, fmt.Errorf("GitHub 返回异常状态码 %d", resp.StatusCode)
	}

	var out struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ReleaseInfo{}, fmt.Errorf("GitHub 返回内容不是 JSON: %w", err)
	}

	tag := NormalizeTag(out.TagName)
	info := ReleaseInfo{
		Tag:  tag,
		Name: out.Name,
		URL:  out.HTMLURL,
	}
	if info.Name == "" {
		info.Name = out.TagName
	}
	if info.Name == "" {
		info.Name = tag
	}
	if info.URL == "" {
		info.URL = ReleasesPage
	}
	return info, nil
}
