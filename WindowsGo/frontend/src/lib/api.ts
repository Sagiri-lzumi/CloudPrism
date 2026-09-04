// api.ts —— 前端唯一封装层。
//
// 职责（见 Plan 阶段 6 文件树）：
//  1. re-export wailsjs 生成的 5 域绑定（视图层一律从这里 import，
//     不直接碰 frontend/wailsjs/go 下的路径，便于日后替换传输层）；
//  2. 统一解包 Go 侧 ApiError —— 序列化过 IPC 后只剩 "[code] message"
//     字符串，这里还原为带 code 的 ApiError 供 UI 按类别分流。
//
// Go 侧错误约定见 internal/bind/apierr.go：Code 常量 17 个、Error()
// 输出 "[code] message"。code 全集与本文件 ApiCode 镜像。

import * as App from '../../wailsjs/go/main/App'
import * as Files from '../../wailsjs/go/bind/Files'
import * as Preview from '../../wailsjs/go/bind/Preview'
import * as Settings from '../../wailsjs/go/bind/Settings'
import * as Transfer from '../../wailsjs/go/bind/Transfer'
import * as Vault from '../../wailsjs/go/bind/Vault'

export {App, Files, Preview, Settings, Transfer, Vault}

/** Go 侧 ApiError code 全集（镜像 internal/bind/apierr.go）。 */
export const ApiCode = {
  Locked: 'locked', // 未连接/已锁库
  Busy: 'busy', // 已有操作进行中
  NoResume: 'no-resume', // 无续传任务
  SyncDirUnset: 'sync-dir-unset', // 未设置同步目录
  BadPassword: 'bad-password', // 主密码错误
  NoVault: 'no-vault', // 指定位置不是密库
  VaultExists: 'vault-exists', // 已存在密库
  BadRecovery: 'bad-recovery', // 恢复码错误
  NotFound: 'not-found', // 远程路径不存在
  NotDir: 'not-dir', // 路径不是目录
  IsDir: 'is-dir', // 路径是目录（操作要求文件）
  Range: 'range', // Range 越界
  NotVaultFile: 'not-vault-file', // 非密库内文件
  Backend: 'backend', // 存储后端错误
  Timeout: 'timeout', // 超时
  Internal: 'internal', // 兜底未知错误
} as const

export type ApiCodeValue = (typeof ApiCode)[keyof typeof ApiCode]

/** ApiError：保留 Go 侧分类 code，UI 可按 code 决定文案与恢复动作。 */
export class ApiError extends Error {
  constructor(
    public readonly code: ApiCodeValue | string,
    message: string,
  ) {
    super(`[${code}] ${message}`)
    this.name = 'ApiError'
  }
}

// Go 侧 Error() 序列化格式；message 本身可能含换行（错误栈），故用 s 旗标。
const CODE_RE = /^\[([a-z][a-z-]*)\]\s([\s\S]*)$/

/**
 * 把 Wails 绑定 reject 出来的值还原为 ApiError。
 * - 匹配 "[code] message" 字符串 → 拆 code/message；
 * - 已是 ApiError / 带 code 字段对象 → 原样归类；
 * - 其余（网络层、JSON 反序列化等）兜底 internal。
 */
export function unwrap(err: unknown): ApiError {
  if (err instanceof ApiError) return err
  if (typeof err === 'string') {
    const m = CODE_RE.exec(err)
    if (m) return new ApiError(m[1], m[2])
    return new ApiError(ApiCode.Internal, err)
  }
  if (err && typeof err === 'object') {
    const o = err as {code?: unknown; message?: unknown}
    if (typeof o.code === 'string' && CODE_RE.test(o.code)) return unwrap(o.code)
    if (typeof o.message === 'string') return unwrap(o.message)
  }
  return new ApiError(ApiCode.Internal, err instanceof Error ? err.message : String(err))
}

/** 分类错误快速判断（UI 对锁定/同步目录未设等状态做定向处理）。 */
export function isApiCode(err: unknown, code: ApiCodeValue | string): boolean {
  return unwrap(err).code === code
}
