// appstate.ts —— 前端自有 TS 模型（v33 起替代 Wails 生成的 wailsjs/go/models.ts）。
//
// 字段镜像 Go 侧 internal/appstate/state.go 的 JSON 标签；新增/改名字段
// 必须两侧同步。纯 interface（无 createFrom 构造器：数据来自 fetch/SSE
// JSON，直接类型断言即可）。
//
// 命名空间 appstate 与 bind 保留：与旧 wailsjs 引用形态一致，迁移时仅换
// import 路径。

export namespace appstate {
  export interface FileEntry {
    name: string
    display: string
    isDir: boolean
    size: number
    remote: string
  }

  export interface OpenRequest {
    kind: string
    localDir: string
    url: string
    user: string
    pass: string
    masterPassword: string
    recoveryCode: string
    filenameEnc: boolean
    vaultName: string
    vaultPath: string
    create: boolean
  }

  export interface OpenVaultResult {
    code: string
  }

  export interface SyncStatus {
    running: boolean
    total: number
    done: number
    current: string
    synced: number
    failed: number
    errors: string[]
  }

  export interface Snapshot {
    connected: boolean
    vaultName: string
    vaultPath: string
    backend: string
    backendId: string
    filenameEnc: boolean
    hasRecovery: boolean
    connectedSec: number
    resumeCount: number
    autoLockMin: number
    transferActive: boolean
    transferDone: number
    transferTotal: number
    transferTasks: number
    sync: SyncStatus
    statsDone: boolean
    statsTotal: number
    statsFiles: number
    statsFailed: boolean
  }

  export interface TaskView {
    id: number
    name: string
    direction: string
    state: string
    progress: number
    totalBytes: number
    doneBytes: number
    errorMsg: string
    remote: string
    remoteDir: string
    local: string
  }
}

export namespace bind {
  export interface BaiduAuthInfo {
    authorized: boolean
    appKey: string
    appId: string
  }
}
