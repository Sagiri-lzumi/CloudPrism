package storage

import "net/url"

// baiduAuthURL 构造浏览器授权页地址（oob 模式：无回调，页面直接展示 code）。
// 对照 gui/baidu_auth.py:35-48（AUTHORIZE_URL/build_auth_url）。
//
// 差异说明：Python 用 str.format 直接插值（不转义），app_key 若含 & 等
// 保留字符会破坏 URL 语义；Go 端对两个参数做 QueryEscape 是加固，
// 正常凭证（字母数字）下两端产出的 URL 逐字节相同。
func baiduAuthURL(appKey, appID string) string {
	return "https://openapi.baidu.com/oauth/2.0/authorize" +
		"?response_type=code" +
		"&client_id=" + url.QueryEscape(appKey) +
		"&redirect_uri=oob" +
		"&scope=basic,netdisk" +
		"&device_id=" + url.QueryEscape(appID)
}
