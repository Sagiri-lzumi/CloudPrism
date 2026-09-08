<!--
  GridCard.vue —— v22 网格模式条目卡。
  对照 docs/ui-redesign/v21-mock.html：132px 卡宽、112px 缩略图、彩色文件
  类型图标、hover 整卡浮起、选中 accent 描边。图片条目异步请求加密缩略图
  （/t/ 令牌），退出视口卸载时吊销，防注册表被大量浏览撑满（ErrTooManyTokens）；
  目录与其余文件显示彩色类别图标占位。

  v22 关键改动：勾选徽章与 ⋯ 按钮彻底移出缩略图区，改放名称行（figcaption）
  右侧 —— 用户实测 v14/v15/v21 反复反馈「缩略图被徽章/⋯ 压住」，header band
  方案无论 22/24px 都无法消除感知遮挡，正解是把它们移进名称行，缩略图区
  100% 纯净。key 由父级按 remote 生成：切目录即重建。
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

// 彩色类型块类：目录/类别 → t-*
const typeClass = computed(() =>
  props.entry.isDir ? 't-dir' : `t-${kindOf(props.entry.display)}`,
)

// 是否处于选择集（多选成员或当前单选，决定整卡高亮）
function isSel(e: appstate.FileEntry): boolean {
  return ui.multi.some((x) => x.remote === e.remote)
}

// 多选批量态（≥2 项）：此时名称行右侧勾选图标才出现；单选仅靠 .gc.sel 整卡高亮
const multiMode = computed(() => ui.multi.length > 1)
</script>

<template>
  <figure
    class="gc"
    :class="{sel: isSel(entry)}"
    @click="emit('select', $event, entry)"
    @dblclick="emit('open', entry)"
    @contextmenu.prevent="emit('ctx', {ev: $event, entry})"
  >
    <!-- 缩略图：112px 纯净区，无任何徽章/⋯ 覆盖 -->
    <div class="thumb">
      <template v-if="entry.isDir">
        <span class="type-ic t-dir"><Icon name="folder" :size="28" /></span>
      </template>
      <img v-else-if="thumb" :src="thumb" class="img" alt="" draggable="false" @error="fail = true" />
      <Icon v-else-if="loading" name="sync" :size="28" class="spin" />
      <span v-else class="type-ic" :class="[typeClass, {err: fail}]">
        <Icon :name="placeholderIcon" :size="28" />
      </span>
    </div>

    <!-- 名称行：文件名（左，两行截断）+ 右侧操作槽（多选勾选图标 / ⋯ 按钮） -->
    <figcaption class="name">
      <span class="name-text" :title="entry.display">{{ entry.display }}</span>
      <span class="name-actions">
        <!-- 多选批量态：勾选图标（替代原左上角徽章，移出缩略图区） -->
        <span v-if="isSel(entry) && multiMode" class="gc-check" aria-hidden="true">
          <Icon name="check" :size="11" class="gc-check-ic" />
        </span>
        <!-- ⋯ 按钮（hover/选中时可见，点击弹与右键一致的菜单） -->
        <button
          type="button"
          class="gc-more"
          title="更多操作（与右键菜单一致）"
          @click.stop="emit('ctx', {ev: $event, entry})"
        >
          <Icon name="more" :size="14" />
        </button>
      </span>
    </figcaption>
  </figure>
</template>

<style scoped>
.gc {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 0;
  width: 132px;
  padding: 0;
  margin: 0;
  background: var(--surface);
  border: 1px solid var(--stroke-card);
  border-radius: var(--radius-card);
  cursor: default;
  user-select: none;
  overflow: hidden;
  transition: transform var(--dur) var(--ease-spring), box-shadow var(--dur) var(--ease),
    border-color var(--dur) var(--ease);
}

.gc:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-card-hover);
  border-color: var(--accent-soft-2);
}

/* 选中态：accent 描边 + 浮起 */
.gc.sel {
  background: var(--surface);
  border-color: var(--accent);
  border-width: 1.5px;
  box-shadow: 0 0 0 1px var(--accent), var(--shadow-card-hover);
}

/* 缩略图：112px 纯净区（无任何覆盖） */
.thumb {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 112px;
  background: var(--surface-2);
}

.img {
  max-width: 120px;
  max-height: 108px;
  object-fit: contain;
  border-radius: var(--r-card-sm);
}

/* 彩色类型图标块（56px 渐变底 + 白图标） */
.type-ic {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 56px;
  height: 56px;
  border-radius: 14px;
  color: #fff;
}

.type-ic.t-image { background: var(--type-image); }
.type-ic.t-video { background: var(--type-video); }
.type-ic.t-audio { background: var(--type-audio); }
.type-ic.t-text  { background: var(--type-text); }
.type-ic.t-dir   { background: var(--type-dir); }
.type-ic.t-other { background: var(--type-other); }

.type-ic.err {
  filter: grayscale(0.4);
  opacity: 0.7;
}

.spin {
  animation: gcspin 1.2s linear infinite;
  color: var(--text2);
}

@keyframes gcspin {
  to {
    transform: rotate(360deg);
  }
}

/* 名称行：文件名（左，两行截断）+ 右侧操作槽（勾选/⋯），横向布局 */
.name {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  min-height: 40px; /* 保证单行名称也有足够高度容纳操作钮 */
  padding: 6px 8px;
  border-top: 1px solid var(--divider);
}

.name-text {
  flex: 1;
  min-width: 0;
  max-height: 2.4em;
  font-size: 0.8rem;
  line-height: 1.3;
  color: var(--text);
  text-align: left;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow-wrap: anywhere;
}

/* 操作槽：多选勾选图标 + ⋯ 按钮（右端，不换行） */
.name-actions {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  flex: none;
}

/* 多选勾选图标（替代原左上角徽章，移出缩略图区）：accent 渐变实色小圈 */
.gc-check {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  background: var(--accent-grad);
  border-radius: 50%;
  box-shadow: 0 0 0 1.5px var(--surface), 0 1px 3px color-mix(in srgb, var(--accent) 40%, transparent);
}

.gc-check-ic {
  color: #fff;
}

/* ⋯ 按钮：hover/选中时显半透 surface 底 */
.gc-more {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
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
  background: var(--surface-hover);
}
</style>
