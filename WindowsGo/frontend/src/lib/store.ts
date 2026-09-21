// store.ts —— 前端唯一响应式全局状态（v32 起 Web 服务模式）。
//
// 数据流：SSE /api/events（st:frame 10Hz 合帧等）→ 快照/任务写入 ui；
// 视图层动作（列目录/上传/下载/建夹/改名/删除…）经 fetch /api/* 调用，
// 错误统一 unwrap 成 ApiError，按 code 分流提示。
//
// op（长操作忙碌态）约定：后端只发 st:op-progress，没有终态事件；
// busy 复位由每个 op 调用方在 finally 里调 endOp()，另有 onFrame 兜底
// （快照 connected false→true 瞬间自动清）防事件缺失卡死。
//
// 模块单例：App.vue onMounted 调 start()，onBeforeUnmount 调 stop()。

import {reactive} from 'vue'
import type {appstate} from '../types/appstate'
import {ApiCode, App, Files, Preview, Settings, Transfer, Vault, unwrap} from './api'
import * as evt from './events'
import {applyThemeIndex} from './theme'
import {showError, showInfo, showSuccess} from './toast'
import {collectDropped, collectFromFileList, type UploadItem} from './upload'

export type PageId = 'files' | 'transfers' | 'vaults' | 'settings'

/** 文件浏览模式：list = 目录树（懒加载层级），grid = 当前目录缩略图网格。 */
export type ViewMode = 'list' | 'grid'

/** 明文导航链的一段（面包屑用）：label 为解密展示名，remote 为回传密文路径。
 *  后端 FileEntry.remote 是加密名拼接路径，不可直接当展示文本。 */
export interface Crumb {
  label: string
  remote: string
}

/** 上传占位（乐观条目）。为什么需要它：浏览器里的 File 在服务端列表里**还没有**
 *  对应条目（要等加密上传完成才会出现），而用户要求「上传的时候文件就在列表里」，
 *  所以由前端先占位，任务到达终态（done/failed/cancelled）即撤下 —— 对账在
 *  onFrame 的 reconcileUploads 里做，不靠定时器。
 *
 *  · name 与任务侧 `TaskView.name`（= 本地文件 basename）同源，故用 name+dir 匹配；
 *  · 只给「直接落在当前目录」的文件占位（rel 里带 '/' 的会进子目录，在当前列表里
 *    显示反而是错的位置）；
 *  · progress = null 表示还没匹配到任务（本地暂存阶段，进度未知）→ 画不定态圆环。 */
export interface UploadPlaceholder {
  key: string
  name: string
  size: number
  /** 目标目录 remote（与 ui.remote 同语义：根 = 空串） */
  dir: string
  /** 创建时刻，仅作安全阀（超时未匹配且未终结则撤下） */
  at: number
  /** 已匹配到的任务 id（0 = 未匹配） */
  taskId: number
  /** 任务进度 0~100；null = 未知（未匹配） */
  progress: number | null
  /** 任务状态；'' = 未匹配 */
  state: string
}

/** 圆环进度的显示阈值：只给「够大、传得够久」的文件画圈（用户 2026-09-21 要求
 *  「一般可以不用这样显示」）。小文件瞬间完成，画圈只会闪一下反而干扰。 */
export const UPLOAD_RING_MIN = 8 * 1024 * 1024

/** 占位安全阀：单文件上传不该超过 30 分钟；超时未终结即撤下，避免永久幽灵条目。 */
const UPLOAD_PLACEHOLDER_TTL = 30 * 60_000

interface Ui {
  // —— 连接与会话（st:frame 驱动）——
  snap: appstate.Snapshot | null
  /** 活动传输任务明细（仅帧内 transferActive 时更新） */
  tasks: appstate.TaskView[]
  /** 上传占位（乐观条目）：文件页在当前目录里把它们渲染成「正在上传」条目 */
  uploads: UploadPlaceholder[]
  /** 长操作（向导/恢复码）阶段文案；空串=无操作 */
  opText: string
  opBusy: boolean
  /** 新建成功待展示的一次性恢复码（空串=无）；App 全局模态展示，关闭时 clearRecovery() */
  pendingRecovery: string

  // —— 页面导航 ——
  page: PageId
  viewMode: ViewMode

  // —— 文件浏览（当前目录 = remote 指向的目录）——
  remote: string // '' = 密库根；路径分隔 '/'（后端统一格式）
  /** 当前目录条目；null = 未加载/加载中 */
  entries: appstate.FileEntry[] | null
  loading: boolean
  loadError: string
  /** 代际：目录切换后旧请求结果丢弃（对照 side_panel.py _grid_seq） */
  seq: number
  /** 选中条目（文件或目录；remote path 唯一键）——最后点击的主条目，预览/单条操作使用 */
  sel: appstate.FileEntry | null
  /** 多选集合（remote path 唯一键；长度 >1 时工具栏/右键呈批量操作）。sel 恒为该集合最后一个 */
  multi: appstate.FileEntry[]
  /** 明文导航链（面包屑）：每次受管进入目录时 push，返回/跳转时截断 */
  crumbs: Crumb[]

  // —— 设置缓存（启动 Get 一次，设置页读写同一份）——
  settings: Record<string, unknown>
}

export const ui = reactive<Ui>({
  snap: null,
  tasks: [],
  uploads: [],
  opText: '',
  opBusy: false,
  pendingRecovery: '',
  page: 'vaults', // 启动默认密库页（未连接引导）
  viewMode: 'grid',
  remote: '',
  entries: null,
  loading: false,
  loadError: '',
  seq: 0,
  sel: null,
  multi: [],
  crumbs: [],
  settings: {},
})

/* ---------------------------------------------------------------- 事件流 */

let started = false

// 空闲轮询定时器：文件页 + 已连接 + 无传输进行中 + 距上次手动刷新 ≥ 1s → 静默刷一次
let pollTimer: ReturnType<typeof setInterval> | null = null
const POLL_MS = 5000

function pollTick() {
  if (!ui.snap?.connected) return
  if (ui.page !== 'files') return
  if (ui.snap.transferActive) return
  if (ui.loading) return
  if (Date.now() - lastManualReloadAt < 1000) return
  refreshSilent()
}

// 上一帧传输态/任务清单：onFrame 用以检测「活动→静止」边界与「任务刚终结」边界
let wasTransferActive = false
let wasTasks: appstate.TaskView[] = []
let lastManualReloadAt = 0

/** 终态：与 pkg/transfer 的 State* 常量一致（完成任务即撤占位、即刷新列表） */
const TERMINAL_STATES = new Set(['done', 'failed', 'cancelled'])

// 帧事件处理器固定引用（EventsOff 需要同一引用）
function onFrame(payload: unknown) {
  const f = payload as {snap?: appstate.Snapshot; tasks?: appstate.TaskView[]}
  if (!f || !f.snap) return
  const wasConnected = !!ui.snap?.connected
  ui.snap = f.snap
  ui.tasks = f.tasks ?? []
  const tasks = f.tasks ?? []
  // 快照权威：连接建立瞬间自动收尾 op 忙碌态（防终态事件缺失卡死）
  if (!wasConnected && f.snap.connected && ui.opBusy) endOp()
  // 锁库/连接态变化时清理遗留的浏览态
  if (!f.snap.connected) {
    ui.uploads = [] // 断开后任务没了对账依据，占位一并清掉
    if (ui.remote !== '') resetBrowse()
  }

  // —— 上传完成就刷新（用户 2026-09-21：不要等 5s 空闲轮询）——
  // 按「任务在本帧刚进入终态」判定（与上一帧同 id 的状态比较），而不是等整队列
  // 从 active 翻到 idle —— 批量上传时后者要等最后一个文件，先传完的会被压住。
  const prevState = new Map(wasTasks.map((t) => [t.id, t.state]))
  const finishedNames: string[] = []
  for (const t of tasks) {
    if (t.direction !== 'upload' || !TERMINAL_STATES.has(t.state)) continue
    if (prevState.get(t.id) === t.state) continue // 早已终结，不是「刚完成」
    if (t.remoteDir !== ui.remote) continue // 传的不是当前目录，列表无需刷新
    if (t.state === 'done') finishedNames.push(t.name)
  }
  if (finishedNames.length) scheduleRefreshAfterUpload(finishedNames)
  reconcileUploads(tasks)

  // 兜底：整队列 active→idle 且确实有传向当前目录的上传任务 → 再刷一次。
  // 主路径已被上面的「刚终结」覆盖，这条防的是「终结帧与任务明细恰好错帧」。
  if (
    wasTransferActive &&
    !f.snap.transferActive &&
    f.snap.connected &&
    ui.page === 'files'
  ) {
    const wantRemote = ui.remote
    const hit = wasTasks.some(
      (t) => t.direction === 'upload' && t.remoteDir === wantRemote,
    )
    if (hit) refreshSilent()
  }
  wasTransferActive = f.snap.transferActive
  wasTasks = tasks
}

/* ------------------------------------------------ 上传占位与完成即刷新 */

/** 占位与任务对账：匹配到任务就带上真实进度；任务终结即撤下（done 时顺手刷新）。 */
function reconcileUploads(tasks: appstate.TaskView[]) {
  if (!ui.uploads.length) return
  const kept: UploadPlaceholder[] = []
  let changed = false
  for (const p of ui.uploads) {
    // 同名同目录即视为同一笔（同名重复上传时会一起收敛，可接受）
    const t = tasks.find(
      (x) => x.direction === 'upload' && x.remoteDir === p.dir && x.name === p.name,
    )
    if (t) {
      if (p.taskId !== t.id || p.progress !== t.progress || p.state !== t.state) {
        p.taskId = t.id
        p.progress = t.progress
        p.state = t.state
      }
      if (TERMINAL_STATES.has(t.state)) {
        changed = true // 任务已终结 → 撤下占位（真实条目由刷新补上）
        continue
      }
      kept.push(p)
      continue
    }
    // 未匹配到任务：可能还在本地暂存（正常），也可能任务已被清空 → 超时才撤
    if (Date.now() - p.at > UPLOAD_PLACEHOLDER_TTL) {
      changed = true
      continue
    }
    kept.push(p)
  }
  if (changed) ui.uploads = kept
}

let upTimer: ReturnType<typeof setTimeout> | null = null
let upWanted: string[] = []

/** 合并 300ms 内的多次完成：批量上传不能每完成一个文件就发一次列表请求。 */
function scheduleRefreshAfterUpload(names: string[]) {
  upWanted.push(...names)
  if (upTimer) clearTimeout(upTimer)
  upTimer = setTimeout(() => {
    upTimer = null
    const want = upWanted
    upWanted = []
    void refreshAfterUpload(want)
  }, 300)
}

/** 立即刷新当前目录；若期望的条目没出现（网盘侧列表索引有短暂滞后），
 *  按 0.8s / 2s 各再试一次 —— 有界重试，不做无限轮询。 */
async function refreshAfterUpload(want: string[]) {
  for (const delay of [0, 800, 2000]) {
    if (delay) await sleep(delay)
    if (ui.page !== 'files' || !ui.snap?.connected) return
    const got = await listDir(ui.remote, {silent: true})
    if (!got || !want.length) return
    const names = new Set(got.map((e) => e.display))
    if (want.every((n) => names.has(n))) return
  }
}

function sleep(ms: number) {
  return new Promise<void>((r) => setTimeout(r, ms))
}


// op 阶段文案：仅置忙碌（后端不发射终态；复位走调用方 finally endOp + onFrame 兜底）
function onOpProgress(msg: unknown) {
  ui.opText = String(msg ?? '')
  ui.opBusy = true
}

// onOpDone/onOpError：事件名保留定义但后端当前不发射（防御性处理器，语义同 endOp）

function onOpDone(msg: unknown) {
  ui.opBusy = false
  ui.opText = ''
  if (msg) showSuccess(String(msg))
}

function onOpError(msg: unknown) {
  ui.opBusy = false
  ui.opText = ''
  showError(unwrap(msg).message || '操作失败')
}

function onLocked() {
  ui.page = 'vaults'
  showInfo('密库已锁定')
}

/* ------------------------------------------------------------ 事件订阅（SSE） */

let eventSource: EventSource | null = null

/** 订阅后端事件（SSE）。幂等：重复 start 不重复注册。 */
export function start() {
  if (started) return
  started = true
  eventSource = new EventSource('/api/events')
  // 所有 SSE 载荷先做空值与 JSON 守卫：服务端异常帧（如空 data）不得
  // 触发全局 unhandledrejection → boot-err 红屏（曾因空帧 JSON.parse 崩溃）。
  eventSource.addEventListener(evt.EvtFrame, (ev) => {
    const raw = ev.data as string
    if (!raw) return
    try {
      onFrame(JSON.parse(raw))
    } catch {
      /* 坏帧丢弃 */
    }
  })
  eventSource.addEventListener(evt.EvtOpProgress, (ev) => {
    const raw = ev.data as string
    if (!raw) return
    try {
      onOpProgress(JSON.parse(raw))
    } catch {
      /* 坏帧丢弃 */
    }
  })
  eventSource.addEventListener(evt.EvtOpDone, (ev) => {
    const raw = ev.data as string
    if (!raw) return
    try {
      onOpDone(JSON.parse(raw))
    } catch {
      /* 坏帧丢弃 */
    }
  })
  eventSource.addEventListener(evt.EvtOpError, (ev) => {
    const raw = ev.data as string
    if (!raw) return
    try {
      onOpError(JSON.parse(raw))
    } catch {
      /* 坏帧丢弃 */
    }
  })
  eventSource.addEventListener(evt.EvtLocked, () => onLocked())
  if (!pollTimer) pollTimer = setInterval(pollTick, POLL_MS)
  void boot()
}

/** 注销事件（应用退出前；幂等）。 */
export function stop() {
  if (!started) return
  started = false
  eventSource?.close()
  eventSource = null
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

/** 启动引导：同步一次快照 + 应用设置（主题/字号，供设置页下拉回显）。 */
async function boot() {
  try {
    ui.snap = await Vault.State()
    // 连接态恢复浏览态（重启后仍连接时首屏直接文件页）
    if (ui.snap.connected) {
      ui.page = 'files'
      void listDir('')
    }
  } catch {
    /* 未连接时 State 返回空快照，无需处理 */
  }
  try {
    ui.settings = await Settings.Get()
    const themeIdx = Number(ui.settings.themeIndex ?? 0)
    applyThemeIndex(themeIdx)
    const fs = Number(ui.settings.fontSize ?? 14)
    if (fs >= 12 && fs <= 18) applyFontSize(fs)
  } catch (e) {
    showError('读取设置失败：' + unwrap(e).message)
  }
}

/** 设置字号：写 html 根 font-size（rem 体系整体缩放）。 */
export function applyFontSize(px: number) {
  document.documentElement.style.setProperty('--font-size', px + 'px')
}

/* ------------------------------------------------------------ 浏览与选中 */

/**
 * 列目录：seq 代际防旧结果覆盖新目录。
 * silent=true 时不置 loading、不清 sel，用于传输收敛与空闲轮询的静默刷新；
 * 静默模式仍保留 seq 代际防护，仅在条目集合确实变化时写回 ui.entries，避免闪烁。
 * 返回本次读到的条目（代际已过期/失败返回 null），供「上传完成即刷新」核对。
 */
export async function listDir(
  remote: string,
  opts: {silent?: boolean} = {},
): Promise<appstate.FileEntry[] | null> {
  ui.seq++
  const seq = ui.seq
  ui.remote = remote
  if (!opts.silent) {
    ui.loading = true
    ui.loadError = ''
    ui.sel = null
    ui.multi = []
  }
  try {
    const entries = await Files.List(remote)
    if (seq !== ui.seq) return null // 期间已切换目录
    if (opts.silent) {
      // 仅在条目集合变化时写回，保留当前 sel 避免闪烁
      if (!entriesEqual(ui.entries, entries)) {
        ui.entries = entries
        if (ui.sel && !entries.some((e) => e.remote === ui.sel!.remote)) {
          ui.sel = null
          ui.multi = []
        } else {
          // 多选集合也按条目存活过滤（被删除/移动的 remote 移出）
          ui.multi = ui.multi.filter((m) => entries.some((e) => e.remote === m.remote))
        }
      }
    } else {
      ui.entries = entries
    }
    return entries
  } catch (e) {
    const err = unwrap(e)
    if (seq !== ui.seq) return null
    if (opts.silent) return null // 静默失败不打扰用户
    ui.entries = []
    ui.loadError = err.message
    if (err.code !== ApiCode.Locked) showError('读取目录失败：' + err.message)
    return null
  } finally {
    if (seq === ui.seq && !opts.silent) ui.loading = false
  }
}

/** 静默刷新当前目录（不闪烁、不清选择）。失败静默。 */
export function refreshSilent() {
  if (ui.page !== 'files' || !ui.snap?.connected) return
  void listDir(ui.remote, {silent: true})
}

/** 浅比较：条目集合变化（数量/remote 集合）时返回 false。 */
function entriesEqual(
  a: appstate.FileEntry[] | null,
  b: appstate.FileEntry[],
): boolean {
  if (a === null) return false
  if (a.length !== b.length) return false
  const set = new Set(a.map((e) => e.remote))
  for (const e of b) if (!set.has(e.remote)) return false
  return true
}

/** 刷新当前目录（F5 / 传输结束后）；root=true 重置到密库根。 */
export function reloadDir() {
  if (ui.page !== 'files') return
  lastManualReloadAt = Date.now()
  void listDir(ui.remote)
}

function resetBrowse() {
  ui.remote = ''
  ui.entries = null
  ui.sel = null
  ui.multi = []
  ui.crumbs = []
  ui.seq++
}

/** 单选（普通单击 / 右键目标）：sel 与 multi 同步为该项，长度=1 即非批量态。 */
export function selectEntry(e: appstate.FileEntry | null) {
  ui.sel = e
  ui.multi = e ? [e] : []
}

/** 切换多选（Ctrl/Shift+单击）：在集合中加入/移除，sel 恒指向集合最后项。 */
export function toggleMulti(e: appstate.FileEntry) {
  const i = ui.multi.findIndex((x) => x.remote === e.remote)
  if (i >= 0) {
    ui.multi.splice(i, 1) // 取消勾选
  } else {
    ui.multi.push(e)
  }
  ui.sel = ui.multi.length ? ui.multi[ui.multi.length - 1] : null
}

/** 范围多选：把 entries 中 [anchorRemote, e.remote] 区间全部加入集合。 */
export function rangeMulti(e: appstate.FileEntry, anchorRemote: string | null, entries: appstate.FileEntry[]) {
  const a = anchorRemote ? entries.findIndex((x) => x.remote === anchorRemote) : -1
  const b = entries.findIndex((x) => x.remote === e.remote)
  const lo = a >= 0 && a < b ? a : b
  const hi = a >= 0 && a > b ? a : b === a ? (b < entries.length - 1 ? b + 1 : b) : b
  const set = new Map(ui.multi.map((x) => [x.remote, x]))
  for (let i = Math.min(lo, hi); i <= Math.max(lo, hi); i++) {
    set.set(entries[i].remote, entries[i])
  }
  ui.multi = [...set.values()]
  ui.sel = ui.multi.length ? ui.multi[ui.multi.length - 1] : null
}

/** 清空选择（切换目录/全不选时）。 */
export function clearMulti() {
  ui.sel = null
  ui.multi = []
}

/** 当前批量选择集（multi；空时为空数组）。 */
export function selEntries(): appstate.FileEntry[] {
  return ui.multi
}

/** 进入目录（双击网格/列表中的目录行）。 */
export function enterDir(e: appstate.FileEntry) {
  if (!ui.snap?.connected || !e.isDir) return
  if (ui.remote === e.remote) return // 同目录幂等（防双击重复压栈）
  ui.crumbs.push({label: e.display, remote: e.remote})
  void listDir(e.remote)
}

/** 面包屑跳转到第 i 级目录并截断其后链（i = 0 表示根）。 */
export function crumbTo(i: number) {
  ui.crumbs = ui.crumbs.slice(0, i + 1)
  const last = ui.crumbs[ui.crumbs.length - 1]
  void listDir(last ? last.remote : '')
}

/** 返回上级目录（面包屑回退一级；根时无操作）。 */
export function goUp() {
  if (!ui.crumbs.length) return
  ui.crumbs.pop()
  const last = ui.crumbs[ui.crumbs.length - 1]
  void listDir(last ? last.remote : '')
}

/** 浏览器改模式（list/grid）保持选择语义。 */
export function setViewMode(m: ViewMode) {
  ui.viewMode = m
}

/** 页面导航（锁库事件会强制回密库页）。 */
export function navigate(p: PageId) {
  ui.page = p
  if (p === 'files' && ui.snap?.connected && ui.entries === null) void listDir(ui.remote)
}

/* ---------------------------------------------------------- 文件动作封装 */

/** 选择并上传（工具栏/拖放/右键共用；remoteDir 缺省为当前浏览目录）。
 *  注意：上传/下载结束后**不再**强制跳转传输页 —— 进度由底部 TransferBar
 *  展示，用户想细看再点「详情」。批次收敛后由 onFrame 静默刷新当前目录。
 *
 *  占位策略：发请求**之前**就在当前目录挂上乐观条目（用户要求「上传的时候
 *  文件就在列表里」），随后由 onFrame 的任务对账逐步换成真实进度、终结即撤下。
 *  只给直接落在 remoteDir 的文件占位（rel 带 '/' 会进子目录，占在当前目录是错位）。 */
export async function uploadFiles(items: UploadItem[], remoteDir: string = ui.remote) {
  if (!items.length) {
    showInfo('没有可上传的文件')
    return
  }
  const stamp = Date.now()
  const batch: UploadPlaceholder[] = items
    .filter((it) => !it.rel.includes('/'))
    .map((it, i) => ({
      key: `${stamp}-${i}-${it.rel}`,
      name: it.rel,
      size: it.file.size,
      dir: remoteDir,
      at: stamp,
      taskId: 0,
      progress: null,
      state: '',
    }))
  if (batch.length) ui.uploads.push(...batch)
  try {
    // 加密发生在入队后的传输管线里（上传 = 加密 → 分块上传），
    // 因此「拖入即加密」在此处只是把文件交给队列，不需要额外步骤。
    await Transfer.Upload(items, remoteDir)
    const dirs = new Set(items.map((it) => it.rel).filter((r) => r.includes('/')))
    const scope = dirs.size ? `（含文件夹，保留目录结构）` : ''
    showInfo(`已加入加密上传队列：${items.length} 个文件${scope}`)
  } catch (e) {
    // 请求本身失败 → 不会产生任务，占位必须立刻撤掉（否则成幽灵条目）
    const keys = new Set(batch.map((b) => b.key))
    ui.uploads = ui.uploads.filter((u) => !keys.has(u.key))
    showError('上传失败：' + unwrap(e).message)
  }
}

/** 全窗口拖放入口：DataTransfer → 条目列表（文件夹递归展开）→ 上传。 */
export async function onDropFiles(dt: DataTransfer) {
  const items = await collectDropped(dt)
  await uploadFiles(items)
}

/** 文件 / 文件夹选择器入口：`<input type=file>` 的结果收集后上传。
 *  目录选择器（webkitdirectory）会填 webkitRelativePath，故同样保留结构。 */
export async function uploadFromFileList(
  files: FileList | File[],
  remoteDir: string = ui.remote,
) {
  const items: UploadItem[] = collectFromFileList(files)
  await uploadFiles(items, remoteDir)
}

/** 取当前生效的操作集合：多选 >1 用 multi，否则回退主条目。 */
function opEntries(): appstate.FileEntry[] {
  return ui.multi.length > 1 ? ui.multi : ui.sel ? [ui.sel] : []
}

/** 批量下载选中条目（浏览器 <a download> 触发保存；服务端 /d/ 流式解密）。
 *  目录暂不支持（多选下提示）。 */
export async function downloadSel() {
  const list = opEntries()
  if (!list.length) return
  if (list.length > 1 && list.some((e) => e.isDir)) {
    showError('暂不支持下载文件夹，请逐个选择文件')
    return
  }
  const firstDir = list.find((e) => e.isDir)
  if (firstDir) {
    showError('暂不支持下载文件夹，请逐个选择文件')
    return
  }
  for (const e of list) {
    try {
      const {url} = await Transfer.DownloadURL(e.remote, e.display)
      triggerDownload(url, e.display)
    } catch (err) {
      showError('下载失败：' + unwrap(err).message)
    }
  }
  showInfo(list.length > 1 ? `已开始下载 ${list.length} 项` : `已开始下载：${list[0].display}`)
}

/** 触发浏览器下载（动态 <a download>）。 */
function triggerDownload(url: string, filename: string) {
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
}

/** 向导/快速连接入参（字段镜像后端 appstate.OpenRequest，可选字段省略）。 */
export interface OpenVaultInput {
  kind: 'local' | 'webdav' | 'baidu'
  localDir?: string
  url?: string
  user?: string
  pass?: string
  masterPassword?: string
  recoveryCode?: string
  filenameEnc?: boolean
  vaultName?: string
  vaultPath?: string
  create?: boolean
}

/**
 * 新建/连接密库的统封装（向导与快速连接共用）：
 * 成功即重置浏览态并切入文件页；新建模式的一次性恢复码写入
 * ui.pendingRecovery，由 App 全局模态展示（页面切换不影响展示）。
 * 失败向上抛 ApiError（调用方负责提示与就地展示）。
 */
export async function openVault(input: OpenVaultInput): Promise<void> {
  const req = {
    kind: input.kind,
    localDir: input.localDir ?? '',
    url: input.url ?? '',
    user: input.user ?? '',
    pass: input.pass ?? '',
    masterPassword: input.masterPassword ?? '',
    recoveryCode: input.recoveryCode ?? '',
    filenameEnc: input.filenameEnc ?? false,
    vaultName: input.vaultName ?? '',
    vaultPath: input.vaultPath ?? '',
    create: input.create ?? false,
  }
  const res = await Vault.Open(req)
  // 新建成功的一次性恢复码：移交全局模态（向导随之卸载也不丢失）
  if (res?.code) ui.pendingRecovery = res.code
  // 锁旧连接时已清浏览态；重置后进入文件页并拉根目录
  resetBrowse()
  ui.page = 'files'
  void listDir('')
}

/** 清空待展示恢复码（App 全局恢复码模态关闭时调用）。 */
export function clearRecovery() {
  ui.pendingRecovery = ''
}

/** 锁定密库（NavRail/密库页共用）。 */
export const lockVault = async () => {
  try {
    await Vault.Lock()
  } catch (e) {
    showError('锁库失败：' + unwrap(e).message)
  }
}

/** 新建文件夹（parentRemote 缺省为当前浏览目录，右键“子文件夹”时指定）。 */
export async function newFolder(display: string, parentRemote: string = ui.remote) {
  if (!display) return
  try {
    await Files.NewFolder(parentRemote, display)
    showSuccess(`已创建文件夹「${display}」`)
    // 若建在当前浏览目录则刷新视图；建在其它目录时仅提示
    if (parentRemote === ui.remote) void reloadDir()
  } catch (e) {
    showError('新建文件夹失败：' + unwrap(e).message)
  }
}

/** 重命名条目。 */
export async function renameSel(newDisplay: string) {
  const e = ui.sel
  if (!e || !newDisplay || newDisplay === e.display) return
  try {
    await Files.Rename(e.remote, newDisplay)
    void reloadDir()
  } catch (err) {
    showError('重命名失败：' + unwrap(err).message)
  }
}

/** 删除条目（目录递归）。多选时批量删除全部选中项。 */
export async function deleteSel() {
  const list = opEntries()
  if (!list.length) return
  try {
    await Files.Delete(list.map((e) => e.remote))
    if (list.length > 1) {
      showSuccess(`已删除 ${list.length} 项`)
    } else {
      showSuccess(`已删除「${list[0].display}」`)
    }
    ui.sel = null
    ui.multi = []
    void reloadDir()
  } catch (err) {
    showError('删除失败：' + unwrap(err).message)
  }
}

/* ------------------------------------------------------- 全局目录选择器 */

/**
 * 网页版本机目录选择器的全局单例状态（App.vue 挂载 FolderPicker，其余页面只调
 * pickLocalDir）。
 *
 * 为什么做成全局单例：调用点分散在向导 / 设置页（同步目录 + 缓存目录）/ 密库页 /
 * 文件页（导出）四处，其中向导自身就在模态里。全局一份既省掉四份重复浮层，
 * 又天然复用 ModalShell 的模态栈（Esc 只关最上层那个，不会连关两层）。
 *
 * 为什么不用主机原生对话框：IFileOpenDialog 以 owner=0 弹出、没有属主窗口，
 * 会跑到浏览器窗口后面 —— 用户看到的是「后台莫名跳出个框」。
 */
export interface LocalDirPick {
  open: boolean
  title: string
  /** 起始目录；空串 = 从「此电脑」开始 */
  start: string
}

export const localDirPick = reactive<LocalDirPick>({open: false, title: '', start: ''})

/** 在途选择的兑现函数；null = 当前没有请求。 */
let pickSettle: ((dir: string | null) => void) | null = null

/**
 * 打开目录选择器并等待用户选择。
 * @param opts.title 选择器标题（说清「在选什么目录」，四处语义各不相同）
 * @param opts.start 起始目录（一般为当前已配置的值）
 * @returns 选中的绝对路径；用户取消返回 null。
 */
export function pickLocalDir(opts: {title: string; start?: string}): Promise<string | null> {
  // 极端情况下（前一次选择器未结清）先兑现旧的，避免 Promise 悬空。
  pickSettle?.(null)
  pickSettle = null
  localDirPick.title = opts.title
  localDirPick.start = opts.start ?? ''
  localDirPick.open = true
  return new Promise<string | null>((resolve) => {
    pickSettle = resolve
  })
}

/** 结清一次选择（由 FolderPicker 调用）：dir 为 null 表示取消。 */
export function settleLocalDir(dir: string | null) {
  localDirPick.open = false
  const done = pickSettle
  pickSettle = null
  done?.(dir)
}

/** 最近一次导出位置的记忆键（同一会话内连续导出多半落在同一处）。 */
const LAST_EXPORT_KEY = 'cp-export-dir'

/** 读取上次导出位置；无记录或读取失败（隐私模式）时返回空串。 */
function lastExportDir(): string {
  try {
    return localStorage.getItem(LAST_EXPORT_KEY) ?? ''
  } catch {
    return ''
  }
}

/** 导出（解密到本地）：单条先选目录再落盘（网页版选择器）；
 *  多选复用浏览器下载（逐条 triggerDownload，与 downloadSel 同路径）。 */
export async function exportSel() {
  const list = opEntries()
  if (!list.length) return
  if (list.length === 1) {
    const e = list[0]
    // 先选目录：取消即静默返回（与「取消原生框不算错误」的既有语义一致）
    const dir = await pickLocalDir({title: '选择导出位置', start: lastExportDir()})
    if (!dir) return
    try {
      const saved = await Files.Export(e.remote, dir)
      try {
        localStorage.setItem(LAST_EXPORT_KEY, dir)
      } catch {
        /* 记不住不影响本次导出 */
      }
      showSuccess(`已导出到 ${saved || dir}`)
    } catch (err) {
      showError('导出失败：' + unwrap(err).message)
    }
    return
  }
  // 多选批量导出（浏览器下载）
  for (const e of list) {
    if (e.isDir) continue
    try {
      const {url} = await Transfer.DownloadURL(e.remote, e.display)
      triggerDownload(url, e.display)
    } catch (err) {
      showError('批量导出失败：' + unwrap(err).message)
    }
  }
  showInfo(`已开始导出 ${list.length} 项`)
}

/** 复制条目明文展示名到剪贴板（右键高级项）。 */
export async function copyEntryName(e: appstate.FileEntry) {
  try {
    await navigator.clipboard.writeText(e.display)
    showInfo('已复制文件名')
  } catch {
    showError('复制失败')
  }
}

/** 复制条目明文展示路径到剪贴板（右键高级项；由面包屑明文链拼接）。 */
export async function copyEntryPath(e: appstate.FileEntry) {
  try {
    const dirs = ui.crumbs.map((c) => c.label)
    const plain = [...dirs, e.display].join('/')
    await navigator.clipboard.writeText(plain)
    showInfo('已复制路径')
  } catch {
    showError('复制失败')
  }
}

/** 播放/预览代理 URL 签发与吊销（Preview 域）。 */
export async function mediaUrl(e: appstate.FileEntry): Promise<string> {
  const url = await Preview.MediaURL(e.remote, e.display)
  return url
}

export async function thumbUrl(remote: string): Promise<string> {
  return await Preview.ThumbURL(remote)
}

export async function revoke(url: string): Promise<void> {
  // 代理 URL 形如 /s/{token}/{display} 或 /t/{token}：token 恒为路径第 2 段
  // （Go 侧 Revoke 按 token 值查注册表，传整条 URL 是空操作）
  try {
    const seg = new URL(url).pathname.split('/')
    await Preview.Revoke(seg[2] ?? '')
  } catch {
    /* 吊销失败静默：注册表随锁库 RevokeAll，不阻塞预览 */
  }
}

/** 结束长操作忙碌态：每个 op 入口的 finally 必调（onFrame 兜底同语义，幂等）。 */
export function endOp() {
  ui.opBusy = false
  ui.opText = ''
}

/** 主密码类长操作（向导/恢复码）在 op 事件窗口内的忙碌态。 */
export function isBusy(): boolean {
  return ui.opBusy
}

// 供视图取用（显式导出类型更直观）
export type {appstate}

// Quit 绑定在 App 域（NavRail 退出钮用）
export const quitApp = () => void App.Quit()

