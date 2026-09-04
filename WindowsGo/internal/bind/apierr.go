// Package bind 是 Wails 绑定层：5 个域的薄适配 struct（Vault / Files /
// Transfer / Settings / Preview）把 internal/appstate 的错误映射为带 Code
// 的 ApiError 后抛出，**不写任何业务逻辑**。分层约束见 docs/ARCHITECTURE.md。
package bind

import (
	"context"
	"errors"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/streaming"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/vault"
)

// 错误码取值：前端 src/lib/api.ts 按 code 分支（幂等重试/文案分级），
// message 为可直接展示的中文（与 Python 端错误文案逐字对齐）。
const (
	CodeLocked       = "locked"         // 需先连接密库
	CodeBusy         = "busy"           // 上一轮独占操作未完成
	CodeNoResume     = "no-resume"      // 没有可续传任务
	CodeSyncDirUnset = "sync-dir-unset" // 未设置本地同步目录
	CodeBadPassword  = "bad-password"   // 主密码/恢复码身份校验失败
	CodeNoVault      = "no-vault"       // 目标位置不存在密库
	CodeVaultExists  = "vault-exists"   // 目标位置已有密库
	CodeBadRecovery  = "bad-recovery"   // 恢复码无效
	CodeNotFound     = "not-found"      // 远端路径不存在
	CodeNotDir       = "not-dir"        // 目标是文件而非目录
	CodeIsDir        = "is-dir"         // 目标是目录而非文件
	CodeRange        = "range"          // 非法字节范围（内部错误）
	CodeNotVaultFile = "not-vault-file" // 不是合法的加密容器
	CodeBackend      = "backend"        // 后端故障（可重试）
	CodeTimeout      = "timeout"        // 网络操作超时
	CodeInternal     = "internal"       // 其余未分类错误
)

// ApiError 是前端可见的结构化错误（code 分支 + message 展示）。
type ApiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error 实现 error 接口。Wails 只把 err.Error() 字符串带给前端，
// 故 code 以 [code] 前缀编码进消息，api.ts 解包还原结构化字段。
func (e *ApiError) Error() string { return "[" + e.Code + "] " + e.Message }

// Wrap 把 core 层错误分类映射为 ApiError；nil 与已包装错误原样返回。
// 哨兵按最具体优先排列：storage.ErrNotFound 会同时被 vault/streaming
// 的包装错误裹住，errors.Is 在 unwrap 链上都能命中，顺序无歧义。
func Wrap(err error) error {
	if err == nil {
		return nil
	}
	var ae *ApiError
	if errors.As(err, &ae) {
		return err
	}
	code := CodeInternal
	switch {
	case errors.Is(err, appstate.ErrLocked):
		code = CodeLocked
	case errors.Is(err, appstate.ErrBusy):
		code = CodeBusy
	case errors.Is(err, appstate.ErrNoResume):
		code = CodeNoResume
	case errors.Is(err, appstate.ErrSyncDirUnset):
		code = CodeSyncDirUnset
	case errors.Is(err, vault.ErrBadPassword):
		code = CodeBadPassword
	case errors.Is(err, vault.ErrBadRecovery):
		code = CodeBadRecovery
	case errors.Is(err, vault.ErrNoVault):
		code = CodeNoVault
	case errors.Is(err, vault.ErrVaultExists):
		code = CodeVaultExists
	case errors.Is(err, storage.ErrNotFound):
		code = CodeNotFound
	case errors.Is(err, storage.ErrNotDir):
		code = CodeNotDir
	case errors.Is(err, storage.ErrIsDir):
		code = CodeIsDir
	case errors.Is(err, storage.ErrRange):
		code = CodeRange
	case errors.Is(err, streaming.ErrNotVaultFile):
		code = CodeNotVaultFile
	case errors.Is(err, storage.ErrBackend):
		code = CodeBackend
	case errors.Is(err, context.DeadlineExceeded):
		code = CodeTimeout
	}
	return &ApiError{Code: code, Message: err.Error()}
}
