<!--
  FolderPicker.vue —— 网页版本机目录选择器（替代主机原生 IFileOpenDialog）。

  为什么不是浏览器原生选择器：`<input type="file" webkitdirectory>` 只提供
  `webkitRelativePath`（所选根目录**之下**的相对路径），File 对象也没有任何
  路径属性 —— 浏览器出于安全刻意不暴露绝对路径。而密库存放目录 / 同步目录 /
  缓存目录 / 导出位置要的恰恰是绝对路径。所以改成：后端列目录（/api/fs/*，
  见 lib/api.ts 的 LocalFS 域），网页渲染选择器，选完把绝对路径回传。

  为什么必须替换掉原生对话框：旧的 win.PickFolder 走 IFileOpenDialog，而
  Show(owner=0) 没有属主窗口，对话框会跑到浏览器窗口后面 —— 用户看到的是
  「后台莫名跳出个框」，点不到、也关不掉。

  交互约定（对齐资源管理器直觉，但只保留必要动作）：
    · 单击文件夹 = 进入（没有「选中但不进入」的中间态，页脚始终显示真正会被
      回传的那个目录，不会出现「看着选 A、其实回传 B」）；
    · 路径框可直接输入并回车跳转（网络路径 \\NAS\share 也能直接进）；
    · 隐藏/系统目录默认折叠（$RECYCLE.BIN 这类），可勾选展开；
    · 新建文件夹在当前位置创建并进入（用 LineEdit 自带的 enter 事件，Esc 只
      收起这一行，不会一路关掉整个选择器 —— 靠 .stop 拦在 window 之前）。
-->
<script setup lang="ts">
import {computed, ref, watch} from 'vue'
import {LocalFS, unwrap, type LocalDirEntry, type LocalDrive, type LocalListing} from '../../lib/api'
import ModalShell from './ModalShell.vue'
import Button from '../fluent/Button.vue'
import PrimaryButton from '../fluent/PrimaryButton.vue'
import Icon from '../fluent/Icon.vue'
import LineEdit from '../fluent/LineEdit.vue'
import Checkbox from '../fluent/Checkbox.vue'

const props = defineProps<{
  open: boolean
  title: string
  /** 起始目录；空串 = 从「此电脑」（盘符列表）开始 */
  start?: string
}>()

const emit = defineEmits<{
  confirm: [path: string]
  cancel: []
}>()

/* --------------------------------------------------------------- 状态 */

/** 路径输入框内容（用户可编辑；与 listing.path 分开，未确认的输入不参与回传） */
const cur = ref('')
/** 当前已加载的目录（唯一权威：只有它可能是回传值） */
const listing = ref<LocalListing | null>(null)
const busy = ref(false)
/** 读目录失败的原因（就地展示，不弹 toast：选择器是模态，报错属于它自己的一部分） */
const err = ref('')

const showHidden = ref(false)

/** 进入根视图（此电脑）的语义：path === '' */
const atRoot = computed(() => !listing.value?.path)
const visibleDirs = computed<LocalDirEntry[]>(() =>
  (listing.value?.dirs ?? []).filter((d) => showHidden.value || !d.hidden),
)
const hiddenCount = computed(() => (listing.value?.dirs ?? []).filter((d) => d.hidden).length)

/** 只有确认存在过的目录才可回传（输入框里的草稿不算）。 */
const canConfirm = computed(() => !!listing.value && !!listing.value.path && !busy.value)

/* ------------------------------------------------------------- 加载 */

async function load(path: string) {
  if (busy.value) return
  busy.value = true
  err.value = ''
  try {
    listing.value = path ? await LocalFS.ListDir(path) : await LocalFS.Drives()
    cur.value = listing.value.path
  } catch (e) {
    const msg = unwrap(e).message
    // 起始目录可能已失效（上次记住的导出目录被删、网络盘断开）——
    // 退回「此电脑」并把原因如实说出，而不是静默跳走让用户以为点错了。
    if (path) {
      try {
        listing.value = await LocalFS.Drives()
        cur.value = ''
      } catch {
        listing.value = null
      }
    }
    err.value = msg
  } finally {
    busy.value = false
  }
}

// 打开即加载起始目录。用 immediate 无意义（组件常驻，靠 open 变化驱动）。
watch(
  () => props.open,
  (v) => {
    if (!v) return
    showHidden.value = false
    mkdirOpen.value = false
    void load(props.start ?? '')
  },
)

/* --------------------------------------------------------- 新建文件夹 */

const mkdirOpen = ref(false)
const mkdirName = ref('')
const mkdirBusy = ref(false)

function openMkdir() {
  mkdirName.value = ''
  mkdirOpen.value = true
}

async function doMkdir() {
  const name = mkdirName.value.trim()
  if (!name || mkdirBusy.value) return
  mkdirBusy.value = true
  err.value = ''
  try {
    const created = await LocalFS.MakeDir(listing.value?.path ?? '', name)
    mkdirOpen.value = false
    await load(created) // 建好即进入，省掉「再找一遍」的往返
  } catch (e) {
    err.value = unwrap(e).message
  } finally {
    mkdirBusy.value = false
  }
}

/* ------------------------------------------------------------- 盘符文案 */

const KIND_TEXT: Record<string, string> = {
  fixed: '本地磁盘',
  removable: '可移动磁盘',
  remote: '网络位置',
  cdrom: '光驱',
  ramdisk: '内存盘',
  other: '磁盘',
}

function driveText(d: LocalDrive): string {
  const kind = KIND_TEXT[d.kind] ?? '磁盘'
  return d.label ? `${d.label}（${kind}）` : kind
}

function confirm() {
  if (!canConfirm.value) return
  emit('confirm', listing.value!.path)
}
</script>

<template>
  <ModalShell
    :open="open"
    :title="title"
    icon="folder"
    :width="560"
    closable
    autofocus="#lp-path input"
    @close="emit('cancel')"
  >
    <!-- 工具条：上一级 / 此电脑 / 路径（可输入回车跳转）/ 新建 -->
    <template #bar>
      <div class="lp-bar">
        <Button
          iconOnly
          icon="folder-up"
          title="上一级"
          :disabled="!listing || !listing.parent"
          @click="load(listing?.parent ?? '')"
        />
        <Button
          iconOnly
          icon="home"
          title="此电脑（盘符列表）"
          :disabled="atRoot"
          @click="load('')"
        />
        <LineEdit
          id="lp-path"
          v-model="cur"
          clearable
          placeholder="输入文件夹路径后回车，例如 D:\CloudPrism 或 \\NAS\share"
          @enter="load(cur)"
        />
        <Button
          iconOnly
          icon="folder_add"
          title="在此新建文件夹"
          :disabled="atRoot"
          @click="openMkdir"
        />
      </div>
    </template>

    <!-- 新建文件夹：就地一行，Esc 只收这一行（.stop 拦在 ModalShell 的 window 监听之前） -->
    <div v-if="mkdirOpen" class="lp-mkdir">
      <Icon name="folder_add" :size="16" class="lp-mkdir-ic" />
      <LineEdit
        v-model="mkdirName"
        autofocus
        placeholder="新文件夹名称"
        @enter="doMkdir"
        @keydown.esc.stop="mkdirOpen = false"
      />
      <Button :disabled="mkdirBusy || !mkdirName.trim()" @click="doMkdir">创建</Button>
      <Button @click="mkdirOpen = false">取消</Button>
    </div>

    <p v-if="err" class="lp-err">
      <Icon name="cancel" :size="14" />
      <span>{{ err }}</span>
    </p>

    <!-- 正文：盘符列表（此电脑）或子目录列表 -->
    <div class="lp-list">
      <p v-if="busy" class="lp-hint">读取中…</p>

      <template v-else-if="atRoot">
        <p v-if="!listing?.drives?.length" class="lp-hint">没有检测到可用的磁盘。</p>
        <button
          v-for="d in listing?.drives ?? []"
          :key="d.path"
          type="button"
          class="lp-row"
          @click="load(d.path)"
        >
          <Icon name="folder" :size="17" class="lp-ic" />
          <span class="lp-name">{{ d.path }}</span>
          <span class="lp-tag">{{ driveText(d) }}</span>
          <Icon name="chevron_right_med" :size="15" class="lp-go" />
        </button>
      </template>

      <template v-else>
        <p v-if="!visibleDirs.length" class="lp-hint">
          此目录下没有子文件夹。可直接点「选择此文件夹」。
        </p>
        <button
          v-for="d in visibleDirs"
          :key="d.path"
          type="button"
          class="lp-row"
          :title="d.path"
          @click="load(d.path)"
        >
          <Icon name="folder" :size="17" class="lp-ic" :class="{dim: d.hidden}" />
          <span class="lp-name" :class="{dim: d.hidden}">{{ d.name }}</span>
          <span v-if="d.hidden" class="lp-tag">隐藏</span>
          <Icon name="chevron_right_med" :size="15" class="lp-go" />
        </button>
        <p v-if="listing?.truncated" class="lp-hint">目录项过多，仅显示前 2000 项。</p>
      </template>
    </div>

    <!-- 页脚：左侧如实展示「会回传哪个目录」（草稿输入不算数），右侧取消/确定 -->
    <template #foot-lead>
      <div class="lp-foot">
        <Checkbox v-if="hiddenCount" v-model="showHidden">
          显示隐藏文件夹（{{ hiddenCount }}）
        </Checkbox>
        <span v-if="canConfirm" class="lp-sel" :title="listing?.path">
          将选择：{{ listing?.path }}
        </span>
      </div>
    </template>
    <template #actions>
      <Button @click="emit('cancel')">取消</Button>
      <PrimaryButton :disabled="!canConfirm" @click="confirm">选择此文件夹</PrimaryButton>
    </template>
  </ModalShell>
</template>

<style scoped>
/* 路径工具条：图标钮 + 自适应输入框 */
.lp-bar {
  display: flex;
  align-items: center;
  gap: 6px;
}

.lp-bar .cp-line-edit {
  flex: 1;
  min-width: 0;
}

/* 新建文件夹行 */
.lp-mkdir {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.lp-mkdir-ic {
  flex: none;
  color: var(--accent);
}

.lp-mkdir .cp-line-edit {
  flex: 1;
  min-width: 0;
}

/* 错误行：读目录失败的原地提示 */
.lp-err {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 8px;
  font-size: 0.857rem;
  color: var(--err);
  overflow-wrap: anywhere;
}

.lp-err svg {
  flex: none;
  margin-top: 2px;
}

/* 目录列表：行式按钮，单击即进入 */
.lp-list {
  display: flex;
  flex-direction: column;
  gap: 1px;
  /* 列表自身不滚动：面板由 ModalShell 的 .ms-body 统一滚动（唯一滚动区），
     否则会出现内外两条滚动条。 */
}

.lp-row {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  min-height: 36px;
  padding: 0 10px;
  font: inherit;
  text-align: left;
  color: var(--text);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  transition: background var(--dur-fast) var(--ease);
}

.lp-row:hover {
  background: color-mix(in srgb, var(--text) 6%, transparent);
}

.lp-ic {
  flex: none;
  color: var(--accent);
}

.lp-ic.dim,
.lp-name.dim {
  opacity: 0.55;
}

.lp-name {
  flex: 1;
  min-width: 0;
  font-size: 0.857rem;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.lp-tag {
  flex: none;
  font-size: 0.786rem;
  color: var(--text2);
}

.lp-go {
  flex: none;
  color: var(--text2);
  opacity: 0.6;
}

/* 空态/提示行 */
.lp-hint {
  margin: 6px 0;
  font-size: 0.857rem;
  color: var(--text2);
}

/* 页脚左侧：隐藏项开关（有才显示）+「将选择」路径，竖排两行 */
.lp-foot {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

/* 路径可能很长，压缩省略并把完整值放 title */
.lp-sel {
  display: block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 手机：行加高，触摸目标够大 */
@media (max-width: 640px) {
  .lp-row {
    min-height: 48px;
  }
}
</style>
