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
  goUp,
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
import Icon from '../components/fluent/Icon.vue'
import RoundMenu from '../components/fluent/RoundMenu.vue'
import MessageBox from '../components/fluent/MessageBox.vue'
import ProgressBar from '../components/fluent/ProgressBar.vue'
import GridCard from './GridCard.vue'
import PreviewPanel from './PreviewPanel.vue'
import DirTree from '../components/DirTree.vue'

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

/* -------------------------------------------------------- Splitter（三栏两个拖柄） */

const SPLIT_KEY = 'cp-split-l'
const TREE_KEY = 'cp-tree-w'
const viewEl = ref<HTMLElement>()
// 文件列表宽度（中栏，窄默认 280px，用户要列表窄 + 预览大头）
const splitL = ref(Number(localStorage.getItem(SPLIT_KEY)) || 280)
// 目录树宽度（左栏，默认 200px）
const treeW = ref(Number(localStorage.getItem(TREE_KEY)) || 200)
const dragging = ref(false)
const draggingTree = ref(false)

// 拖柄1：目录树/文件列表
function splitTreeDown(e: PointerEvent) {
  e.preventDefault()
  draggingTree.value = true
  window.addEventListener('pointermove', splitTreeMove)
  window.addEventListener('pointerup', splitTreeEnd, {once: true})
}

function splitTreeMove(e: PointerEvent) {
  const left = viewEl.value!.getBoundingClientRect().left
  // 目录树宽度钳制：140px ~ 320px
  treeW.value = Math.min(Math.max(e.clientX - left, 140), 320)
}

function splitTreeEnd() {
  draggingTree.value = false
  window.removeEventListener('pointermove', splitTreeMove)
  localStorage.setItem(TREE_KEY, String(treeW.value))
}

// 拖柄2：文件列表/预览（拖动调的是中栏文件列表宽度）
function splitDown(e: PointerEvent) {
  e.preventDefault()
  dragging.value = true
  window.addEventListener('pointermove', splitMove)
  window.addEventListener('pointerup', splitEnd, {once: true})
}

function splitMove(e: PointerEvent) {
  // 中栏左缘 = 目录树宽 + 拖柄1宽(4px) + 左栏内边距偏移
  const left = viewEl.value!.getBoundingClientRect().left + treeW.value + 4
  // 中栏宽度钳制：180px（文件名可见）~ 窗口宽 50%（预览至少占一半）
  const max = Math.max(180, window.innerWidth * 0.5 - left)
  splitL.value = Math.min(Math.max(e.clientX - left, 180), max)
}

function splitEnd() {
  dragging.value = false
  window.removeEventListener('pointermove', splitMove)
  localStorage.setItem(SPLIT_KEY, String(splitL.value))
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
      <!-- 工具行：导航/动作/视图模式 -->
      <div class="cp-toolbar">
        <Button
          iconOnly
          icon="up"
          title="返回上级"
          :disabled="!crumbs.length || ui.loading"
          @click="goUp"
        />
        <Button iconOnly icon="update" title="刷新（F5）" @click="reloadDir" />
        <span class="sep" />
        <Button iconOnly icon="folder_add" title="新建文件夹" @click="openMsg('newFolder')" />
        <Button iconOnly icon="send" title="上传文件到当前目录" @click="pickUpload()" />
        <Button
          iconOnly
          icon="folder-up"
          title="上传文件夹到当前目录（保留目录结构）"
          @click="pickUpload(ui.remote, true)"
        />
        <Button
          iconOnly
          icon="download"
          :title="hasMulti ? `下载所选 ${multiSel.length} 项` : '下载选中项'"
          :disabled="!multiSel.length"
          @click="downloadSel"
        />
        <Button
          iconOnly
          icon="share"
          :title="hasMulti ? `导出所选 ${multiSel.length} 项` : '导出选中文件'"
          :disabled="!multiSel.length"
          @click="exportSel"
        />
        <span class="spacer" />
        <Button
          v-if="multiSel.length > 1"
          iconOnly
          icon="cancel"
          title="取消多选"
          @click="clearMulti"
        />
        <span class="mode">
          <Button
            iconOnly
            icon="list"
            class="toolbar-mode"
            :class="{on: ui.viewMode === 'list'}"
            title="列表视图"
            @click="setViewMode('list')"
          />
          <Button
            iconOnly
            icon="tiles"
            class="toolbar-mode"
            :class="{on: ui.viewMode === 'grid'}"
            title="网格视图"
            @click="setViewMode('grid')"
          />
        </span>
      </div>

      <!-- 多选批量条：>1 项时展示，一键下载/导出/删除/取消 -->
      <Transition name="fade">
        <div v-if="hasMulti" class="multi-bar">
          <Icon name="check" :size="15" class="mb-check" />
          <span class="mb-text">已选 {{ multiSel.length }} 项</span>
          <span class="mb-actions">
            <Button icon="download" :disabled="!connected" @click="downloadSel">下载</Button>
            <Button icon="share" :disabled="!connected" @click="exportSel">导出</Button>
            <Button icon="delete" danger @click="openMsg('delete')">删除</Button>
            <Button icon="cancel" @click="clearMulti">取消</Button>
          </span>
        </div>
      </Transition>

      <!-- 三栏：目录树抽屉 | 文件列表 | 预览（大头，E 方案双栏抽屉）。
           preview-open 供 ≤640px 下把预览切成全屏浮层（选中即浮出，
           清空选择或进入目录时 listDir 清 ui.sel 自动收起）。 -->
      <div
        class="files-shell"
        :class="{'preview-open': !!ui.sel}"
        :style="{'--tree-w': treeW + 'px', '--split-l': splitL + 'px'}"
      >
        <!-- 左栏：目录树（懒加载，展开时拉子目录） -->
        <aside class="dirtree-col">
          <DirTree />
        </aside>

        <!-- 拖柄1：目录树/文件列表 -->
        <div
          class="split-handle tree-split"
          :class="{dragging: draggingTree}"
          role="separator"
          aria-orientation="vertical"
          @pointerdown="splitTreeDown"
        ></div>

        <!-- 中栏：面包屑 + 文件列表（窄） -->
        <section class="browse">
          <nav class="crumbs" aria-label="路径">
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

            <!-- 空目录 -->
            <div v-else-if="showEmpty" class="center">
              <Icon name="folder" :size="40" class="dim" />
              <p class="lead2">此目录为空</p>
              <p class="hint">点上方「上传」或直接拖入文件以开始</p>
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
              <div
                v-for="e in entries"
                :key="e.remote"
                class="row"
                :class="{sel: isSel(e)}"
                @click="onItemClick($event, e)"
                @dblclick="e.isDir && enterDir(e)"
                @contextmenu.prevent="openCtx($event, e)"
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

        <!-- 拖柄2：文件列表/预览（独立类 files-split，避免与 tree-split 共用
             split-handle 类导致 grid-column 权重冲突） -->
        <div
          class="split-handle files-split"
          :class="{dragging}"
          role="separator"
          aria-orientation="vertical"
          @pointerdown="splitDown"
        ></div>
        <div v-if="dragging || draggingTree" class="split-mask"></div>

        <!-- 右栏：预览面板（占大头） -->
        <PreviewPanel class="preview" />
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

/* 三栏（E 方案双栏抽屉）：目录树 | 拖柄1 | 文件列表 | 拖柄2 | 预览（大头）
   覆盖 layout.css 的全局双栏规则，显式设三栏 grid */
.files-shell {
  flex: 1;
  min-height: 0;
  min-width: 0;
  display: grid;
  grid-template-columns: var(--tree-w, 200px) 4px var(--split-l, 280px) 4px 1fr;
}

/* 左栏：目录树抽屉 */
.dirtree-col {
  grid-column: 1;
  min-height: 0;
  overflow: hidden;
}

.dim {
  color: var(--text2);
}

/* 行多选勾选（lucide square-check：描边勾选框，随 accent 着色） */
.row-check {
  flex: none;
  color: var(--accent);
  margin-right: 2px;
}

/* ---------------- 多选批量条（>1 项时出现） ---------------- */
.multi-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 40px;
  padding: 0 14px;
  margin: 0 12px 8px;
  background: var(--surface);
  border: 1px solid var(--stroke-card);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow-card);
}

.mb-check {
  color: var(--accent);
}

.mb-text {
  font-size: 0.857rem;
  font-weight: 600;
  color: var(--heading);
}

.mb-actions {
  display: inline-flex;
  gap: 8px;
  margin-left: auto;
}

/* ---------------- 模式钮组（view/tiles 二选一） ---------------- */
.mode {
  display: inline-flex;
  gap: 2px;
}

/* ---------------- 浏览区（三栏中栏：文件列表） ---------------- */
.browse {
  grid-column: 3;
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  background: var(--bg-page);
}

/* 拖柄1（目录树/文件列表）：column 2 */
.tree-split {
  grid-column: 2;
}

/* 拖柄2（文件列表/预览）：column 4。
   拖柄2 用独立类 files-split（不与 tree-split 共用 split-handle 做 grid 定位），
   避免高权重选择器把 tree-split 从 column 2 误拉到 4 导致两行阶梯。 */
.files-split {
  grid-column: 4;
}

/* 预览面板（三栏右栏，占大头 1fr）：column 5。
   PreviewPanel 是子组件，根元素 .cp-preview 的 scope hash 属于 PreviewPanel，
   本组件 scoped 的 .preview 匹配不到 → 必须用 :deep 穿透，否则
   grid-column:5 不生效、预览栏溢出到下一行造成三栏"阶梯"错位。 */
:deep(.preview) {
  grid-column: 5;
  min-height: 0;
  min-width: 0;
}

/* 面包屑：根图标 + 明文段（后端 remote 是密文，不可直接展示） */
.crumbs {
  display: flex;
  align-items: center;
  gap: 2px;
  height: 36px;
  padding: 0 8px;
  overflow-x: auto;
  flex: none;
  border-bottom: 1px solid var(--divider);
}

.crumb {
  display: inline-flex;
  align-items: center;
  flex: none;
  height: 26px;
  max-width: 180px;
  padding: 0 8px;
  font-family: inherit;
  font-size: 0.786rem;
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
  padding: 10px;
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

.lead2 {
  margin: 4px 0 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--heading);
}

/* 网格：固定列宽自动换行。列宽是缩略图承托面的唯一宽度来源
   （GridCard 用 width:100% 填满列，不另写尺寸）。 */
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, 118px);
  justify-content: center;
  gap: 10px;
}

/* 手机：列宽放大到 156px（118px 在手机上过小，缩略图难辨认） */
@media (max-width: 640px) {
  .grid {
    grid-template-columns: repeat(auto-fill, 156px);
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
 * ≤900px：目录树列收起，三栏 → 两栏（列表 | 预览）。
 *   依据：三栏可用下限 ≈ 树 140 + 柄 4 + 列表 180 + 柄 4 + 预览 300 = 628px，
 *   再叠加导航轨 48px 后低于 900px 预览已被挤到不可用。目录树的导航职能由
 *   面包屑（可逐级回退）与列表行尾「进入目录」钮承接。
 * ≤640px：预览改全屏浮层，选中才浮出；列表独占单栏。
 * 触摸设备：补 hover 缺失导致的入口不可见问题。
 * ============================================================ */

@media (max-width: 900px) {
  /* 仍是三轨但去掉树列。必须重排 grid-column：原 column 3/5 的 .browse 与
     .preview 若不动，会落到隐式轨道上把栅格撑宽。 */
  .files-shell {
    grid-template-columns: var(--split-l, 280px) 4px 1fr;
  }

  .dirtree-col,
  .tree-split {
    display: none;
  }

  .browse {
    grid-column: 1;
  }

  .files-split {
    grid-column: 2;
  }

  :deep(.preview) {
    grid-column: 3;
  }
}

@media (max-width: 640px) {
  .files-shell {
    position: relative; /* 预览浮层的定位上下文 */
    grid-template-columns: 1fr;
  }

  .browse {
    grid-column: 1;
  }

  /* 单栏下拖柄无意义 */
  .files-split {
    display: none;
  }

  /* 预览默认不占位；选中后浮出覆盖列表区。
     只盖 .files-shell 而非整页，工具栏与面包屑仍可见可点，避免"进去出不来"。 */
  :deep(.preview) {
    display: none;
  }

  .files-shell.preview-open :deep(.preview) {
    display: flex;
    position: absolute;
    inset: 0;
    z-index: 620;
    background: var(--bg-page);
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

  /* 面包屑同步放大，便于手指点按逐级回退 */
  .crumbs {
    height: 44px;
  }

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
