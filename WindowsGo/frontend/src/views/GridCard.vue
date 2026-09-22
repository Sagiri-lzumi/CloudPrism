<!--
  GridCard.vue —— 网格模式条目卡（操作带 22px + 缩略图面 + 名称）。
  图片条目异步请求加密缩略图（/t/ 令牌）；**视频条目异步抽中心帧当封面**
  （/s/ 令牌 + 离屏 <video>，见 lib/videoCover.ts），两者都在退出视口卸载时
  回收（令牌吊销 / objectURL revoke），防注册表被大量浏览撑满（ErrTooManyTokens）。
  目录与其余文件显示类别占位图标。

  布局约束（历史修复，勿回退）：⋯ 与多选勾选角标落在**顶部操作带**内的空白里，
  不压在缩略图上；多选角标仅批量态（≥2）显示，单选只靠整卡高亮。

  上传占位态（props.upload 有值）：这是一个「正在上传」的乐观条目，服务端还没有
  它 —— 不取缩略图/封面（还没传上去，取也是 404）、不出 ⋯/勾选角标、不响应点击与
  右键（所有动作都还没有对象）。够大的文件在缩略图面正中压一个圆环进度（见 store 的
  UPLOAD_RING_MIN）。

  视觉：缩略图放在一层浅色"承托面"（.thumb）上，尺寸不一的图片有了统一的
  落位边界，网格看起来才整齐；卡片 hover 抬升 2px + 微放大（transform，不参与排版，
  所以网格不会整体抖一下 —— 位移只发生在这一张卡上，邻居不重排）。

  视频封面与图片**刻意走不同的 object-fit**：图片 contain（不能裁掉内容），
  视频帧 cover（帧本身就是满幅画面，contain 会留出难看的黑边）。
-->
<script setup lang="ts">
import {computed, onBeforeUnmount, onMounted, ref} from 'vue'
import type {appstate} from '../types/appstate'
import {thumbUrl, revoke, ui} from '../lib/store'
import {kindOf, KIND_ICON} from '../lib/media'
import {requestVideoCover, type CoverHandle} from '../lib/videoCover'
import Icon from '../components/fluent/Icon.vue'
import ProgressRing from '../components/fluent/ProgressRing.vue'

const props = defineProps<{
  entry: appstate.FileEntry
  /** 有值 = 该卡是「上传中」占位（见文件头注释） */
  upload?: {ratio: number | null; showRing: boolean}
}>()

const emit = defineEmits<{
  select: [ev: MouseEvent, e: appstate.FileEntry]
  open: [e: appstate.FileEntry]
  /** 上下文菜单：单对象载荷避免 Vue 编译器在组件事件上丢掉闭包变量 */
  ctx: [payload: {ev: MouseEvent; entry: appstate.FileEntry}]
}>()

const up = computed(() => props.upload ?? null)

// 占位态下所有条目动作都不可用（还没有远端对象可操作）
function onSelect(ev: MouseEvent) {
  if (up.value) return
  emit('select', ev, props.entry)
}

function onOpen() {
  if (up.value) return
  emit('open', props.entry)
}

function onCtx(ev: MouseEvent) {
  if (up.value) return
  emit('ctx', {ev, entry: props.entry})
}

const isImg = computed(() => !props.entry.isDir && kindOf(props.entry.display) === 'image')
const isVideo = computed(() => !props.entry.isDir && kindOf(props.entry.display) === 'video')
const thumb = ref('')
/** 视频封面：本地 objectURL（由抽帧 Blob 造，非令牌 URL） */
const cover = ref('')
/** 封面时长（秒；0 = 容器没报时长，不画胶囊） */
const coverDur = ref(0)
const fail = ref(false)
const loading = ref(false)
// 图片解码完成后才淡入：缩略图是异步取的，直接挂上去会出现"从空白突然砸出
// 一张图"的跳变；淡入让网格的加载过程看起来是渐次填满的。
const imgLoaded = ref(false)

let disposed = false
/** 抽帧任务句柄：卸载时 cancel，避免排队中的任务白跑一趟 */
let coverTask: CoverHandle | null = null

onMounted(() => {
  if (up.value) return
  if (isImg.value) void loadThumb()
  else if (isVideo.value) void loadCover()
})

async function loadThumb() {
  loading.value = true
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
}

/**
 * 视频封面：抽中心帧（细节与降级策略见 lib/videoCover.ts）。
 * 失败**不置 fail**：没有封面是正常结局（容器不支持等），卡片安静地显示类别
 * 图标即可 —— 显示成错误态会把「浏览器解不了这个容器」误报成「文件坏了」。
 */
async function loadCover() {
  loading.value = true
  coverTask = requestVideoCover(props.entry)
  try {
    const out = await coverTask.promise
    if (disposed) return
    cover.value = URL.createObjectURL(out.blob)
    coverDur.value = out.duration
  } catch {
    /* 见上方注释：保持类别图标 */
  } finally {
    if (!disposed) loading.value = false
  }
}

onBeforeUnmount(() => {
  disposed = true
  coverTask?.cancel()
  coverTask = null
  if (thumb.value) void revoke(thumb.value).catch(() => {})
  // 封面是本地 blob URL，走 URL.revokeObjectURL；不要用 store 的 revoke
  // （它是给 /s/ /t/ 令牌用的，作用对象是服务端注册表）
  if (cover.value) URL.revokeObjectURL(cover.value)
})

/** 秒 → 视频时长胶囊文案：`m:ss`，满一小时才带小时段。 */
function fmtDur(sec: number): string {
  const s = Math.max(0, Math.round(sec))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const r = s % 60
  const mm = h > 0 ? String(m).padStart(2, '0') : String(m)
  return `${h > 0 ? `${h}:` : ''}${mm}:${String(r).padStart(2, '0')}`
}

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
  <!-- 右键必须 .stop：FilesView 的条目区 .zone 也挂了 contextmenu（空白区弹菜单），
       它在 DOM 上是本卡的祖先。只 .prevent 不 .stop 时事件会继续冒泡到 .zone，
       那里无条件 openCtx(ev, undefined) 把 ctxEntry 覆盖成 null ⇒ 右键条目最终弹的
       是「空白区菜单」，条目动作菜单等于失效（实测：右键卡片与右键列表行都只弹
       「新建文件夹/上传文件…/上传文件夹…/刷新」）。⋯ 按钮的 @click.stop 同理。 -->
  <figure
    class="gc"
    :class="{sel: isSel(entry), up: !!up}"
    @click="onSelect"
    @dblclick="onOpen"
    @contextmenu.prevent.stop="onCtx"
  >
    <!-- 顶部操作带 22px：⋯ 在此，不压缩略图；勾选角标仅多选态出现。
         上传占位态两者都不出（没有可操作的对象、也不在多选集里）。 -->
    <div class="gc-head">
      <span v-if="!up && isSel(entry) && multiMode" class="gc-check" aria-hidden="true">
        <Icon name="check" :size="11" class="gc-check-ic" />
      </span>
      <button
        v-if="!up"
        type="button"
        class="gc-more"
        title="更多操作（与右键菜单一致）"
        @click.stop="onCtx"
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
        v-else-if="!up && thumb"
        :src="thumb"
        class="img"
        :class="{in: imgLoaded}"
        alt=""
        draggable="false"
        @load="imgLoaded = true"
        @error="fail = true"
      />
      <!-- 视频封面（抽中心帧）：与图片同用 .img，但 object-fit 换 cover。
           解码失败就当没有封面（清空 src 落到下面的占位图标分支），
           不要走 fail —— 那会显示成红色错误图标，把「容器不支持」说成「文件坏了」。 -->
      <img
        v-else-if="!up && cover"
        :src="cover"
        class="img cover in"
        alt=""
        draggable="false"
        @error="cover = ''"
      />
      <Icon v-else-if="!up && loading" name="sync" :size="28" class="ic spin" />
      <Icon v-else :name="placeholderIcon" :size="44" class="ic" :class="{err: fail}" />

      <!-- 视频标识：封面只是抽出来的一帧静止画面，没有播放角标与时长胶囊，
           用户分不清「这是视频」还是「这是一张图」。
           两者都是**绝对定位** —— 条件渲染的元素绝不能裸参与 .thumb 的 flex 排版，
           否则它们一出现就把中间的 img 挤偏（见 .up-ring 同一处理，本项目踩过多次）。 -->
      <template v-if="!up && cover">
        <span class="play" aria-hidden="true"><Icon name="play" :size="15" /></span>
        <span v-if="coverDur > 0" class="dur">{{ fmtDur(coverDur) }}</span>
      </template>

      <!-- 上传进度圆环：只给够大的文件画（小文件瞬间完成，画了反而闪） -->
      <ProgressRing
        v-if="up && up.showRing"
        class="up-ring"
        :ratio="up.ratio"
        :size="52"
      />
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
    box-shadow var(--dur-fast) var(--ease), transform var(--dur-fast) var(--ease-spring);
}

/* hover：抬 2px + 微放大 + 浮起阴影，过冲曲线让它"弹"起来一下。
   位移/缩放全在 transform 上，不参与排版 —— 所以「网格整体抖一下」并不会发生，
   动的是这一张卡自己（早前版本曾因为担心抖动而完全不做位移，那是把
   「transform 不重排」和「改变尺寸」混为一谈了）。
   z-index 抬到 --z-raise：否则抬升后会被后序兄弟盖住半张。 */
.gc:hover {
  background: color-mix(in srgb, var(--text) 5%, transparent);
  box-shadow: var(--shadow-card);
  transform: translateY(-2px) scale(1.012);
  z-index: var(--z-raise);
}

/* 按压：收回去一点点，给"点到了"的触感 */
.gc:active {
  transform: translateY(0) scale(.985);
}

/* 选中态：accent-soft 底 + accent 描边 + 一圈极淡外环（三重强化，
   在浅色与深色下都能一眼分辨） */
.gc.sel {
  background: var(--accent-soft);
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-softer);
}

/* 上传占位（乐观条目）：不可交互，视觉上比真条目轻一档 —— 它在等真实条目
   把它替换掉，抢眼反而会让人以为已经传完。 */
.gc.up {
  cursor: default;
}

.gc.up .name {
  opacity: 0.65;
}

.gc.up .thumb {
  background: color-mix(in srgb, var(--text) 3%, transparent);
}

/* 圆环压在缩略图面正中：绝对定位 + inset:0 + margin:auto，
   与同层的占位图标解耦（否则两者会并排排开）。 */
.up-ring {
  position: absolute;
  inset: 0;
  margin: auto;
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
  z-index: var(--z-raise);
  box-shadow: 0 0 0 1.5px var(--surface), 0 1px 2px rgba(0, 0, 0, 0.2);
}

.gc-check-ic {
  color: var(--text-on-accent);
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
   宽高比不一的图片按 contain 内嵌，不会被裁掉，也不会把网格撑得参差。
   盒子取 4:3 而非正方形：照片与截图以 3:2 / 4:3 为主，方形盒会让它们上下各留
   一大块空，缩略图看起来"很小、很飘"；4:3 把留白压到最小，同时仍能容纳竖图。 */
.thumb {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  aspect-ratio: 4 / 3;
  margin: 0 6px 3px; /* 左右内缩 6px：承托面比卡片窄一圈，形成层次 */
  overflow: hidden;
  /* 内层面圆角略小于外卡（--radius-card = 12），嵌套才自然 */
  border-radius: 10px;
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
  border-radius: 8px;
  object-fit: contain;
  opacity: 0;
  transition: opacity var(--dur) var(--ease);
}

/* 解码完成 → 淡入（由 @load 打标） */
.img.in {
  opacity: 1;
}

/* 视频封面：帧画面按 cover 铺满承托面（图片那份是 contain，方向相反，别混）。
   理由：帧本身就是 3:2/16:9 的满幅画面，contain 会在 4:3 的承托面里留出黑边，
   看起来像"图很小、周围一圈空"。 */
.img.cover {
  object-fit: cover;
}

/* 播放角标：绝对定位 + inset:0 + margin:auto 居中（与 .up-ring 同一套写法）。
   **不要**靠 flex 对齐来居中绝对定位子元素 —— 那依赖静态位置，行为不稳定。 */
.play {
  position: absolute;
  inset: 0;
  margin: auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  /* 恒白图标 + 恒黑罩：压在任意一帧画面上都必须可读，故刻意不随主题变化
     （与 MediaPlayer 的控件底同源，见 theme.css 的 --scrim-media 注释） */
  color: #fff;
  background: var(--scrim-media);
  border-radius: 50%;
  box-shadow: 0 1px 3px rgba(0, 0, 0, .3);
  pointer-events: none;
}

/* 时长胶囊：右下角，与播放角标同一套恒定配色 */
.dur {
  position: absolute;
  right: 5px;
  bottom: 5px;
  padding: 1px 5px;
  font-size: 0.714rem;
  line-height: 1.3;
  color: #fff;
  background: var(--scrim-media);
  border-radius: var(--radius-round);
  font-variant-numeric: tabular-nums;
  pointer-events: none;
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
    border-radius: var(--radius-card);
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