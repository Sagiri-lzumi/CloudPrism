<!--
  GridCard.vue —— 网格模式条目卡（header band 22px + 缩略图 110×44 + 名称）。
  对照 Python side_panel 网格视图。图片条目异步请求加密缩略图（/t/ 令牌），
  退出视口卸载时吊销，防注册表被大量浏览撑满（ErrTooManyTokens）；目录与其
  余文件显示类别占位图标。徽章/⋯ 落在 header band 内的空白里，不再压在缩
  略图上（v14 的 top:2px 把图标贴在缩略图上被吐槽遮挡）。key 由父级按 remote
  生成：切目录即重建。
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
  /** 上下文菜单：单对象载荷避免 Vue 编译器在组件事件上丢掉闭包变量 */
  ctx: [payload: {ev: MouseEvent; entry: appstate.FileEntry}]
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

// 图标位内容：目录=folder；图片=缩略图/加载中/占位；其它=类别图标
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
    @contextmenu.prevent="emit('ctx', {ev: $event, entry})"
  >
    <!-- 顶部操作带 22px：徽章/⋯ 在此，不压缩略图 -->
    <div class="gc-head">
      <span v-if="isSel(entry)" class="gc-check" aria-hidden="true">
        <Icon name="check" :size="11" class="gc-check-ic" />
      </span>
      <button
        type="button"
        class="gc-more"
        title="更多操作（与右键菜单一致）"
        @click.stop="emit('ctx', {ev: $event, entry})"
      >
        <Icon name="more" :size="14" />
      </button>
    </div>
    <!-- 缩略图：让位 14px 给 header band，宽 110 高 44 -->
    <div class="thumb">
      <template v-if="entry.isDir">
        <Icon name="folder" :size="36" class="ic dir" />
      </template>
      <img v-else-if="thumb" :src="thumb" class="img" alt="" draggable="false" @error="fail = true" />
      <Icon v-else-if="loading" name="sync" :size="20" class="ic spin" />
      <Icon v-else :name="placeholderIcon" :size="32" class="ic" :class="{err: fail}" />
    </div>
    <figcaption class="name" :title="entry.display">{{ entry.display }}</figcaption>
  </figure>
</template>

<style scoped>
.gc {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: stretch; /* header/thumb/name 撑满宽度 */
  gap: 2px;
  width: 110px;
  padding: 0;
  margin: 0;
  border: 1px solid transparent;
  border-radius: var(--radius-card);
  cursor: default;
  user-select: none;
  overflow: hidden; /* 圆角裁剪 header 背景 */
}

.gc:hover {
  background: color-mix(in srgb, var(--text) 5%, transparent);
}

/* 选中态：accent-soft 背景 + 1px accent 实线 border，强化对比度 */
.gc.sel {
  background: var(--accent-soft);
  border-color: var(--accent);
}

/* 顶部操作带 22px：徽章/⋯ 落在里面，不压在缩略图上 */
.gc-head {
  position: relative;
  flex: none;
  height: 22px;
}

/* 选中徽章：绝对定位在 header 左上，圆形 accent 底 + 白对勾 + surface 描边圈
   防背景穿透（accent-soft 选中态上叠 accent 实色圈辨识度高） */
.gc-check {
  position: absolute;
  top: 3px;
  left: 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  background: var(--accent);
  border-radius: 50%;
  z-index: 2;
  box-shadow: 0 0 0 1.5px var(--surface), 0 1px 2px rgba(0, 0, 0, 0.2);
}

.gc-check-ic {
  color: #fff;
}

/* 更多按钮：header 右上角；hover/选中时显半透 surface 底 */
.gc-more {
  position: absolute;
  top: 3px;
  right: 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: 50%;
  opacity: 0;
  transition: opacity var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease),
    background var(--dur-fast) var(--ease);
}

.gc:hover .gc-more,
.gc.sel .gc-more,
.gc-more:focus-visible {
  opacity: 1;
}

.gc-more:hover {
  color: var(--accent);
  background: color-mix(in srgb, var(--surface) 85%, transparent);
}

/* 缩略图：让位 22px+2px 给 header band，宽 110 高 44 */
.thumb {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 44px;
  padding: 0 6px;
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
  max-width: 98px;
  max-height: 44px;
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

/* 名称：两行截断，6px 底距让卡片呼吸 */
.name {
  padding: 0 4px 6px;
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