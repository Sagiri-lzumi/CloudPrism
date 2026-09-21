<!--
  FilesView.vue —— 文件浏览页（工具栏 + 面包屑 + 网格/列表 + 预览双栏）。
  对照 WindowsPy FilesPage/FileTreeView：当前目录语义由 store 驱动，
  网格卡片缩略图走 /t/ 令牌（GridCard），预览面板在 Splitter 右栏。
  交互：单击选中 → 预览面板即时更新；双击目录进入（明文链压栈）；
  右键目标条弹动作菜单；工具栏模式钮切换 list/grid（qfw 双视图语义）。
-->
<script setup lang="ts">
import {computed, reactive, ref} from 'vue'
import type {appstate} from '../types/appstate'
import {
  ui,
  selectEntry,
  enterDir,
  reloadDir,
  crumbTo,
  setViewMode,
  downloadSel,
  exportSel,
  newFolder,
  renameSel,
  deleteSel,
  uploadFromFileList,
  navigate,
  toggleMulti,
  rangeMulti,
  clearMulti,
  copyEntryName,
  copyEntryPath,
} from '../lib/store'
import {fmtSize} from '../lib/format'
import {kindOf, KIND_ICON} from '../lib/media'
import Button from '../components/fluent/Button.vue'
import PrimaryButton from '../components/fluent/PrimaryButton.vue'
import Icon from '../components/fluent/Icon.vue'
import RoundMenu from '../components/fluent/RoundMenu.vue'
import MessageBox from '../components/fluent/MessageBox.vue'
import ProgressBar from '../components/fluent/ProgressBar.vue'
import PageHeader from '../components/layout/PageHeader.vue'
import GridCard from './GridCard.vue'
import PreviewPanel from './PreviewPanel.vue'

/* ------------------------------------------------------------- 派生 */

const connected = computed(() => !!ui.snap?.connected)
const crumbs = computed(() => ui.crumbs)
const entries = computed(() => ui.entries ?? [])
const multiSel = computed(() => ui.multi)
const hasMulti = computed(() => ui.multi.length > 1)
const showEmpty = computed(
  () => !ui.loading && !ui.loadError && ui.entries !== null && ui.entries.length === 0,
)

function isSel(e: appstate.FileEntry): boolean {
  return ui.multi.some((x) => x.remote === e.remote)
}

function kindOfRow(e: appstate.FileEntry): string {
  return e.isDir ? 'folder' : KIND_ICON[kindOf(e.display)]
}

/* -------------------------------------------------- 点击/多选交互 */

// shift 连选锚点：最近一次「普通单击」的主条目 remote（无则取列表首项）
const shiftAnchor = ref<string | null>(null)

/** 触摸设备判定：无 hover 且指针不精确（手机/平板）。桌面触屏笔记本主指针
 *  仍是鼠标（hover:hover），不会命中，故不影响桌面交互。
 */
const isCoarsePointer = () => window.matchMedia('(hover: none) and (pointer: coarse)').matches

function onItemClick(ev: MouseEvent, e: appstate.FileEntry) {
  // 触摸设备无双击（网格卡的 @dblclick 永不触发）、也无 Ctrl/Shift 修饰键，
  // 目录若是只选中就会拿不到进入入口 → 触摸下目录单击直接进入。
  if (e.isDir && isCoarsePointer()) {
    enterDir(e)
    return
  }
  if (ev.shiftKey) {
    rangeMulti(e, shiftAnchor.value, entries.value)
    return
  }
  if (ev.ctrlKey || ev.metaKey) {
    toggleMulti(e)
    return
  }
  selectEntry(e) // 普通单击：单选（多选集合重置）
  shiftAnchor.value = e.remote
}

/** 网格卡 ctx 事件载荷：GridCard emit 单对象 {ev, entry}，此处拆开后调 openCtx。
 *  改用方法引用（而非 inline `openCtx($event, e)`）绕开 Vue 3 编译器在组件事件上
 *  丢弃闭包变量 `e` 的 bug——否则 GridCard 右键永远拿到 entry=null 弹空白菜单。 */
function onCardCtx(p: {ev: MouseEvent; entry: appstate.FileEntry}) {
  openCtx(p.ev, p.entry)
}

/* ------------------------------------------------- 检查器（预览栏）开关与拖柄 */

// 「可收起检查器」：默认收起，选中条目时自动滑出（让未选中时的列表吃满整宽，
// 不再有一条 900px 宽、只显示空态提示的常驻预览栏）。
// 用户一旦手动收起，就记为关闭偏好，之后不再自动弹出——直到再点开关。
const INSP_KEY = 'cp-inspector'
const INSP_W_KEY = 'cp-insp-w'

/** 用户偏好：检查器是否可用（false = 用户显式收起了） */
const inspectorOn = ref(localStorage.getItem(INSP_KEY) !== '0')
/** 检查器宽度（px），拖动左缘调整。
 *  注意它**不是**最终宽度：CSS 的 minmax(60%, …) 会给它一个 60% 硬下限、
 *  并以「容器宽 −244px」封顶（预览是主区，见 .files-shell.insp 注释）。
 *  缺省 380 只是「没拖过」时的占位值 —— 会被下限顶到 60%。 */
const inspW = ref(Number(localStorage.getItem(INSP_W_KEY)) || 380)

const viewEl = ref<HTMLElement>()
const dragging = ref(false)

/** 检查器是否真的占位：偏好开启 + 当前有选中条目 */
const showInspector = computed(() => inspectorOn.value && !!ui.sel)

function toggleInspector() {
  inspectorOn.value = !inspectorOn.value
  localStorage.setItem(INSP_KEY, inspectorOn.value ? '1' : '0')
}

// 拖柄：拖动调的是「检查器宽度」（列表恒占剩余宽度）
function splitDown(e: PointerEvent) {
  e.preventDefault()
  dragging.value = true
  window.addEventListener('pointermove', splitMove)
  window.addEventListener('pointerup', splitEnd, {once: true})
}

function splitMove(e: PointerEvent) {
  const box = viewEl.value!.getBoundingClientRect()
  // 钳制跟 CSS 同一套约束（两侧都写是有原因的，见 .files-shell.insp 注释）：
  //   下限 = 容器宽 60%（预览是重点，用户 2026-09-21 明确要求）
  //   上限 = 容器宽 − 244px（列表保底 240px + 4px 拖柄）
  // CSS 里那份保证缩放窗口后下限仍成立，这份保证拖动时手柄不越界。
  const minW = Math.round(box.width * 0.6)
  const maxW = Math.max(minW, Math.round(box.width - 244))
  inspW.value = Math.min(Math.max(box.right - e.clientX, minW), maxW)
}

function splitEnd() {
  dragging.value = false
  window.removeEventListener('pointermove', splitMove)
  localStorage.setItem(INSP_W_KEY, String(inspW.value))
}

/* --------------------------------------------------- 顶栏下拉菜单（上传/更多） */

// 与右键菜单共用 RoundMenu 与 CtxItem 结构，但内容不同：
// 这两个是**页面级**动作，与「针对某个条目的动作」分开，避免语义混用。
type BarMenu = 'none' | 'upload' | 'more'

const barMenu = ref<BarMenu>('none')
const barAnchor = ref<HTMLElement | null>(null)

const barItems = computed<CtxItem[]>(() => {
  if (barMenu.value === 'upload') {
    return [
      {id: 'uploadFiles', label: '上传文件…', icon: 'send'},
      {id: 'uploadFolder', label: '上传文件夹…', icon: 'folder-up'},
    ]
  }
  if (barMenu.value === 'more') {
    return [
      {id: 'refresh', label: '刷新', icon: 'update'},
      {divider: true},
      {id: 'export', label: `导出 (${multiSel.value.length})`, icon: 'share', disabled: !multiSel.value.length},
      {divider: true},
      {id: 'clearMulti', label: '取消选择', icon: 'cancel', disabled: !multiSel.value.length},
    ]
  }
  return []
})

/** 打开顶栏菜单：用触发按钮自身作锚点（按钮在顶栏里，位置天然正确）。 */
function openBar(which: Exclude<BarMenu, 'none'>, ev: MouseEvent) {
  barAnchor.value = (ev.currentTarget ?? ev.target) as HTMLElement | null
  barMenu.value = 'none'
  barMenu.value = which
}

function onBarIndex(i: number) {
  const id = barItems.value[i]?.id
  if (!id) return
  barMenu.value = 'none'
  void onCtx(id)
}

/* --------------------------------------------------------- 右键/更多菜单 */

// 菜单 items 是**唯一事实源**：右键、列表行 ⋯、网格卡 ⋯ 三条入口
// 全部经由 openCtx → ctxItems 取同一份内容，保证任意入口弹出的菜单一致。
//
// 分发按 **id** 而非数组下标：早期实现用下标分发，加一项就得同步改两处
// 数字（分隔线还占下标），极易错位。现在 id 是唯一契约，插项不影响既有动作。
// 目标类型优先级：multi=多选批量（目标 ∈ 当前多选集且 >1 项）→
//   blank=无目标（空白区）→ dir=目录 → file=文件

/** 菜单动作全集。新增动作只需在此加一项 + 在 onCtx 加一个 case。 */
type CtxAction =
  | 'open'
  | 'preview'
  | 'newFolder'
  | 'newSubFolder'
  | 'uploadFiles'
  | 'uploadFolder'
  | 'download'
  | 'export'
  | 'copyName'
  | 'copyPath'
  | 'rename'
  | 'delete'
  | 'refresh'
  | 'clearMulti'

interface CtxItem {
  /** 动作标识；纯分隔线无 id */
  id?: CtxAction
  label?: string
  icon?: string
  divider?: boolean
  danger?: boolean
}

const ctxOpen = ref(false)
const ctxAnchor = ref<HTMLElement | null>(null)
const ctxPos = ref<{x: number; y: number} | null>(null)
const ctxEntry = ref<appstate.FileEntry | null>(null)

// 右键目标是当前多选集合成员且多选>1 → 批量菜单
const ctxMulti = computed(
  () =>
    !!ctxEntry.value &&
    hasMulti.value &&
    ui.multi.some((x) => x.remote === ctxEntry.value!.remote),
)

/** 构建目标上下文菜单项（分隔线不参与分发，动作靠 id 匹配）。 */
function buildCtxItems(): CtxItem[] {
  const e = ctxEntry.value
  if (ctxMulti.value && e) {
    // 多选批量菜单：精简版（动词领先 + 数量括号）
    const n = ui.multi.length
    return [
      {id: 'download', label: `下载 (${n})`, icon: 'download'},
      {id: 'export', label: `导出 (${n})`, icon: 'share'},
      {divider: true},
      {id: 'delete', label: `删除 (${n})`, icon: 'delete', danger: true},
      {id: 'clearMulti', label: '取消选择', icon: 'cancel'},
    ]
  }
  if (!e)
    return [
      {id: 'newFolder', label: '新建文件夹', icon: 'folder_add'},
      {id: 'uploadFiles', label: '上传文件…', icon: 'send'},
      {id: 'uploadFolder', label: '上传文件夹…', icon: 'folder-up'},
      {divider: true},
      {id: 'refresh', label: '刷新', icon: 'update'},
    ]
  if (e.isDir)
    return [
      {id: 'open', label: '打开', icon: 'folder'},
      {id: 'newSubFolder', label: '新建子文件夹', icon: 'folder_add'},
      {id: 'uploadFiles', label: '上传文件', icon: 'send'},
      {id: 'uploadFolder', label: '上传文件夹', icon: 'folder-up'},
      {divider: true},
      {id: 'download', label: '下载', icon: 'download'},
      {divider: true},
      {id: 'copyPath', label: '复制路径', icon: 'copy'},
      {divider: true},
      {id: 'rename', label: '重命名', icon: 'edit'},
      {id: 'delete', label: '删除', icon: 'delete', danger: true},
    ]
  return [
    {id: 'preview', label: '打开预览', icon: 'photo'},
    {id: 'download', label: '下载', icon: 'download'},
    {id: 'export', label: '导出', icon: 'share'},
    {divider: true},
    {id: 'copyName', label: '复制名称', icon: 'copy'},
    {id: 'copyPath', label: '复制路径', icon: 'copy'},
    {divider: true},
    {id: 'rename', label: '重命名', icon: 'edit'},
    {id: 'delete', label: '删除', icon: 'delete', danger: true},
  ]
}

// 响应式取用单一事实源；任何入口改动 ctxEntry 都会经此重算菜单内容
const ctxItems = computed<CtxItem[]>(() => buildCtxItems())

/** RoundMenu 以数组下标回调（其 items 契约不含 id，且被 ComboBoxCard 共用），
 *  这里做一次下标→动作 id 的适配；分隔线不会被点中，故取不到 id 时静默忽略。 */
function onCtxIndex(i: number) {
  const id = ctxItems.value[i]?.id
  if (id) void onCtx(id)
}

function openCtx(ev: MouseEvent, entry?: appstate.FileEntry | null) {
  // 所有入口都显式内联传参：GridCard @ctx="openCtx($event, e)"、列表行
  // @contextmenu/@click ⋯、空白区 openCtx($event, undefined)，不依赖
  // 组件 emit 的传参语义，杜绝「目标丢失弹空白菜单」类问题。
  const ent: appstate.FileEntry | null = entry ?? null
  // 先判定：目标已是当前多选集合成员且 >1 项 → 保留多选并弹批量菜单；
  // 否则把该目标收敛为单选（selectEntry 会重置 multi 为单元素）。
  const inMulti = !!ent && ui.multi.length > 1 && ui.multi.some((x) => x.remote === ent!.remote)
  if (ent && !inMulti) selectEntry(ent)
  ctxEntry.value = ent
  // 菜单跟随光标弹出：记录鼠标坐标（RoundMenu position 优先于元素锚点）
  ctxPos.value = {x: ev.clientX, y: ev.clientY}
  const el = (ev.currentTarget ?? ev.target) as HTMLElement | null
  ctxAnchor.value = el
  ctxOpen.value = false
  ctxOpen.value = true
}

// 菜单动作分发：按 id 匹配（分隔线无 id，不参与分发）
async function onCtx(action: CtxAction) {
  const e = ctxEntry.value
  switch (action) {
    case 'open':
      if (e) enterDir(e)
      return
    case 'preview':
      if (e) selectEntry(e) // 已在集合/主条目，预览即打开
      return
    case 'newFolder':
      openMsg('newFolder')
      return
    case 'newSubFolder':
      openMsg('newSubFolder')
      return
    case 'uploadFiles':
      await pickUpload(e?.remote ?? ui.remote, false)
      return
    case 'uploadFolder':
      await pickUpload(e?.remote ?? ui.remote, true)
      return
    case 'download':
      void downloadSel()
      return
    case 'export':
      void exportSel()
      return
    case 'copyName':
      if (e) copyEntryName(e)
      return
    case 'copyPath':
      if (e) copyEntryPath(e)
      return
    case 'rename':
      openMsg('rename')
      return
    case 'delete':
      openMsg('delete')
      return
    case 'refresh':
      reloadDir()
      return
    case 'clearMulti':
      clearMulti()
      return
  }
}

/* ------------------------------------------------- 上传对话框（浏览器 file input） */

/**
 * 打开系统选择器并上传。
 *
 * @param remoteDir 目标远端目录
 * @param directory true = 选文件夹（webkitdirectory，目录结构随 webkitRelativePath
 *   一路带到后端）；false = 选多个文件（平铺到当前目录）
 *
 * 用 `document.createElement('input')` 而非模板里的隐藏 input：每次点击都是
 * 全新元素，天然规避「选同一批文件不触发 change」的老问题。
 */
async function pickUpload(remoteDir: string = ui.remote, directory = false) {
  const input = document.createElement('input')
  input.type = 'file'
  input.multiple = true
  if (directory) {
    // 非标准但 Chromium 全系支持；Web 模式下界面始终跑在浏览器里，可用
    input.webkitdirectory = true
  }
  input.onchange = () => {
    if (input.files?.length) void uploadFromFileList(input.files, remoteDir)
  }
  input.click()
}

/* ------------------------------------------------- 模态对话框队列 */

type DlgKind = 'newFolder' | 'newSubFolder' | 'rename' | 'delete'

const dlg = reactive({
  open: false,
  kind: '' as DlgKind | '',
  title: '',
  content: '',
  inputLabel: '',
  initial: '',
  danger: false,
})

function openMsg(kind: DlgKind) {
  const e = ui.sel
  const n = ui.multi.length
  dlg.kind = kind
  dlg.danger = false
  if (kind === 'newFolder') {
    dlg.title = '新建文件夹'
    dlg.inputLabel = '文件夹名称'
    dlg.initial = ''
    dlg.content = ''
  } else if (kind === 'newSubFolder') {
    dlg.title = '新建子文件夹'
    dlg.inputLabel = '文件夹名称'
    dlg.initial = ''
    dlg.content = `将创建在「${e?.display ?? ''}」内`
  } else if (kind === 'rename') {
    dlg.title = '重命名'
    dlg.inputLabel = '新名称'
    dlg.initial = e?.display ?? ''
    dlg.content = ''
  } else {
    dlg.title = n > 1 ? `删除所选 ${n} 项？` : '删除确认'
    dlg.inputLabel = ''
    dlg.initial = ''
    dlg.danger = true
    dlg.content =
      n > 1
        ? `将删除所选 ${n} 项（含目录则递归其全部内容），此操作不可撤销。`
        : e?.isDir
          ? `将删除文件夹「${e?.display ?? ''}」及其全部内容，此操作不可撤销。`
          : `将删除文件「${e?.display ?? ''}」，此操作不可撤销。`
  }
  dlg.open = true
}

function onDlgConfirm(payload: string | boolean) {
  const value = String(payload)
  if (dlg.kind === 'newFolder') void newFolder(value)
  else if (dlg.kind === 'newSubFolder') void newFolder(value, ui.sel?.remote)
  else if (dlg.kind === 'rename') void renameSel(value)
  else if (dlg.kind === 'delete') void deleteSel()
}

/** 对话框确认事件（模板绑定：先关框再执行，防双弹） */
function confirmDlg(payload: string | boolean) {
  dlg.open = false
  onDlgConfirm(payload)
}
</script>

<template>
  <!-- 拖放不再由本页接管：App.vue 上有全窗口热区（任意页面都可拖入上传），
       此处只负责渲染。重复绑定会导致同一次 drop 触发两次上传。 -->
  <div ref="viewEl" class="files-view">
    <!-- 未连接：引导回密库页（锁库事件后兜底） -->
    <div v-if="!connected" class="empty-state">
      <span class="plate"><Icon name="folder" :size="32" /></span>
      <p class="lead">尚未连接密库</p>
      <p class="sub">连接后即可浏览文件。锁库或断开后浏览状态会重置。</p>
      <Button icon="certificate" @click="navigate('vaults')">前往连接</Button>
    </div>

    <template v-else>
      <!-- 统一页头（56px）：左区「我在哪」（面包屑；选中时换成批量摘要），
           右区「能做什么」。此前这里是 9 个无文字图标钮一字排开、动作全靠
           记忆，现收敛为 2 个带文字主操作 + 1 个分段控件 + 2 个次级图标钮；
           下载/导出/删除只在**有选中**时出现，不再常驻占位。 -->
      <PageHeader>
        <nav v-if="!multiSel.length" class="crumbs" aria-label="路径">
          <button
            type="button"
            class="crumb root"
            :class="{on: !crumbs.length}"
            title="密库根目录"
            @click="crumbTo(-1)"
          >
            <Icon name="home" :size="13" />
          </button>
          <template v-for="(c, i) in crumbs" :key="c.remote">
            <Icon name="chevron_right_med" :size="12" class="arrow" />
            <button
              type="button"
              class="crumb"
              :class="{on: i === crumbs.length - 1}"
              :title="c.label"
              @click="crumbTo(i)"
            >
              {{ c.label }}
            </button>
          </template>
        </nav>

        <!-- 选中态：左区让位给批量摘要（Finder 语义——工具栏整体进入批量模式），
             「我在哪」由侧栏目录树的选中项继续承担，信息不丢失 -->
        <div v-else class="sel-summary">
          <span class="sel-count">已选 {{ multiSel.length }} 项</span>
          <button type="button" class="sel-clear" title="取消选择" @click="clearMulti">
            <Icon name="cancel" :size="12" />
          </button>
        </div>

        <template #actions>
          <!-- 常态：两个主操作 -->
          <template v-if="!multiSel.length">
            <PrimaryButton
              icon="send"
              title="上传文件或文件夹到当前目录"
              @click="openBar('upload', $event)"
            >
              上传<Icon name="care_down_solid" :size="9" class="caret" />
            </PrimaryButton>
            <Button icon="folder_add" @click="openMsg('newFolder')">新建文件夹</Button>
          </template>

          <!-- 选中态：动作随选择出现（单选与多选共用同一处，不再让单选只能右键） -->
          <template v-else>
            <Button icon="download" :disabled="!connected" @click="downloadSel">下载</Button>
            <Button icon="share" :disabled="!connected" @click="exportSel">导出</Button>
            <Button icon="delete" danger @click="openMsg('delete')">删除</Button>
          </template>

          <span class="sep" />

          <!-- 视图模式分段控件（macOS segmented control）：灰底容器 +
               白色选中浮块滑动过渡 -->
          <div
            class="seg"
            :class="{'at-grid': ui.viewMode === 'grid'}"
            role="group"
            aria-label="视图模式"
          >
            <span class="seg-thumb" aria-hidden="true"></span>
            <button
              type="button"
              class="seg-btn"
              :class="{on: ui.viewMode === 'list'}"
              :aria-pressed="ui.viewMode === 'list'"
              title="列表视图"
              @click="setViewMode('list')"
            >
              <Icon name="list" :size="16" />
            </button>
            <button
              type="button"
              class="seg-btn"
              :class="{on: ui.viewMode === 'grid'}"
              :aria-pressed="ui.viewMode === 'grid'"
              title="网格视图"
              @click="setViewMode('grid')"
            >
              <Icon name="tiles" :size="16" />
            </button>
          </div>

          <Button
            iconOnly
            :icon="inspectorOn ? 'hide' : 'view'"
            :title="inspectorOn ? '收起预览检查器' : '展开预览检查器（选中文件时自动展开）'"
            @click="toggleInspector"
          />
          <Button iconOnly icon="more" title="更多操作" @click="openBar('more', $event)" />
        </template>
      </PageHeader>

      <!-- 双栏：文件列表 | 预览检查器（目录树已并入外壳侧栏，见 App.vue）。
           展开时**预览是主区**：至少占横向 60%（用户 2026-09-21 明确要求），
           列表让位到 40% 以内；检查器收起时列表吃满整宽——此前它常驻占约
           900px 却只显示空态提示。
           preview-open 供 ≤640px 下把检查器切成全屏浮层（清空选择或进目录时
           listDir 清 ui.sel 会自动收起）。 -->
      <div
        class="files-shell"
        :class="{insp: showInspector, 'preview-open': showInspector}"
        :style="{'--insp-w': inspW + 'px'}"
      >
        <!-- 左栏：文件列表（面包屑已在页头，列表独占纵向空间） -->
        <section class="browse">

          <!-- 条目区：右键空白=上下文菜单；加载/错误/空态分流 -->
          <div
            class="zone"
            @click.self="clearMulti"
            @contextmenu.prevent="openCtx($event, undefined)"
          >
            <!-- 加载 -->
            <div v-if="ui.loading && !ui.entries" class="center">
              <div class="loading-bar"><ProgressBar indeterminate /></div>
              <span class="hint">读取目录中…</span>
            </div>

            <!-- 错误 -->
            <div v-else-if="ui.loadError" class="center">
              <Icon name="cancel" :size="32" class="err-ic" />
              <p class="err-msg">{{ ui.loadError }}</p>
              <Button icon="update" @click="reloadDir">重试</Button>
            </div>

            <!-- 空目录：复用全局空态骨架（layout.css 的 .empty-state + .plate），
                 与「未连接」「无任务」等空态同一套视觉语言 -->
            <div v-else-if="showEmpty" class="empty-state empty-dir">
              <span class="plate"><Icon name="folder" :size="32" /></span>
              <p class="lead">此目录为空</p>
              <p class="sub">点上方「上传」或直接拖入文件以开始</p>
            </div>

            <!-- 网格：96px 缩略图卡 -->
            <div v-else-if="ui.viewMode === 'grid'" class="grid">
              <GridCard
                v-for="e in entries"
                :key="e.remote"
                :entry="e"
                @select="onItemClick"
                @open="enterDir"
                @ctx="onCardCtx"
              />
            </div>

            <!-- 列表：行式（目录行尾提供直达钮） -->
            <div v-else class="list">
              <!-- 行右键要 .stop：祖先 .zone 也挂 contextmenu（空白区菜单），
                   不阻止冒泡会被它覆盖成空白菜单（详见 GridCard.vue 顶部注释）。 -->
              <div
                v-for="e in entries"
                :key="e.remote"
                class="row"
                :class="{sel: isSel(e)}"
                @click="onItemClick($event, e)"
                @dblclick="e.isDir && enterDir(e)"
                @contextmenu.prevent.stop="openCtx($event, e)"
              >
                <!-- 行首勾选：仅多选批量态（≥2 项）显示，单选只靠 .row.sel 高亮 -->
                <Icon v-if="isSel(e) && hasMulti" name="check" :size="16" class="row-check" />
                <Icon :name="kindOfRow(e)" :size="18" class="row-ic" :class="{dir: e.isDir}" />
                <span class="row-name" :title="e.display">{{ e.display }}</span>
                <span class="row-size">{{ e.isDir ? '文件夹' : fmtSize(e.size) }}</span>
                <button
                  type="button"
                  class="more"
                  title="更多操作（删除、重命名、下载…）"
                  @click.stop="openCtx($event, e)"
                >
                  <Icon name="more" :size="16" />
                </button>
                <button
                  v-if="e.isDir"
                  type="button"
                  class="open"
                  title="进入目录"
                  @click.stop="enterDir(e)"
                >
                  <Icon name="chevron_right_med" :size="16" />
                </button>
              </div>
            </div>
          </div>
        </section>

        <!-- 拖柄：仅在检查器占位时存在（列表恒占剩余宽度） -->
        <div
          v-if="showInspector"
          class="split-handle files-split"
          :class="{dragging}"
          role="separator"
          aria-orientation="vertical"
          @pointerdown="splitDown"
        ></div>
        <div v-if="dragging" class="split-mask"></div>

        <!-- 右栏：预览检查器（可收起，选中条目时自动滑出） -->
        <PreviewPanel v-if="showInspector" class="preview" />
      </div>
    </template>

    <!-- 右键上下文菜单（跟随光标弹出） -->
    <RoundMenu
      :open="ctxOpen"
      :position="ctxPos"
      :anchor="ctxAnchor"
      :items="ctxItems"
      @select="onCtxIndex"
      @close="ctxOpen = false"
    />

    <!-- 页头下拉菜单（上传 / 更多）：锚在触发按钮上，条目走同一套分发 -->
    <RoundMenu
      :open="barMenu !== 'none'"
      :anchor="barAnchor"
      :items="barItems"
      @select="onBarIndex"
      @close="barMenu = 'none'"
    />

    <!-- 新建/重命名/删除 模态 -->
    <MessageBox
      :open="dlg.open"
      :title="dlg.title"
      :content="dlg.content"
      :input-label="dlg.inputLabel"
      :initial="dlg.initial"
      :danger="dlg.danger"
      :confirm-text="dlg.danger ? '删除' : '确定'"
      @confirm="confirmDlg"
      @cancel="dlg.open = false"
    />
  </div>
</template>

<style scoped>
.files-view {
  display: flex;
  flex-direction: column;
  height: 100%;
}

/* 文件列表 + （可选）拖柄 + 预览检查器。
   检查器收起时只有一列——列表吃满整宽，这是「可收起检查器」的核心收益：
   此前预览栏常驻 1fr（实测 904px）却只显示一句空态提示。
   .insp 由 showInspector 驱动（模板上绑定），列轨道随之切换。 */
.files-shell {
  flex: 1;
  min-height: 0;
  min-width: 0;
  display: grid;
  grid-template-columns: 1fr;
}

/* 展开态：预览是**主区**，列表让位。
   用户明确要求（2026-09-21）：文件/视频预览至少占横向 60%。所以轨道分配反过来
   —— 列表拿 1fr 的**余量**（上限 40%），预览拿其余全部，并带 60% 硬下限：
     · `minmax(60%, …)` 保证下限：即使 localStorage 里存着旧版钳制的 380px
       （旧实现的 300~640 区间已在 60% 之下），轨道也不会窄于 60%；
     · 上限 min(--insp-w, 容器宽−244px)：244 = 列表保底 240px + 4px 拖柄，
       避免把列表挤成不可浏览。minmax 的 max < min 时按 min 处理，窄窗下仍保 60%。
   · 60% 的下限刻意放在 CSS 而不是只写进 JS：窗口缩放（不触发拖动）时 JS 不会
     重算，只有 CSS 能持续保证「任何时刻都 ≥60%」；JS 那份负责让拖动跟手不越界。 */
.files-shell.insp {
  grid-template-columns:
    minmax(0, 1fr) 4px
    minmax(60%, min(var(--insp-w, 900px), calc(100% - 244px)));
}

/* 页头里的「已选 N 项」摘要：替代原面包屑的位置，右侧带取消钮 */
.sel-summary {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}

.sel-count {
  font-size: 0.929rem;
  font-weight: 600;
  color: var(--accent);
  white-space: nowrap;
}

.sel-clear {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 22px;
  height: 22px;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

.sel-clear:hover {
  background: color-mix(in srgb, var(--text) 8%, transparent);
  color: var(--text);
}

/* 「上传」主按钮尾部的下拉指示：告诉用户点它出菜单而不是直接上传 */
.caret {
  margin-left: 3px;
  opacity: 0.85;
}

/* 行多选勾选（描边勾选框，随 accent 着色） */
.row-check {
  flex: none;
  color: var(--accent);
  margin-right: 2px;
}

/* ---------------- 视图模式分段控件（macOS segmented control） ----------------
   灰底胶囊容器 + 选中浮块（--surface 白浮块）滑动；浮块用 transform 位移
   而非 left，脱离布局流才能有滑动过渡动画。 */
.seg {
  position: relative;
  display: inline-flex;
  gap: 2px;
  height: 32px;
  padding: 2px;
  background: color-mix(in srgb, var(--text) 6%, transparent);
  border-radius: calc(var(--radius-ctrl) + 2px);
}

.seg-thumb {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 28px;
  height: 28px;
  background: var(--surface);
  border-radius: var(--radius-ctrl);
  box-shadow: var(--shadow-1);
  transition: transform var(--dur-fast) var(--ease);
}

/* 浮块滑到第二格：28px 按钮宽 + 2px gap */
.seg.at-grid .seg-thumb {
  transform: translateX(30px);
}

.seg-btn {
  position: relative; /* 压在浮块之上可点 */
  z-index: var(--z-raise);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  transition: color var(--dur-fast) var(--ease);
}

.seg-btn.on {
  color: var(--text);
}

/* ---------------- 浏览区（左栏：文件列表） ---------------- */
.browse {
  grid-column: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  background: var(--bg-page);
}

/* 拖柄（文件列表/预览）：column 2 */
.files-split {
  grid-column: 2;
}

/* 预览检查器（右栏）：column 3。
   PreviewPanel 是子组件，根元素 .cp-preview 的 scope hash 属于 PreviewPanel，
   本组件 scoped 的 .preview 匹配不到 → 必须用 :deep 穿透，否则
   grid-column 不生效、检查器会落到隐式轨道上把栅格撑宽。 */
:deep(.preview) {
  grid-column: 3;
  min-height: 0;
  min-width: 0;
  /* v-if 挂载是瞬时的，给一个淡入避免"啪"一下出现。
     不做宽度/位移过渡：grid 列宽过渡在本机会抖，横向位移还可能引出滚动条。 */
  animation: insp-in var(--dur) var(--ease);
}

@keyframes insp-in {
  from { opacity: 0; }
  to { opacity: 1; }
}

/* 面包屑：根图标 + 明文段（后端 remote 是密文，不可直接展示）。
   现在它住在 56px 页头里，所以不再自带高度与底边分隔线——
   页头已经是那条分隔线，再加一条会出现「双横线」。 */
.crumbs {
  display: flex;
  align-items: center;
  gap: 2px;
  min-width: 0;
  overflow-x: auto;
  scrollbar-width: none;
}

.crumbs::-webkit-scrollbar {
  display: none;
}

.crumb {
  display: inline-flex;
  align-items: center;
  flex: none;
  height: 26px;
  max-width: 180px;
  padding: 0 8px;
  font-family: inherit;
  /* 比正文小一档：面包屑是导航控件而非内容，不该和文件名抢注意力 */
  font-size: 0.857rem;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.crumb:hover {
  background: color-mix(in srgb, var(--text) 8%, transparent);
}

.crumb.on {
  color: var(--accent);
  font-weight: 600;
}

.arrow {
  flex: none;
  color: var(--text2);
  opacity: 0.6;
}

/* ---------------- 条目区 ---------------- */
.zone {
  flex: 1;
  min-height: 0;
  overflow: auto;
  /* 与页头的左右内边距对齐（14px），内容左缘和面包屑/标题在同一条竖线上 */
  padding: 12px 14px;
}

.center {
  height: 100%;
  min-height: 180px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  text-align: center;
}

.loading-bar {
  width: 180px;
}

.hint {
  font-size: 0.857rem;
  color: var(--text2);
}

.err-ic {
  color: var(--err);
}

.err-msg {
  margin: 0;
  font-size: 0.857rem;
  color: var(--err);
  max-width: 420px;
  overflow-wrap: anywhere;
}

/* 空目录态用全局 .empty-state（无 min-height，会在矮列表栏里被压扁），
   这里只补一个最小高度保证「圆盘 + 两行字」始终完整可见 */
.empty-dir {
  min-height: 180px;
}

/* 网格：固定列宽自动换行。列宽是缩略图承托面的唯一宽度来源
   （GridCard 用 width:100% 填满列，不另写尺寸）。
   136px：列表占满整宽后 118px 会排出十几列、卡片小到认不出内容。 */
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, 136px);
  justify-content: start;
  gap: 12px;
}

/* 手机：列宽放大到 168px（136px 在手机上过小，缩略图难辨认） */
@media (max-width: 640px) {
  .grid {
    grid-template-columns: repeat(auto-fill, 168px);
    gap: 10px;
  }
}

/* 列表：行式条目 */
.list {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.row {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 36px;
  padding: 0 10px;
  border-radius: var(--radius-ctrl);
  user-select: none;
}

.row:hover {
  background: color-mix(in srgb, var(--text) 5%, transparent);
}

.row.sel {
  background: var(--accent-soft);
}

/* 行类型图标（裸 Icon，随 accent/text2 着色）；
   overflow visible + border-box 固定尺寸，防浏览器渲染下被裁切 */
.row-ic {
  flex: none;
  color: var(--text2);
  overflow: visible;
  box-sizing: border-box;
}

.row-ic.dir {
  color: var(--accent);
}

.row-name {
  flex: 1;
  min-width: 0;
  font-size: 0.857rem;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.row-size {
  flex: none;
  min-width: 56px;
  font-size: 0.786rem;
  color: var(--text2);
  text-align: right;
}

.open {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 24px;
  height: 24px;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
}

.open:hover {
  background: color-mix(in srgb, var(--text) 8%, transparent);
  color: var(--accent);
}

/* 更多菜单（hover 才显式可见，右键始终可用） */
.more {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 24px;
  height: 24px;
  margin-left: 2px;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  opacity: 0;
  transition: opacity var(--dur-fast) var(--ease), background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

.row:hover .more,
.row.sel .more,
.more:focus-visible {
  opacity: 1;
}

.more:hover {
  background: color-mix(in srgb, var(--text) 8%, transparent);
  color: var(--accent);
}

/* ============================================================ 响应式
 * 桌面：双栏（列表 + 可收起检查器），没有中间断点——目录树已移入外壳侧栏，
 *   侧栏自身的折叠（56px）承担了窄窗下的宽度收缩，本页不再需要三栏→两栏切换。
 * ≤640px：检查器改全屏浮层，选中才浮出；列表独占单栏。
 * 触摸设备：补 hover 缺失导致的入口不可见问题。
 * ============================================================ */

@media (max-width: 640px) {
  .files-shell,
  /* .insp 的选择器权重高于裸类，必须显式重置，否则手机上会残留三列轨道 */
  .files-shell.insp {
    position: relative; /* 浮层的定位上下文 */
    grid-template-columns: 1fr;
  }

  .browse {
    grid-column: 1;
  }

  /* 单栏下拖柄无意义 */
  .files-split {
    display: none;
  }

  /* 检查器默认不占位；选中后浮出覆盖列表区。
     只盖 .files-shell 而非整页，页头仍可见可点，避免"进去出不来"。 */
  :deep(.preview) {
    display: none;
  }

  .files-shell.preview-open :deep(.preview) {
    display: flex;
    position: absolute;
    inset: 0;
    z-index: var(--z-sheet);
    background: var(--surface);
  }

  /* 触摸目标放大：行 36→48px，行尾钮 24→40px */
  .row {
    height: 48px;
  }

  .open,
  .more {
    width: 40px;
    height: 40px;
  }

  .row-size {
    min-width: 44px;
  }

  /* 面包屑放大，便于手指点按逐级回退 */
  .crumb {
    height: 32px;
    max-width: 130px;
  }
}

/* 触摸设备（无 hover）：行尾「更多」钮原为 hover 才显形，触摸下会永久隐形，
   导致手机上没有改名/删除入口 → 常显。粗指针判定同时排除触屏笔记本。 */
@media (hover: none) and (pointer: coarse) {
  .more {
    opacity: 1;
  }
}

</style>
