package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// tokenURL 授权码换取 token 的接口地址（对照 gui/baidu_auth.py TOKEN_URL）。
const tokenURL = "https://openapi.baidu.com/oauth/2.0/token"

// BaiduAuthURL 构造浏览器授权页地址（oob 模式：无回调，页面直接展示 code）。
// 对照 gui/baidu_auth.py:35-48（AUTHORIZE_URL/build_auth_url）。
//
// 差异说明：Python 用 str.format 直接插值（不转义），app_key 若含 & 等
// 保留字符会破坏 URL 语义；Go 端对两个参数做 QueryEscape 是加固，
// 正常凭证（字母数字）下两端产出的 URL 逐字节相同。
func BaiduAuthURL(appKey, appID string) string {
	return "https://openapi.baidu.com/oauth/2.0/authorize" +
		"?response_type=code" +
		"&client_id=" + url.QueryEscape(appKey) +
		"&redirect_uri=oob" +
		"&scope=basic,netdisk" +
		"&device_id=" + url.QueryEscape(appID)
}

// ExchangeBaiduToken 用 oob 授权码换取 access/refresh token（对照
// gui/baidu_auth.py exchange_code：GET + 查询参数，响应缺 access_token 即失败）。
// 返回数据不含 app_id/sign_key（授权提交方手头有），由调用方补全后落盘；
// ExpiresAt 为 Unix 秒（对齐 Python time.time()+expires_in 的口径）。
func ExchangeBaiduToken(ctx context.Context, appKey, secretKey, code string) (BaiduCredData, error) {
	q := url.Values{}
	q.Set("grant_type", "authorization_code")
	q.Set("code", code)
	q.Set("client_id", appKey)
	q.Set("client_secret", secretKey)
	q.Set("redirect_uri", "oob")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL+"?"+q.Encode(), nil)
	if err != nil {
		return BaiduCredData{}, fmt.Errorf("构造授权请求失败: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return BaiduCredData{}, fmt.Errorf("授权请求失败（网络/凭证无效）: %w", err)
	}
	defer resp.Body.Close()

	var raw struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    float64 `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return BaiduCredData{}, fmt.Errorf("授权响应解析失败: %w", err)
	}
	// 成功响应必有 access_token；缺失按 Python 语义整体判定为授权失败
	if raw.AccessToken == "" {
		return BaiduCredData{}, fmt.Errorf("授权失败（code 可能已过期或凭证不正确）")
	}
	return BaiduCredData{
		AppKey:       appKey,
		SecretKey:    secretKey,
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresAt:    float64(time.Now().Unix()) + raw.ExpiresIn,
	}, nil
}
