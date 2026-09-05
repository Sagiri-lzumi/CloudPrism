<!--
  GridCard.vue —— 网格模式条目卡（96px 图标位 + 名称，对照 Python
  side_panel 网格视图）。图片条目异步请求加密缩略图（/t/ 令牌），退出
  视口卸载时吊销，防注册表被大量浏览撑满（ErrTooManyTokens）；目录与
  其余文件显示类别占位图标。key 由父级按 remote 生成：切目录即重建。
-->
<script setup lang="ts">
import {computed, onBeforeUnmount, onMounted, ref} from 'vue'
import type {appstate} from '../../wailsjs/go/models'
import {thumbUrl, revoke, ui} from '../lib/store'
import {kindOf, KIND_ICON} from '../lib/media'
import Icon from '../components/fluent/Icon.vue'

const props = defineProps<{
  entry: appstate.FileEntry
}>()

const emit = defineEmits<{
  select: [ev: MouseEvent, e: appstate.FileEntry]
  open: [e: appstate.FileEntry]
  ctx: [e: MouseEvent, entry: appstate.FileEntry]
}>()

const isImg = ref(!props.entry.isDir && kindOf(props.entry.display) === 'image')
const thumb = ref('')
const fail = ref(false)
const loading = ref(false)

let disposed = false

onMounted(() => {
  if (!isImg.value) return
  loading.value = true
  void (async () => {
    try {
      const u = await thumbUrl(props.entry.remote)
      if (disposed) {
        void revoke(u).catch(() => {}) // 卸载后才返回的令牌直接吊销
        return
      }
      thumb.value = u
    } catch {
      if (!disposed) fail.value = true
    } finally {
      if (!disposed) loading.value = false
    }
  })()
})

onBeforeUnmount(() => {
  disposed = true
  if (thumb.value) void revoke(thumb.value).catch(() => {})
})

// 图标位内容：目录=folder 大图标；图片=缩略图/加载中/占位；其它=类别图标
const placeholderIcon = computed(() =>
  props.entry.isDir ? 'folder' : KIND_ICON[kindOf(props.entry.display)],
)

// 是否处于选择集（普通单选或多选成员都高亮）
function isSel(e: appstate.FileEntry): boolean {
  return ui.multi.some((x) => x.remote === e.remote)
}
</script>

<template>
  <figure
    class="gc"
    :class="{sel: isSel(entry)}"
    @click="emit('select', $event, entry)"
    @dblclick="emit('open', entry)"
    @contextmenu.prevent="emit('ctx', $event, entry)"
  >
    <Icon v-if="isSel(entry)" name="square-check" :size="16" class="gc-check" />
    <button
      type="button"
      class="gc-more"
      title="更多操作（删除、重命名、下载…）"
      @click.stop="emit('ctx', $event, entry)"
    >
      <Icon name="more" :size="12" />
    </button>
    <div class="thumb">
      <template v-if="entry.isDir">
        <Icon name="folder" :size="44" class="ic dir" />
      </template>
      <img v-else-if="thumb" :src="thumb" class="img" alt="" draggable="false" @error="fail = true" />
      <Icon v-else-if="loading" name="sync" :size="24" class="ic spin" />
      <Icon v-else :name="placeholderIcon" :size="36" class="ic" :class="{err: fail}" />
    </div>
    <figcaption class="name" :title="entry.display">{{ entry.display }}</figcaption>
  </figure>
</template>

<style scoped>
.gc {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  width: 96px;
  padding: 8px 4px 6px;
  margin: 0;
  border: 1px solid transparent;
  border-radius: 6px;
  cursor: default;
  user-select: none;
}

.gc:hover {
  background: color-mix(in srgb, var(--text) 5%, transparent);
}

.gc.sel {
  background: var(--accent-soft);
  border-color: color-mix(in srgb, var(--accent) 45%, transparent);
}

/* 更多菜单：hover 才显示；定位卡片右上角 */
.gc-more {
  position: absolute;
  top: 4px;
  right: 4px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  color: var(--text2);
  background: color-mix(in srgb, var(--surface) 92%, transparent);
  border: 1px solid var(--stroke);
  border-radius: 4px;
  opacity: 0;
  transition: opacity var(--dur-fast) var(--ease), background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

/* 选中标记：square-check 图标自带蓝底白勾，直接放卡片左上角 */
.gc-check {
  position: absolute;
  top: 2px;
  left: 4px;
  z-index: 1;
}

.gc:hover .gc-more,
.gc.sel .gc-more,
.gc-more:focus-visible {
  opacity: 1;
}

.gc-more:hover {
  background: var(--surface);
  color: var(--accent);
}

/* 图标位：96px 视窗内容 96×72 图区（Python iconSize 96 的扁化） */
.thumb {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 96px;
  height: 64px;
}

.ic {
  color: var(--text2);
}

.ic.dir {
  color: var(--accent);
}

.ic.err {
  color: var(--err);
}

.img {
  max-width: 96px;
  max-height: 64px;
  object-fit: contain;
}

.spin {
  animation: gcspin 1.2s linear infinite;
}

@keyframes gcspin {
  to {
    transform: rotate(360deg);
  }
}

.name {
  width: 100%;
  max-height: 2.4em; /* 两行截断 */
  font-size: 0.786rem;
  line-height: 1.2;
  color: var(--text);
  text-align: center;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow-wrap: anywhere;
}
</style>
