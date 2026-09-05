<!--
  MediaPlayer.vue —— 令牌化解密流的自绘播放器（预览面板内嵌）。
  对应 Python PlayerWidget：不做落盘，明文仅在 WebView 与 Go 代理内存中
  流动。能力：播放/暂停、进度拖动（change 才 seek）、当前/总时长、音量与静音。

  本轮 v8 调整：
  · 进入预览**不自动播放**（v7 行为：watch url 即 el.play() 自动起播），挂载后只
    预载 metadata，等用户点中央大钮/底条小钮才 start。
  · **记忆续播**：上次播放位置按 props.remote（密文路径）作 key 写入
    localStorage（cp.video.resume.{remote}）。再次进入同一文件时 seek 到
    该位置，但**不恢复播放**（仍需用户点播放）。播到末尾自动清除记忆。
  · 错误兜底仅展示原因，**不再提供「导出到本地」按钮**（用户认为该功能
    冗余；文件页/批量下载等已完整覆盖解密导出场景）。
-->
<script setup lang="ts">
import {computed, nextTick, onBeforeUnmount, ref, watch} from 'vue'
import {fmtDur} from '../../lib/format'
import Icon from '../fluent/Icon.vue'

const props = defineProps<{
  /** 代理播放 URL（Preview.MediaURL 签发） */
  url: string
  /** 远端展示名（信息行用） */
  display: string
  /** 远端路径（密文相对路径），记忆续播 localStorage key；空则不落盘 */
  remote?: string
}>()

const v = ref<HTMLVideoElement | null>(null)

const playing = ref(false)
const muted = ref(false)
const ready = ref(false)
const failed = ref(false)
const duration = ref(0)
const current = ref(0)
const dragPos = ref<number | null>(null)
const volume = ref(1)

const shownPos = computed(() => dragPos.value ?? current.value)
const currentRemote = computed(() => props.remote ?? '')

/* ----------------------------------------------- 记忆续播（localStorage） */

const RESUME_PREFIX = 'cp.video.resume.'
/** 仅播放到 ≥2s 才落盘（避免初始化阶段覆盖） */
const RESUME_MIN = 2
/** 进度写盘节流 5s（timeupdate 频繁，只节流落盘，不影响 UI 进度） */
const RESUME_FLUSH_MS = 5000

function loadResume(remote: string): number | null {
  if (!remote) return null
  try {
    const raw = localStorage.getItem(RESUME_PREFIX + remote)
    if (!raw) return null
    const sec = Number(raw)
    return Number.isFinite(sec) && sec >= RESUME_MIN ? sec : null
  } catch {
    return null
  }
}

function saveResume(remote: string, sec: number) {
  if (!remote) return
  try {
    localStorage.setItem(RESUME_PREFIX + remote, String(sec))
  } catch {
    /* 隐私模式/已满：忽略 */
  }
}

function clearResume(remote: string) {
  if (!remote) return
  try {
    localStorage.removeItem(RESUME_PREFIX + remote)
  } catch {
    /* ignore */
  }
}

let resumeTimer: ReturnType<typeof setTimeout> | null = null
function flushResume() {
  if (resumeTimer) {
    clearTimeout(resumeTimer)
    resumeTimer = null
  }
  const el = v.value
  const remote = currentRemote.value
  if (!el || !remote) return
  saveResume(remote, el.currentTime)
}
function scheduleSave() {
  if (resumeTimer) return
  resumeTimer = setTimeout(() => {
    resumeTimer = null
    const el = v.value
    const remote = currentRemote.value
    if (!el || !remote) return
    // 暂停态/失败态不写记忆（用户可能不再继续看）
    if (playing.value) saveResume(remote, el.currentTime)
  }, RESUME_FLUSH_MS)
}

/* -------------------------------------------------- 控件 */

function togglePlay() {
  const el = v.value
  if (!el || failed.value) return
  if (el.paused) void el.play().catch(() => {})
  else el.pause()
}

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

function onTimeUpdate() {
  if (dragPos.value != null) return
  const el = v.value
  if (!el) return
  current.value = el.currentTime
  // 接近末尾（剩 1s 内）→ 清除记忆，下次进入从 0 开始
  if (el.duration > 0 && el.currentTime >= el.duration - 1 && currentRemote.value) {
    clearResume(currentRemote.value)
    return
  }
  scheduleSave()
}

function onPause() {
  // 暂停立即落盘（用户主动停的，记忆有意义）
  flushResume()
}

function onEnded() {
  // 播放结束清记忆（避免下次从末尾进入）
  const remote = currentRemote.value
  if (remote) clearResume(remote)
}

/* ----------------------------------------------- 切源：预载 + 记忆续播 seek（不自动播） */

watch(
  () => props.url,
  async (url) => {
    // 切源前 flush 旧源记忆
    flushResume()
    failed.value = false
    ready.value = false
    duration.value = 0
    current.value = 0
    dragPos.value = null
    if (!url) return
    const last = loadResume(currentRemote.value)
    await nextTick()
    const el = v.value
    if (!el) return
    el.load()
    // 有记忆则 seek 到该位置；不自动 play（保持手动）
    if (last != null) {
      const seekWhenReady = () => {
        if (el.readyState >= 1 && el.duration > 0) {
          el.currentTime = Math.min(last, Math.max(0, el.duration - 0.5))
        } else {
          setTimeout(seekWhenReady, 120)
        }
      }
      seekWhenReady()
    }
  },
  {immediate: true},
)

onBeforeUnmount(() => {
  flushResume()
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
      @durationchange="(e: Event) => {duration = (e.target as HTMLVideoElement).duration; ready = true}"
      @timeupdate="onTimeUpdate"
      @play="playing = true"
      @pause="onPause"
      @ended="onEnded"
      @error="failed = true"
      @click="togglePlay"
    ></video>

    <!-- 解码失败：仅展示原因，不再提供导出入口 -->
    <div v-if="failed" class="err" role="alert">
      <p class="err-msg">无法解码此媒体（或后端流式响应异常）。</p>
      <p class="err-sub">请用系统播放器打开该格式，或检查后端连接。</p>
    </div>

    <!-- 中央播放钮（暂停/未播时悬浮） -->
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
  background: #000;
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
  color: #d13438;
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
