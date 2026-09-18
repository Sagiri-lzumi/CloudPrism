// api.ts —— 前端唯一封装层（v32 起 Web 服务模式）。
//
// 职责：
//  1. re-export 5 域的 fetch 调用（视图层一律从这里 import，不直接碰 fetch）；
//  2. 统一解包后端 ApiError —— HTTP 错误响应带 {code, message}，还原为
//     带 code 的 ApiError 供 UI 按类别分流。
//
// Go 侧错误约定见 internal/bind/apierr.go：Code 常量 + {code,message} JSON。

import type {appstate} from '../types/appstate'

/** ApiCode 全集（镜像 internal/bind/apierr.go）。 */
export const ApiCode = {
  Locked: 'locked',
  Busy: 'busy',
  NoResume: 'no-resume',
  SyncDirUnset: 'sync-dir-unset',
  BadPassword: 'bad-password',
  NoVault: 'no-vault',
  VaultExists: 'vault-exists',
  BadRecovery: 'bad-recovery',
  NotFound: 'not-found',
  NotDir: 'not-dir',
  IsDir: 'is-dir',
  Range: 'range',
  NotVaultFile: 'not-vault-file',
  Backend: 'backend',
  Timeout: 'timeout',
  Internal: 'internal',
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

/** 解包 HTTP 错误响应为 ApiError。 */
export function unwrap(err: unknown): ApiError {
  if (err instanceof ApiError) return err
  if (err && typeof err === 'object') {
    const o = err as {code?: unknown; message?: unknown}
    if (typeof o.code === 'string' && typeof o.message === 'string') {
      return new ApiError(o.code, o.message)
    }
  }
  if (err instanceof Error) return new ApiError(ApiCode.Internal, err.message)
  return new ApiError(ApiCode.Internal, String(err))
}

/** 分类错误快速判断。 */
export function isApiCode(err: unknown, code: ApiCodeValue | string): boolean {
  return unwrap(err).code === code
}

/* ------------------------------------------------------------- HTTP 层 */

const BASE = '/api'

async function call<T>(path: string, body?: unknown): Promise<T> {
  const resp = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!resp.ok) {
    let err: unknown
    try {
      err = await resp.json()
    } catch {
      err = {code: ApiCode.Internal, message: `HTTP ${resp.status}`}
    }
    throw unwrap(err)
  }
  const text = await resp.text()
  if (!text) return undefined as T
  try {
    return JSON.parse(text) as T
  } catch {
    return text as unknown as T
  }
}

/* ------------------------------------------------------------- 5 域绑定 */

/** Vault 域：密库连接/锁定/状态/最近记录/恢复码/百度凭证。 */
export const Vault = {
  State: () => call<appstate.Snapshot>('/vault/state'),
  Open: (req: appstate.OpenRequest) => call<appstate.OpenVaultResult>('/vault/open', req),
  Lock: () => call<void>('/vault/lock'),
  RecentVaults: () => call<Record<string, unknown>[]>('/vault/recents'),
  ForgetRecent: (key: string) => call<void>('/vault/forget', {key}),
  ListOtherVaults: () => call<string[]>('/vault/listother'),
  ConnectOtherVault: (vaultPath: string, masterPassword: string) =>
    call<void>('/vault/connectother', {vaultPath, masterPassword}),
  RenameVault: (newName: string) => call<void>('/vault/renamevault', {newName}),
  RegenerateRecoveryCode: (masterPassword: string) =>
    call<string>('/vault/regenrecovery', {masterPassword}),
  ResumePending: () => call<number>('/vault/resume'),
  RequestStats: () => call<void>('/vault/requeststats'),
  BaiduStatus: () => call<{authorized: boolean; appKey: string; appId: string}>('/vault/baidustatus'),
  BaiduAuthURL: (appID: string, appKey: string) =>
    call<string>('/vault/baiduauthurl', {appID, appKey}),
  BaiduSaveAuth: (appID: string, appKey: string, secretKey: string, signKey: string, code: string) =>
    call<void>('/vault/baidusaveauth', {appID, appKey, secretKey, signKey, code}),
  BaiduClearAuth: () => call<void>('/vault/baiduclearauth'),
  ChooseLocalDir: () => call<string>('/vault/chooselocaldir'),
}

/** Files 域：目录列表/新建/重命名/删除/导出。 */
export const Files = {
  List: (remote: string) => call<appstate.FileEntry[]>('/files/list', {remote}),
  NewFolder: (parent: string, name: string) => call<void>('/files/newfolder', {parent, name}),
  Rename: (remote: string, name: string) => call<void>('/files/rename', {remote, name}),
  Delete: (remotes: string[]) => call<void>('/files/delete', {remotes}),
  Export: (remote: string) => call<string>('/files/export', {remote}),
}

/** CacheInfo：缓存目录与占用（镜像 appstate.CacheInfo）。 */
export interface CacheInfo {
  dir: string // 实际生效的缓存根目录
  scope: string // 当前密库的作用域目录（未连接为空）
  chunkMb: number // 生效的分块大小（MB）
  limitMb: number // 容量上限（MB）
  bytes: number // 已占用字节
  entries: number // 条目数
  enabled: boolean // 分块缓存是否可用
}

/** Settings 域：偏好读写（Web 模式下目录选择用 stub，后续阶段补）。 */
export const Settings = {
  Get: () => call<Record<string, unknown>>('/settings/get'),
  SetTheme: (index: number) => call<void>('/settings/settheme', {index}),
  SetFontSize: (px: number) => call<void>('/settings/setfontsize', {px}),
  SetCache: (limitMB: number, path: string) => call<void>('/settings/setcache', {limitMB, path}),
  // 分块大小 = 分块阈值（MB）：大于等于该值的文件按块缓存
  SetChunkSize: (mb: number) => call<void>('/settings/setchunksize', {mb}),
  CacheInfo: () => call<CacheInfo>('/settings/cacheinfo'),
  PurgeCache: () => call<CacheInfo>('/settings/purgecache'),
  SetTransfer: (chunkIndex: number, concurrent: number) =>
    call<void>('/settings/settransfer', {chunkIndex, concurrent}),
  SetAutoLock: (index: number) => call<void>('/settings/setautolock', {index}),
  SetMaxCores: (n: number) => call<void>('/settings/setmaxcores', {n}),
  SetSyncDir: (dir: string) => call<void>('/settings/setsyncdir', {dir}),
  SyncNow: () => call<number>('/settings/syncnow'),
  ChooseSyncDir: () => call<string>('/settings/choosesyncdir'),
  ChooseCacheDir: () => call<string>('/settings/choosecachedir'),
}

/** Transfer 域：上传/下载/任务管理。 */
export const Transfer = {
  // 浏览器 multipart 流上传：files 为浏览器 File 对象（input/drag-drop），
  // 后端 staging 成临时文件后入传输队列。
  Upload: async (files: File[], remoteDir: string): Promise<void> => {
    const fd = new FormData()
    for (const f of files) fd.append('files', f)
    fd.append('remoteDir', remoteDir)
    const resp = await fetch('/api/transfer/upload', {method: 'POST', body: fd})
    if (!resp.ok) {
      let err: unknown
      try {
        err = await resp.json()
      } catch {
        err = {code: ApiCode.Internal, message: `HTTP ${resp.status}`}
      }
      throw unwrap(err)
    }
  },
  // 下载端点 URL：浏览器 <a download> 触发保存（后端 /d/ 流式解密）。
  DownloadURL: (remote: string, display: string) =>
    call<{url: string}>('/transfer/downloadurl', {remote, display}),
  Tasks: () => call<appstate.TaskView[]>('/transfer/tasks'),
  Retry: (id: number) => call<void>('/transfer/retry', {id}),
  CancelAll: () => call<void>('/transfer/cancelall'),
  ClearFinished: () => call<void>('/transfer/clearfinished'),
}

/** Preview 域：媒体/缩略图代理 URL（走 /s/ /t/ 路径，非 JSON API）。 */
export const Preview = {
  MediaURL: (remote: string, display: string) =>
    call<string>('/preview/mediaurl', {remote, display}),
  ThumbURL: (remote: string) => call<string>('/preview/thumburl', {remote}),
  Revoke: (token: string) => call<void>('/preview/revoke', {token}),
}

/** LanAddr：一个可用于局域网访问的地址及其来源网卡（镜像 bind.LanAddr）。 */
export interface LanAddr {
  ip: string // IPv4 地址
  iface: string // 网卡名称（如「以太网」「WLAN」）
  url: string // 带访问令牌的完整地址（复制即用）
  virtual: boolean // 虚拟网卡/隧道上的地址：手机通常连不上，UI 需标注
}

/** LanStatus：局域网访问档状态（镜像 bind.Lan.Status 返回的 map）。 */
export interface LanStatus {
  enabled: boolean // 设置里已保存的开关
  active: boolean // 当前进程是否真的对局域网监听（切换开关需重启才生效）
  port: number // 实际监听端口（0 = 尚未监听）
  token: string // 访问令牌（拼接分享链接用；能拿到即已通过闸门）
  localUrl: string // 本机地址
  addrs: LanAddr[] // 局域网可分享地址（真实网卡在前、虚拟网卡在后）
}

/** Lan 域：局域网访问档（开关 / 分享地址 / 访问令牌）。 */
export const Lan = {
  Status: () => call<LanStatus>('/lan/status'),
  // 仅写设置、不热重载监听：开启后需重启程序才会真正绑到局域网，
  // 因此 UI 必须同时显示 enabled（已保存）与 active（当前生效）。
  SetEnabled: (on: boolean) => call<void>('/lan/setenabled', {on}),
  // 重新生成令牌：旧令牌立即失效，所有已授权远端设备需用新链接重新进入。
  RotateToken: () => call<{token: string}>('/lan/rotatetoken'),
}

/** App 域：全局操作。 */
export const App = {
  Quit: () => call<void>('/app/quit'),
  Ping: (token: string) => call<string>('/app/ping', {token}),
  Version: () => call<string>('/app/version'),
}

export type {appstate}
