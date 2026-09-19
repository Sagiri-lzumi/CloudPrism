<!--
  GridCard.vue —— 网格模式条目卡（操作带 22px + 缩略图面 + 名称）。
  图片条目异步请求加密缩略图（/t/ 令牌），退出视口卸载时吊销，防注册表被
  大量浏览撑满（ErrTooManyTokens）；目录与其余文件显示类别占位图标。

  布局约束（历史修复，勿回退）：⋯ 与多选勾选角标落在**顶部操作带**内的空白里，
  不压在缩略图上；多选角标仅批量态（≥2）显示，单选只靠整卡高亮。

  视觉：缩略图放在一层浅色"承托面"（.thumb）上，尺寸不一的图片有了统一的
  落位边界，网格看起来才整齐；卡片 hover/选中只改底色+描边+微投影，不做位移，
  避免网格整体"抖一下"。
-->
<script setup lang="ts">
import {computed, onBeforeUnmount, onMounted, ref} from 'vue'
import type {appstate} from '../types/appstate'
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
// 图片解码完成后才淡入：缩略图是异步取的，直接挂上去会出现"从空白突然砸出
// 一张图"的跳变；淡入让网格的加载过程看起来是渐次填满的。
const imgLoaded = ref(false)

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

// 是否处于选择集（多选成员或当前单选，决定整卡高亮）
function isSel(e: appstate.FileEntry): boolean {
  return ui.multi.some((x) => x.remote === e.remote)
}

// 多选批量态（≥2 项）：此时勾选角标才出现；单选仅靠 .gc.sel 整卡高亮
//（v17：去掉单选常驻左上角蓝点的观感问题）
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
    <!-- 顶部操作带 22px：⋯ 在此，不压缩略图；勾选角标仅多选态出现 -->
    <div class="gc-head">
      <span v-if="isSel(entry) && multiMode" class="gc-check" aria-hidden="true">
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
    <!-- 缩略图承托面：统一落位边界，图片按 contain 内嵌不裁切 -->
    <div class="thumb">
      <template v-if="entry.isDir">
        <Icon name="folder" :size="52" class="ic dir" />
      </template>
      <img
        v-else-if="thumb"
        :src="thumb"
        class="img"
        :class="{in: imgLoaded}"
        alt=""
        draggable="false"
        @load="imgLoaded = true"
        @error="fail = true"
      />
      <Icon v-else-if="loading" name="sync" :size="28" class="ic spin" />
      <Icon v-else :name="placeholderIcon" :size="44" class="ic" :class="{err: fail}" />
    </div>
    <figcaption class="name" :title="entry.display">{{ entry.display }}</figcaption>
  </figure>
</template>

<style scoped>
.gc {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: stretch; /* 操作带/缩略图面/名称撑满宽度 */
  gap: 2px;
  /* 宽度由父级网格列决定（.grid 的 auto-fill 列宽）：卡片是网格的直接子项，
     用 100% 填满列，避免出现"列 148 / 卡 110"这种各写各的错配 */
  width: 100%;
  padding: 0;
  margin: 0;
  border: 1px solid transparent;
  border-radius: var(--radius-card);
  cursor: default;
  user-select: none;
  overflow: hidden; /* 圆角裁剪内部面 */
  transition: background var(--dur-fast) var(--ease), border-color var(--dur-fast) var(--ease),
    box-shadow var(--dur-fast) var(--ease);
}

.gc:hover {
  background: color-mix(in srgb, var(--text) 5%, transparent);
}

/* 选中态：accent-soft 底 + accent 描边 + 一圈极淡外环（三重强化，
   在浅色与深色下都能一眼分辨） */
.gc.sel {
  background: var(--accent-soft);
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-softer);
}

/* 顶部操作带 22px：徽章/⋯ 落在里面，不压在缩略图上 */
.gc-head {
  position: relative;
  flex: none;
  height: 22px;
}

/* 选中勾选角标（仅多选批量态显示，见 multiMode）：绝对定位在 header 左上，
   圆形 accent 底 + 白对勾 + surface 描边圈；单选不显示，靠 .gc.sel 高亮 */
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

/* 缩略图承托面：一层比卡片略深的浅色面，给所有条目一个统一的"落位框"；
   宽高比不一的图片按 contain 内嵌，不会被裁掉，也不会把网格撑得参差。 */
.thumb {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  height: 96px;
  margin: 0 6px 2px; /* 左右内缩 6px：承托面比卡片窄一圈，形成层次 */
  overflow: hidden;
  border-radius: 10px; /* 内层面圆角略小于外卡（12），嵌套才自然 */
  background: color-mix(in srgb, var(--text) 4%, transparent);
}

/* 类别图标（裸 Icon，随 text2/accent/err 着色）；
   overflow visible + border-box 固定尺寸，防浏览器渲染下被裁切 */
.ic {
  color: var(--text2);
  overflow: visible;
  box-sizing: border-box;
}

.ic.dir {
  color: var(--accent);
}

.ic.err {
  color: var(--err);
}

.img {
  width: 100%;
  height: 100%;
  max-width: 96px;
  max-height: 96px;
  border-radius: 8px;
  object-fit: contain;
  opacity: 0;
  transition: opacity var(--dur) var(--ease);
}

/* 解码完成 → 淡入（由 @load 打标） */
.img.in {
  opacity: 1;
}

.spin {
  animation: gcspin 1.2s linear infinite;
}

@keyframes gcspin {
  to {
    transform: rotate(360deg);
  }
}

/* 名称：两行截断，底距让卡片呼吸；选中时随整体高亮 */
.name {
  padding: 2px 6px 8px;
  width: 100%;
  max-height: 2.7em; /* 两行截断 */
  font-size: 0.786rem;
  line-height: 1.35;
  color: var(--text);
  text-align: center;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow-wrap: anywhere;
}

.gc.sel .name {
  color: var(--accent);
  font-weight: 600;
}

/* ---- 触摸设备：hover 缺失下的可见性与命中区 ----
   .gc-more 原为 hover/sel 才显形，触摸下会永久隐形（手机上拿不到改名/删除
   入口）→ 常显；18px 命中区远小于手指，放大到 28px。 */
@media (hover: none) and (pointer: coarse) {
  .gc-more {
    opacity: 1;
    width: 28px;
    height: 28px;
  }
}

/* ---- 手机：列宽由 .grid 放大，缩略图面同步加高更好认图 ---- */
@media (max-width: 640px) {
  .gc-more {
    width: 28px;
    height: 28px;
  }

  .thumb {
    height: 124px;
    border-radius: 12px;
  }

  .img {
    max-width: 100%;
    max-height: 124px;
  }

  .ic.dir {
    width: 64px;
    height: 64px;
  }
}

</style>