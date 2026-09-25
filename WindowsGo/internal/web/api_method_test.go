package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAPIRejectsNonPost 钉死 /api/* 只接受 POST。
//
// 回归的是 CSRF 面：状态变更端点若接受 GET，就绕过了 originAllowed 的跨源
// 校验（只覆盖非 GET 方法），而跨源 GET 是「简单请求」——不带 Origin、不需
// 预检、响应读不回来也不妨碍副作用。于是本机浏览器里打开的任意网页都能盲打
// /api/vault/lock、/api/app/quit、/api/settings/purgecache…（回环来源还
// 本来就免令牌）。前端 lib/api.ts 的 call() 全部走 POST，故直接收紧。
func TestAPIRejectsNonPost(t *testing.T) {
	s := &Server{}
	reached := false
	ok := func() (any, error) { reached = true; return nil, nil }

	wrappers := map[string]http.HandlerFunc{
		"wrapErr": s.wrapErr(func(*http.Request) (any, error) { return ok() }),
		"wrapJSON": s.wrapJSON(func(*http.Request, []byte) (any, error) { return ok() }),
	}

	for name, h := range wrappers {
		t.Run(name+"-拒绝GET", func(t *testing.T) {
			reached = false
			w := httptest.NewRecorder()
			h(w, httptest.NewRequest(http.MethodGet, "/api/vault/lock", nil))
			if w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("GET 应 405，实得 %d", w.Code)
			}
			if reached {
				t.Error("GET 不应触达业务处理函数")
			}
			if allow := w.Header().Get("Allow"); allow != "POST" {
				t.Errorf("Allow 头应为 POST，实得 %q", allow)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("/api 路径必须回 JSON（前端只认 {code,message}），实得 %q", ct)
			}
			if !strings.Contains(w.Body.String(), "code") {
				t.Errorf("错误体应带 code 字段，实得 %s", w.Body.String())
			}
		})

		t.Run(name+"-放行POST", func(t *testing.T) {
			reached = false
			w := httptest.NewRecorder()
			h(w, httptest.NewRequest(http.MethodPost, "/api/vault/lock", nil))
			if !reached {
				t.Fatal("POST 应触达业务处理函数")
			}
			if w.Code != http.StatusOK {
				t.Fatalf("handler 返回 nil,nil ⇒ 应 200，实得 %d", w.Code)
			}
		})
	}
}

// TestWrapJSONRejectsOversizedBody 钉死 JSON 端点的请求体上限：
// 之前是无上限的 io.ReadAll，任何能访问本服务的客户端持续发数据就能把
// 进程内存吃光。
func TestWrapJSONRejectsOversizedBody(t *testing.T) {
	s := &Server{}
	reached := false
	h := s.wrapJSON(func(*http.Request, []byte) (any, error) {
		reached = true
		return nil, nil
	})

	body := strings.NewReader(strings.Repeat("x", maxJSONBodyBytes+1))
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/api/vault/open", body))

	if reached {
		t.Fatal("超限请求体不应触达业务处理函数")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("超限请求体应 400，实得 %d", w.Code)
	}
}
