package storage

import (
	"errors"
	"fmt"
	"strings"
)

// 存储后端构造工厂与描述工具（非 GUI）。
//
// 对照 WindowsPy/src/cloudprism/core/backend_factory.py。按类型键与参数
// 构造后端实例，避免初始化向导与密库连接处各自维护构造逻辑。
// 主密码/密码参数由调用方即时传入，绝不落盘。

// Params 是一次后端连接的完整参数（按 Kind 取用对应字段）。
type Params struct {
	Kind string // "local" / "webdav" / "baidu"

	LocalDir string // local

	WebDAVURL  string // webdav
	WebDAVUser string
	WebDAVPass string

	BaiduStore *BaiduCredStore // baidu：凭证已加密落盘，授权状态由 Load 判定
}

// Build 按参数构造后端实例：
//
//   - local  : 本地文件夹（目录须存在，NewLocal 负责校验规范化）；
//   - webdav : WebDAV（url 合法性 + 账号密码，NewWebDAV 负责校验）；
//   - baidu  : 读取凭证存储，未完成授权时返回 ErrBackend
//     （对齐 Python 端 ConnectionError 语义——授权缺失属于「后端连不上」，
//     上层据此进入授权向导而不是把错误当文件系统故障展示）。
//
// 未知类型键返回普通错误（参数错误，不属于任何后端故障类别）。
func Build(p Params) (Backend, error) {
	switch p.Kind {
	case "local":
		if p.LocalDir == "" {
			return nil, fmt.Errorf("本地后端缺少根目录参数（local_dir）")
		}
		return NewLocal(p.LocalDir)
	case "webdav":
		return NewWebDAV(p.WebDAVURL, p.WebDAVUser, p.WebDAVPass)
	case "baidu":
		store := p.BaiduStore
		if store == nil {
			store = NewBaiduCredStore("", nil) // path 为空时 Load 恒 nil（未授权）
		}
		d := store.Load()
		if d == nil || d.AccessToken == "" {
			return nil, fmt.Errorf(
				"%w: 尚未完成百度网盘授权，请先在向导中完成授权，或按《Plan/百度网盘开放平台申请指南》申请凭证",
				ErrBackend,
			)
		}
		return NewBaidu(*d, store), nil
	}
	return nil, fmt.Errorf("未知后端类型：%s", p.Kind)
}

// Describe 返回后端的 (显示名, 路径/地址, 类型键)，供设置页/状态栏展示。
// 对照 backend_factory.py:57-65（describe_backend）。
func Describe(b Backend) (name, addr, kind string) {
	switch v := b.(type) {
	case *Local:
		return "本地文件夹", v.Root(), "local"
	case *WebDAV:
		return "WebDAV", v.baseURL, "webdav"
	case *Baidu:
		return "百度网盘", "百度网盘（开放平台授权）", "baidu"
	}
	return fmt.Sprintf("%T", b), "-", "unknown"
}

// IsBaiduUnauthorized 供上层区分「授权缺失」与其它后端错误
// （初始化向导据此决定跳转授权页而非报错）。
func IsBaiduUnauthorized(err error) bool {
	return err != nil && errors.Is(err, ErrBackend) &&
		strings.Contains(err.Error(), "尚未完成百度网盘授权")
}
