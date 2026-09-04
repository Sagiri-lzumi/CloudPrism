// store.ts —— 前端唯一响应式全局状态（不引 Pinia：页面级状态足够少）。
//
// 数据流：Wails st:frame 事件（10Hz 合帧）→ 快照/任务写入 ui；
// 视图层动作（列目录/上传/下载/建夹/改名/删除…）经 bind 域调用，
// 错误统一 unwrap 成 ApiError，按 code 分流提示。
//
// 模块单例：App.vue onMounted 调 start()，onBeforeUnmount 调 stop()。

import {reactive} from 'vue'
import {EventsOff, EventsOn} from '../../wailsjs/runtime/runtime'
import type {appstate} from '../../wailsjs/go/models'
import {ApiCode, App, Files, Preview, Settings, Transfer, Vault, unwrap} from './api'
import * as evt from './events'
import {applyThemeIndex} from './theme'
import {showError, showInfo, showSuccess} from './toast'

export type PageId = 'files' | 'transfers' | 'vaults' | 'settings'

/** 文件浏览模式：list = 目录树（懒加载层级），grid = 当前目录缩略图网格。 */
export type ViewMode = 'list' | 'grid'

/** 明文导航链的一段（面包屑用）：label 为解密展示名，remote 为回传密文路径。
 *  后端 FileEntry.remote 是加密名拼接路径，不可直接当展示文本。 */
export interface Crumb {
  label: string
  remote: string
}

interface Ui {
  // —— 连接与会话（st:frame 驱动）——
  snap: appstate.Snapshot | null
  /** 活动传输任务明细（仅帧内 transferActive 时更新） */
  tasks: appstate.TaskView[]
  /** 长操作（向导/恢复码）阶段文案；空串=无操作 */
  opText: string
  opBusy: boolean

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
  /** 选中条目（文件或目录；remote path 唯一键） */
  sel: appstate.FileEntry | null
  /** 明文导航链（面包屑）：每次受管进入目录时 push，返回/跳转时截断 */
  crumbs: Crumb[]

  // —— 设置缓存（启动 Get 一次，设置页读写同一份）——
  settings: Record<string, unknown>
}

export const ui = reactive<Ui>({
  snap: null,
  tasks: [],
  opText: '',
  opBusy: false,
  page: 'vaults', // 启动默认密库页（未连接引导）
  viewMode: 'grid',
  remote: '',
  entries: null,
  loading: false,
  loadError: '',
  seq: 0,
  sel: null,
  crumbs: [],
  settings: {},
})

/* ---------------------------------------------------------------- 事件流 */

let started = false

// 帧事件处理器固定引用（EventsOff 需要同一引用）
function onFrame(payload: unknown) {
  const f = payload as {snap?: appstate.Snapshot; tasks?: appstate.TaskView[]}
  if (!f || !f.snap) return
  ui.snap = f.snap
  ui.tasks = f.tasks ?? []
  // 锁库/连接态变化时清理遗留的浏览态
  if (!f.snap.connected) {
    if (ui.remote !== '') resetBrowse()
  }
}

function onOpProgress(msg: unknown) {
  ui.opText = String(msg ?? '')
  ui.opBusy = true
}

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

/** st:files-dropped：系统文件拖入 → 上传到当前浏览目录。 */
function onDropped(paths: unknown) {
  const list = paths as string[]
  if (!Array.isArray(list) || list.length === 0) return
  void uploadPaths(list)
}

/** 订阅后端事件（幂等：重复 start 不重复注册）。 */
export function start() {
  if (started) return
  started = true
  EventsOn(evt.EvtFrame, onFrame)
  EventsOn(evt.EvtOpProgress, onOpProgress)
  EventsOn(evt.EvtOpDone, onOpDone)
  EventsOn(evt.EvtOpError, onOpError)
  EventsOn(evt.EvtLocked, onLocked)
  EventsOn(evt.EvtDropped, onDropped)
  void boot()
}

/** 注销事件（应用退出前；幂等）。 */
export function stop() {
  if (!started) return
  started = false
  EventsOff(evt.EvtFrame)
  EventsOff(evt.EvtOpProgress)
  EventsOff(evt.EvtOpDone)
  EventsOff(evt.EvtOpError)
  EventsOff(evt.EvtLocked)
  EventsOff(evt.EvtDropped)
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

/** 列目录：seq 代际防旧结果覆盖新目录。 */
export async function listDir(remote: string) {
  ui.seq++
  const seq = ui.seq
  ui.remote = remote
  ui.loading = true
  ui.loadError = ''
  ui.sel = null
  try {
    const entries = await Files.List(remote)
    if (seq !== ui.seq) return // 期间已切换目录
    ui.entries = entries
  } catch (e) {
    const err = unwrap(e)
    if (seq !== ui.seq) return
    ui.entries = []
    ui.loadError = err.message
    if (err.code !== ApiCode.Locked) showError('读取目录失败：' + err.message)
  } finally {
    if (seq === ui.seq) ui.loading = false
  }
}

/** 刷新当前目录（F5 / 传输结束后）；root=true 重置到密库根。 */
export function reloadDir() {
  if (ui.page !== 'files') return
  void listDir(ui.remote)
}

function resetBrowse() {
  ui.remote = ''
  ui.entries = null
  ui.sel = null
  ui.crumbs = []
  ui.seq++
}

/** 选中条目（列表单击；grid 单击同语义）。 */
export function selectEntry(e: appstate.FileEntry | null) {
  ui.sel = e
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

/** 选择并上传（工具栏/拖放/右键共用；remoteDir 缺省为当前浏览目录）。 */
export async function uploadPaths(localPaths: string[], remoteDir: string = ui.remote) {
  try {
    await Transfer.Upload(localPaths, remoteDir)
    showInfo(`已加入上传队列：${localPaths.length} 项`)
    // 传完由任务帧驱动；切到传输页让用户看到进度
    ui.page = 'transfers'
  } catch (e) {
    showError('上传失败：' + unwrap(e).message)
  }
}

/** 下载选中条目到用户选择目录（选中目录则整体递归由后端处理）。 */
export async function downloadSel() {
  const e = ui.sel
  if (!e) return
  try {
    const dir = await Transfer.DownloadDialog()
    if (!dir) return // 用户取消
    await Transfer.Download([e], dir)
    ui.page = 'transfers'
  } catch (err) {
    showError('下载失败：' + unwrap(err).message)
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

/** 删除条目（目录递归）。 */
export async function deleteSel() {
  const e = ui.sel
  if (!e) return
  try {
    await Files.Delete([e.remote])
    showSuccess(`已删除「${e.display}」`)
    if (ui.sel?.remote === e.remote) ui.sel = null
    void reloadDir()
  } catch (err) {
    showError('删除失败：' + unwrap(err).message)
  }
}

/** 导出（解密到本地）：目录框由 Go 弹原生对话框。 */
export async function exportSel() {
  const e = ui.sel
  if (!e) return
  try {
    const dir = await Files.Export(e.remote)
    if (!dir) return
    showSuccess(`已导出到 ${dir}`)
  } catch (err) {
    showError('导出失败：' + unwrap(err).message)
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

/** 主密码类长操作（向导/恢复码）在 op 事件窗口内的忙碌态。 */
export function isBusy(): boolean {
  return ui.opBusy
}

// 供视图取用（显式导出类型更直观）
export type {appstate}

// Quit 绑定在 App 域（NavRail 退出钮用）
export const quitApp = () => void App.Quit()
export const lockVault = async () => {
  try {
    await Vault.Lock()
  } catch (e) {
    showError('锁库失败：' + unwrap(e).message)
  }
}
