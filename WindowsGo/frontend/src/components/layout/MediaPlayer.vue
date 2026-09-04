<!--
  MediaPlayer.vue —— 令牌化解密流的自绘播放器（预览面板内嵌）。
  对应 Python PlayerWidget：不做落盘，明文仅在 WebView 与 Go 代理内存中
  流动。能力：播放/暂停、进度拖动（change 才 seek —— 拖动过程只是预览
  时间，避免逐像素触发后端 Range 解密）、当前/总时长、音量与静音。
  失败兜底：错误横幅 + 「解密导出到本地」按钮（Go Export 弹目录框）。
-->
<script setup lang="ts">
import {computed, nextTick, onBeforeUnmount, ref, watch} from 'vue'
import {fmtDur} from '../../lib/format'
import {exportSel} from '../../lib/store'
import Button from '../fluent/Button.vue'
import Icon from '../fluent/Icon.vue'

const props = defineProps<{
  /** 代理播放 URL（Preview.MediaURL 签发） */
  url: string
  /** 远端展示名（信息行用） */
  display: string
}>()

const v = ref<HTMLVideoElement | null>(null)

const playing = ref(false)
const muted = ref(false)
/** 缓冲期（切源后等待 metadata） */
const ready = ref(false)
const failed = ref(false)
const duration = ref(0)
const current = ref(0)
/** 拖动中的临时进度（秒）；null = 未在拖动 */
const dragPos = ref<number | null>(null)
const volume = ref(1)

const shownPos = computed(() => dragPos.value ?? current.value)

/** 播放/暂停切换（中央大钮与底条小钮共用）。 */
function togglePlay() {
  const el = v.value
  if (!el || failed.value) return
  if (el.paused) void el.play().catch(() => {})
  else el.pause()
}

/** 进度条拖动：拖动过程只更新显示，松手才 seek（省后端 Range 请求）。 */
function seekCommit() {
  if (dragPos.value == null || !v.value) return
  v.value.currentTime = dragPos.value
  dragPos.value = null
}

function toggleMute() {
  if (!v.value) return
  muted.value = !muted.value
  v.value.muted = muted.value
}

function onVol(e: Event) {
  const el = v.value
  if (!el) return
  volume.value = Number((e.target as HTMLInputElement).value)
  el.volume = volume.value
  muted.value = volume.value === 0
  el.muted = muted.value
}

// 切源（换选中文件）→ 重置状态并重载
watch(
  () => props.url,
  async (url) => {
    failed.value = false
    ready.value = false
    duration.value = 0
    current.value = 0
    dragPos.value = null
    if (!url) return
    await nextTick()
    const el = v.value
    if (el) {
      el.load()
      // 用户已在本会话播放过（有交互手势权限），尽量续播
      void el.play().catch(() => {})
    }
  },
  {immediate: true},
)

onBeforeUnmount(() => {
  // 停止解码，让代理连接尽快关闭（换条目会先 revoke token）
  v.value?.pause()
})
</script>

<template>
  <div class="cp-media" :class="{failed}">
    <video
      ref="v"
      class="surface"
      :src="url"
      preload="metadata"
      @loadedmetadata="(e: Event) => {duration = (e.target as HTMLVideoElement).duration; ready = true}"
      @timeupdate="(e: Event) => {if (dragPos == null) current = (e.target as HTMLVideoElement).currentTime}"
      @durationchange="(e: Event) => {duration = (e.target as HTMLVideoElement).duration; ready = true}"
      @play="playing = true"
      @pause="playing = false"
      @ended="playing = false; current = duration"
      @error="failed = true"
      @click="togglePlay"
    ></video>

    <!-- 解码失败横幅：红字 + 落盘兜底（系统播放器播放导出文件） -->
    <div v-if="failed" class="err" role="alert">
      <p class="err-msg">无法解码此媒体（或后端流式响应异常）。</p>
      <p class="err-sub">可先解密导出到本地，再用系统播放器打开。</p>
      <Button icon="download" @click="exportSel">导出到本地</Button>
    </div>

    <!-- 中央播放钮（暂停态悬浮） -->
    <button
      v-if="!playing && !failed"
      type="button"
      class="big-play"
      :title="ready ? '播放' : '加载中…'"
      :disabled="!ready"
      @click="togglePlay"
    >
      <Icon :name="ready ? 'play' : 'sync'" :size="30" :class="{spin: !ready}" />
    </button>

    <!-- 底条控制 -->
    <div v-if="!failed" class="bar">
      <button type="button" class="ctl" title="播放/暂停" @click="togglePlay">
        <Icon :name="playing ? 'pause' : 'play'" :size="16" />
      </button>
      <span class="time">{{ fmtDur(shownPos) }}</span>
      <input
        class="seek"
        type="range"
        min="0"
        :max="duration || 0"
        step="0.5"
        :value="shownPos"
        title="播放进度"
        :disabled="!ready || !duration"
        @input="dragPos = Number(($event.target as HTMLInputElement).value)"
        @change="seekCommit"
      />
      <span class="time dim">{{ fmtDur(duration) }}</span>
      <button
        type="button"
        class="ctl"
        :title="muted ? '取消静音' : '静音'"
        @click="toggleMute"
      >
        <Icon :name="muted || volume === 0 ? 'mute' : 'volume'" :size="16" />
      </button>
      <input
        class="vol"
        type="range"
        min="0"
        max="1"
        step="0.05"
        :value="volume"
        title="音量"
        @input="onVol"
      />
    </div>
  </div>
</template>

<style scoped>
.cp-media {
  position: relative;
  display: flex;
  flex-direction: column;
  height: 100%;
  background: #000; /* 视频区黑底（图片文本不走此组件） */
}

.surface {
  flex: 1;
  min-height: 0;
  width: 100%;
  outline: none;
}

/* 中央播放钮：半透明黑圆钮悬浮（Fluent 媒体风格） */
.big-play {
  position: absolute;
  top: calc(50% - 34px);
  left: 50%;
  transform: translate(-50%, -50%);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 68px;
  height: 68px;
  color: #fff;
  background: rgba(0, 0, 0, 0.55);
  border: none;
  border-radius: 50%;
  backdrop-filter: blur(4px);
}

.big-play:disabled {
  opacity: 0.6;
}

.spin {
  animation: cpspin 1.2s linear infinite;
}

@keyframes cpspin {
  to {
    transform: rotate(360deg);
  }
}

/* 底条：播放控制 + 进度 + 音量 */
.bar {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 40px;
  padding: 0 12px;
  background: var(--surface);
  border-top: 1px solid var(--divider);
  user-select: none;
}

.ctl {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 28px;
  height: 28px;
  color: var(--text);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
}

.ctl:hover {
  background: color-mix(in srgb, var(--text) 8%, transparent);
}

.time {
  flex: none;
  min-width: 44px;
  font-size: 0.786rem;
  color: var(--text);
  font-variant-numeric: tabular-nums;
}

.time.dim {
  color: var(--text2);
}

/* range 进度条/音量条统一样式（webkit 桌面渲染） */
.seek,
.vol {
  --track: color-mix(in srgb, var(--text) 20%, transparent);
  appearance: none;
  height: 4px;
  border-radius: var(--radius-round);
  background: var(--track);
  outline: none;
}

.seek {
  flex: 1;
  min-width: 0;
}

.vol {
  width: 72px;
}

.seek::-webkit-slider-thumb,
.vol::-webkit-slider-thumb {
  appearance: none;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--accent);
  border: none;
  box-shadow: 0 0 0 2px var(--surface);
}

.seek:disabled {
  opacity: 0.4;
}

/* 错误横幅 */
.err {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 24px;
  text-align: center;
  background: #000;
}

.err-msg {
  margin: 0;
  color: #d13438; /* 语义红固定值：黑底上的高对比（主题 err 深色下偏亮） */
  font-weight: 600;
}

.err-sub {
  margin: 0 0 10px;
  color: #c4c4c4;
  font-size: 0.857rem;
}

.cp-media:not(.failed) .err {
  display: none;
}
</style>
