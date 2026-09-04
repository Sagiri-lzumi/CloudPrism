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
const showEmpty = computed(
  () => !ui.loading && !ui.loadError && ui.entries !== null && ui.entries.length === 0,
)

function isSel(e: appstate.FileEntry): boolean {
  return ui.sel?.remote === e.remote
}

function kindOfRow(e: appstate.FileEntry): string {
  return e.isDir ? 'folder' : KIND_ICON[kindOf(e.display)]
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

/* --------------------------------------------------------- 右键菜单 */

// 菜单 items 按目标动态生成，索引即动作：blank=无目标、dir=目录、file=文件
interface CtxItem {
  label?: string
  icon?: string
  divider?: boolean
  danger?: boolean
}

const ctxOpen = ref(false)
const ctxAnchor = ref<HTMLElement | null>(null)
const ctxEntry = ref<appstate.FileEntry | null>(null)

const ctxItems = computed<CtxItem[]>(() => {
  const e = ctxEntry.value
  if (!e)
    return [
      {label: '新建文件夹', icon: 'folder_add'},
      {label: '上传文件…', icon: 'send'},
      {divider: true},
      {label: '刷新', icon: 'update'},
    ]
  if (e.isDir)
    return [
      {label: '新建子文件夹', icon: 'folder_add'},
      {label: '上传到此目录', icon: 'send'},
      {divider: true},
      {label: '下载到本地', icon: 'download'},
      {divider: true},
      {label: '重命名', icon: 'edit'},
      {label: '删除', icon: 'delete', danger: true},
    ]
  return [
    {label: '下载到本地', icon: 'download'},
    {label: '解密导出', icon: 'share'},
    {divider: true},
    {label: '重命名', icon: 'edit'},
    {label: '删除', icon: 'delete', danger: true},
  ]
})

function openCtx(e: MouseEvent, entry: appstate.FileEntry | null) {
  // 右键目标即时选中（菜单动作都以选中条目为对象）
  if (entry) selectEntry(entry)
  ctxEntry.value = entry
  // 锚点取 currentTarget；GridCard 走自定义事件 emit 时 currentTarget 已被
  // 置空（原生事件派发结束），回退到 target（点击/右键命中的元素恒存在）
  const el = (e.currentTarget ?? e.target) as HTMLElement | null
  ctxAnchor.value = el
  ctxOpen.value = false
  ctxOpen.value = true
}

async function onCtx(i: number) {
  const e = ctxEntry.value
  if (!e) {
    if (i === 0) openMsg('newFolder')
    else if (i === 1) void pickUpload()
    else if (i === 3) reloadDir()
    return
  }
  if (e.isDir) {
    if (i === 0) openMsg('newSubFolder')
    else if (i === 1) void pickUpload(e.remote)
    else if (i === 3) void downloadSel()
    else if (i === 5) openMsg('rename')
    else if (i === 6) openMsg('delete')
    return
  }
  if (i === 0) void downloadSel()
  else if (i === 1) void exportSel()
  else if (i === 3) openMsg('rename')
  else if (i === 4) openMsg('delete')
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
    dlg.title = '删除确认'
    dlg.inputLabel = ''
    dlg.initial = ''
    dlg.danger = true
    dlg.content = e?.isDir
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
          icon="download"
          title="下载选中项"
          :disabled="!ui.sel"
          @click="downloadSel"
        />
        <Button
          iconOnly
          icon="share"
          title="解密导出选中文件"
          :disabled="!ui.sel || !!ui.sel.isDir"
          @click="exportSel"
        />
        <span class="spacer" />
        <span class="mode">
          <Button
            iconOnly
            icon="view"
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

      <!-- 双栏：浏览（面包屑+条目） | Splitter | 预览 -->
      <div class="files-shell" :style="{'--split-l': splitL + 'px'}">
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
            @contextmenu.prevent="openCtx($event, null)"
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
                @select="selectEntry"
                @open="enterDir"
                @ctx="openCtx"
              />
            </div>

            <!-- 列表：行式（目录行尾提供直达钮） -->
            <div v-else class="list">
              <div
                v-for="e in entries"
                :key="e.remote"
                class="row"
                :class="{sel: isSel(e)}"
                @click="selectEntry(e)"
                @dblclick="e.isDir && enterDir(e)"
                @contextmenu.prevent="openCtx($event, e)"
              >
                <Icon :name="kindOfRow(e)" :size="16" class="row-ic" :class="{dir: e.isDir}" />
                <span class="row-name" :title="e.display">{{ e.display }}</span>
                <span class="row-size">{{ e.isDir ? '文件夹' : fmtSize(e.size) }}</span>
                <button
                  type="button"
                  class="more"
                  title="更多操作（删除、重命名、下载…）"
                  @click.stop="openCtx($event, e)"
                >
                  <Icon name="more" :size="14" />
                </button>
                <button
                  v-if="e.isDir"
                  type="button"
                  class="open"
                  title="进入目录"
                  @click.stop="enterDir(e)"
                >
                  <Icon name="chevron_right_med" :size="14" />
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

    <!-- 右键上下文菜单 -->
    <RoundMenu
      :open="ctxOpen"
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

.dim {
  color: var(--text2);
}

/* ---------------- 模式钮组（view/tiles 二选一） ---------------- */
.mode {
  display: inline-flex;
  gap: 2px;
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

/* 网格：固定 96px 卡片自动换行 */
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, 96px);
  justify-content: center;
  gap: 6px;
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
  height: 34px;
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

.row-ic {
  flex: none;
  color: var(--text2);
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
</style>
