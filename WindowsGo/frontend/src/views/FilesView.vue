<!--
  FilesView.vue —— 文件浏览页（工具栏 + 面包屑 + 网格/列表 + 预览双栏）。
  对照 WindowsPy FilesPage/FileTreeView：当前目录语义由 store 驱动，
  网格卡片缩略图走 /t/ 令牌（GridCard），预览面板在 Splitter 右栏。
  交互：单击选中 → 预览面板即时更新；双击目录进入（明文链压栈）；
  右键目标条弹动作菜单；工具栏模式钮切换 list/grid（qfw 双视图语义）。
-->
<script setup lang="ts">
import {computed, reactive, ref} from 'vue'
import type {appstate} from '../../wailsjs/go/models'
import {Transfer} from '../lib/api'
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
  uploadPaths,
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

/** 行类型彩色块类：目录/类别 → t-*（网格卡/列表行彩色图标底共用口径） */
function rowTypeClass(e: appstate.FileEntry): string {
  return e.isDir ? 't-dir' : `t-${kindOf(e.display)}`
}

/* -------------------------------------------------- 点击/多选交互 */

// shift 连选锚点：最近一次「普通单击」的主条目 remote（无则取列表首项）
const shiftAnchor = ref<string | null>(null)

function onItemClick(ev: MouseEvent, e: appstate.FileEntry) {
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

/* -------------------------------------------------------- Splitter */

const SPLIT_KEY = 'cp-split-l'
const viewEl = ref<HTMLElement>()
const splitL = ref(Number(localStorage.getItem(SPLIT_KEY)) || 400)
const dragging = ref(false)

function splitDown(e: PointerEvent) {
  e.preventDefault()
  dragging.value = true
  window.addEventListener('pointermove', splitMove)
  window.addEventListener('pointerup', splitEnd, {once: true})
}

function splitMove(e: PointerEvent) {
  const left = viewEl.value!.getBoundingClientRect().left
  // 浏览区宽度钳制：240px（图标列可见）~ 窗口宽 3/4
  const max = Math.max(240, window.innerWidth * 0.75 - left)
  splitL.value = Math.min(Math.max(e.clientX - left, 240), max)
}

function splitEnd() {
  dragging.value = false
  window.removeEventListener('pointermove', splitMove)
  localStorage.setItem(SPLIT_KEY, String(splitL.value))
}

/* --------------------------------------------------------- 右键/更多菜单 */

// 菜单 items 是**唯一事实源**：右键、列表行 ⋯、网格卡 ⋯ 三条入口
// 全部经由 openCtx → ctxItems 取同一份内容，保证任意入口弹出的菜单一致。
// 目标类型优先级：multi=多选批量（目标 ∈ 当前多选集且 >1 项）→
//   blank=无目标（空白区）→ dir=目录 → file=文件
interface CtxItem {
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

/** 构建目标上下文菜单项（索引含分隔线占位，onCtx 按下标分发，两者必须同步改）。 */
function buildCtxItems(): CtxItem[] {
  const e = ctxEntry.value
  if (ctxMulti.value && e) {
    // 多选批量菜单：精简版（动词领先 + 数量括号；onCtx 索引 0/1/3/4 不变）
    const n = ui.multi.length
    return [
      {label: `下载 (${n})`, icon: 'download'},
      {label: `导出 (${n})`, icon: 'share'},
      {divider: true},
      {label: `删除 (${n})`, icon: 'delete', danger: true},
      {label: '取消选择', icon: 'cancel'},
    ]
  }
  if (!e)
    return [
      {label: '新建文件夹', icon: 'folder_add'},
      {label: '上传文件…', icon: 'send'},
      {divider: true},
      {label: '刷新', icon: 'update'},
    ]
  if (e.isDir)
    return [
      {label: '打开', icon: 'folder'},
      {label: '新建子文件夹', icon: 'folder_add'},
      {label: '上传', icon: 'send'},
      {divider: true},
      {label: '下载', icon: 'download'},
      {divider: true},
      {label: '复制路径', icon: 'copy'},
      {divider: true},
      {label: '重命名', icon: 'edit'},
      {label: '删除', icon: 'delete', danger: true},
    ]
  return [
    {label: '打开预览', icon: 'photo'},
    {label: '下载', icon: 'download'},
    {label: '导出', icon: 'share'},
    {divider: true},
    {label: '复制名称', icon: 'copy'},
    {label: '复制路径', icon: 'copy'},
    {divider: true},
    {label: '重命名', icon: 'edit'},
    {label: '删除', icon: 'delete', danger: true},
  ]
}

// 响应式取用单一事实源；任何入口改动 ctxEntry 都会经此重算菜单内容
const ctxItems = computed<CtxItem[]>(() => buildCtxItems())

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

// 菜单项分发：索引 = ctxItems 数组下标（分隔线占位占下标，勿按视觉顺序改）
//   批量 0下载 1导出 3删除 4取消；空白 0新建 1上传 3刷新
//   目录 0打开 1建子夹 2上传 4下载 6复制路径 8重命名 9删除
//   文件 0预览 1下载 2导出 4复制名 5复制路径 7重命名 8删除
async function onCtx(i: number) {
  const e = ctxEntry.value
  // —— 多选批量菜单 ——
  if (ctxMulti.value && e) {
    if (i === 0) void downloadSel() // 批量下载
    else if (i === 1) void exportSel() // 批量导出
    else if (i === 3) openMsg('delete') // 批量删除（危险确认）
    else if (i === 4) clearMulti() // 取消选择
    return
  }
  if (!e) {
    if (i === 0) openMsg('newFolder')
    else if (i === 1) void pickUpload()
    else if (i === 3) reloadDir()
    return
  }
  if (e.isDir) {
    if (i === 0) enterDir(e)
    else if (i === 1) openMsg('newSubFolder')
    else if (i === 2) void pickUpload(e.remote)
    else if (i === 4) void downloadSel()
    else if (i === 6) void copyEntryPath(e)
    else if (i === 8) openMsg('rename')
    else if (i === 9) openMsg('delete')
    return
  }
  if (i === 0) {
    selectEntry(e) // 已在集合/主条目，预览即打开
  } else if (i === 1) void downloadSel()
  else if (i === 2) void exportSel()
  else if (i === 4) void copyEntryName(e)
  else if (i === 5) void copyEntryPath(e)
  else if (i === 7) openMsg('rename')
  else if (i === 8) openMsg('delete')
}

/* ------------------------------------------------- 上传对话框（原生） */

async function pickUpload(remoteDir: string = ui.remote) {
  try {
    const paths = await Transfer.UploadDialog()
    if (paths && paths.length) void uploadPaths(paths, remoteDir)
  } catch {
    /* 用户取消对话框时不提示 */
  }
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
  <div ref="viewEl" class="files-view">
    <!-- 未连接：引导回密库页（锁库事件后兜底） -->
    <div v-if="!connected" class="empty-state">
      <Icon name="folder" :size="40" class="dim" />
      <p class="lead">尚未连接密库</p>
      <p class="sub">连接后即可浏览文件。锁库或断开后浏览状态会重置。</p>
      <Button icon="certificate" @click="navigate('vaults')">前往连接</Button>
    </div>

    <template v-else>
      <!-- 页头：大标题 + 面包屑 + 主操作（v21 重排：替代原 cp-toolbar） -->
      <div class="page-head">
        <Button
          iconOnly
          icon="up"
          title="返回上级"
          :disabled="!crumbs.length || ui.loading"
          @click="goUp"
        />
        <span class="page-title">我的密库</span>
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
        <div class="head-actions">
          <Button iconOnly icon="update" title="刷新（F5）" @click="reloadDir" />
          <Button iconOnly icon="folder_add" title="新建文件夹" @click="openMsg('newFolder')" />
          <Button icon="send" @click="pickUpload()">上传</Button>
          <Button
            icon="download"
            :disabled="!multiSel.length"
            @click="downloadSel"
          >下载</Button>
          <Button
            icon="share"
            :disabled="!multiSel.length"
            @click="exportSel"
          >导出</Button>
          <span class="view-toggle">
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

      <!-- 双栏：浏览（条目区） | Splitter | 预览 -->
      <div class="files-shell" :style="{'--split-l': splitL + 'px'}">
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
                <span class="row-ic" :class="rowTypeClass(e)">
                  <Icon :name="kindOfRow(e)" :size="16" />
                </span>
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

        <!-- 拖柄：hover/拖动高亮，宽度记忆 localStorage -->
        <div
          class="split-handle"
          :class="{dragging}"
          role="separator"
          aria-orientation="vertical"
          @pointerdown="splitDown"
        ></div>
        <div v-if="dragging" class="split-mask"></div>

        <PreviewPanel class="preview" />
      </div>
    </template>

    <!-- 右键上下文菜单（跟随光标弹出） -->
    <RoundMenu
      :open="ctxOpen"
      :position="ctxPos"
      :anchor="ctxAnchor"
      :items="ctxItems"
      @select="onCtx"
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

/* 双栏（grid 骨架在 layout.css）：作为 flex 子项用 flex 拉伸，勿再用 height:100%，
   否则多选批量条出现时内容区会被顶出视口 */
.files-shell {
  flex: 1;
  min-height: 0;
  min-width: 0;
}

.dim {
  color: var(--text2);
}

/* ---------------- 页头（v21 重排：大标题 + 面包屑 + 主操作） ---------------- */
.page-title {
  font-size: 1.05rem;
  font-weight: 700;
  color: var(--heading);
  white-space: nowrap;
  flex: none;
}

.crumbs {
  display: flex;
  align-items: center;
  gap: 2px;
  flex: 1;
  min-width: 0;
  overflow-x: auto;
  scrollbar-width: thin;
}

.crumb {
  display: inline-flex;
  align-items: center;
  flex: none;
  height: 28px;
  max-width: 200px;
  padding: 0 8px;
  font-family: inherit;
  font-size: 0.82rem;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

.crumb:hover {
  background: var(--surface-hover);
  color: var(--text);
}

.crumb.on {
  color: var(--accent);
  font-weight: 600;
}

.arrow {
  flex: none;
  color: var(--text2);
  opacity: 0.5;
}

.head-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: none;
}

/* 视图切换钮组：胶囊容器 */
.view-toggle {
  display: inline-flex;
  gap: 2px;
  padding: 3px;
  background: var(--fill-quiet);
  border-radius: var(--radius-round);
}

.view-toggle :deep(.toolbar-mode) {
  height: 28px;
  width: 28px;
  min-width: 28px;
  color: var(--text2);
  background: transparent;
  border: none;
  box-shadow: none;
}

.view-toggle :deep(.toolbar-mode.on) {
  background: var(--surface);
  color: var(--accent);
  box-shadow: var(--shadow-card);
}

/* ---------------- 多选批量条（>1 项时出现） ---------------- */
.multi-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 48px;
  padding: 0 18px;
  margin: 0 16px 8px;
  background: var(--surface);
  border: 1px solid var(--stroke-card);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow-card);
}

.mb-check {
  color: var(--accent);
}

.mb-text {
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--heading);
}

.mb-actions {
  display: inline-flex;
  gap: 8px;
  margin-left: auto;
}

/* ---------------- 浏览区（files-shell 左栏） ---------------- */
.browse {
  grid-column: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  background: var(--bg-page);
}

/* ---------------- 条目区 ---------------- */
.zone {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: 18px;
}

.center {
  height: 100%;
  min-height: 180px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  text-align: center;
}

.loading-bar {
  width: 200px;
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

/* 网格：132px 卡片自动换行（v21 加大）；gap 18px 容纳 hover 浮起投影不盖邻卡 */
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, 132px);
  justify-content: start;
  gap: 18px;
}

/* 列表：行式条目 */
.list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.row {
  display: flex;
  align-items: center;
  gap: 12px;
  height: 48px;
  padding: 0 14px;
  border-radius: var(--radius-ctrl);
  user-select: none;
  transition: background var(--dur-fast) var(--ease);
}

.row:hover {
  background: var(--surface-hover);
}

.row.sel {
  background: var(--accent-soft);
}

/* 行多选勾选（随 accent 着色） */
.row-check {
  flex: none;
  color: var(--accent);
}

/* 行类型图标块：彩色渐变底（与网格卡一致）；
   overflow visible + border-box 固定尺寸，防 WebView2 渲染下被裁切 */
.row-ic {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  box-sizing: border-box;
  overflow: visible;
  color: #fff;
  border-radius: 8px;
}

/* Icon span 也固定尺寸，防 svg 撑出 span 边界 */
.row-ic :deep(.fluent-icon) {
  width: 16px;
  height: 16px;
  overflow: visible;
}

.row-ic.t-image { background: var(--type-image); }
.row-ic.t-video { background: var(--type-video); }
.row-ic.t-audio { background: var(--type-audio); }
.row-ic.t-text  { background: var(--type-text); }
.row-ic.t-dir   { background: var(--type-dir); }
.row-ic.t-other { background: var(--type-other); }

.row-name {
  flex: 1;
  min-width: 0;
  font-size: 0.88rem;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.row-size {
  flex: none;
  min-width: 60px;
  font-size: 0.78rem;
  color: var(--text2);
  text-align: right;
}

.open {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 28px;
  height: 28px;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: 50%;
  opacity: 0;
  transition: opacity var(--dur-fast) var(--ease), background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

.row:hover .open,
.row.sel .open,
.open:focus-visible {
  opacity: 1;
}

.open:hover {
  background: var(--surface-hover);
  color: var(--accent);
}

/* 更多菜单（hover 才显式可见，右键始终可用） */
.more {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 28px;
  height: 28px;
  margin-left: 2px;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: 50%;
  opacity: 0;
  transition: opacity var(--dur-fast) var(--ease), background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

.row:hover .more,
.row.sel .more,
.more:focus-visible {
  opacity: 1;
}

.more:hover {
  background: var(--surface-hover);
  color: var(--accent);
}
</style>
