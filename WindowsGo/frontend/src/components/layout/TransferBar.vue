<!--
  TransferBar.vue —— 传输聚合进度条（活动传输时占位，对照 Python 底部
  传输栏：总进度 + 任务数，点「详情」进传输页逐任务管理）。
  驱动：10Hz 快照帧（transferActive/transferDone/transferTotal）。
-->
<script setup lang="ts">
import {computed} from 'vue'
import {ui, navigate} from '../../lib/store'
import {fmtSize, fmtPct} from '../../lib/format'
import ProgressBar from '../fluent/ProgressBar.vue'
import Button from '../fluent/Button.vue'
import Icon from '../fluent/Icon.vue'

const snap = computed(() => ui.snap)
const ratio = computed(() => {
  const t = snap.value?.transferTotal
  return t && t > 0 ? (snap.value!.transferDone / t) : 0
})
const percent = computed(() => fmtPct(ratio.value))

// 有字节口径才报「X / Y」；队列里只有删除任务时聚合恒为 0 字节，
// 写成「1 个任务 · 0 B / 0 B」是纯噪音（用户 2026-09-27：删除也要可见，
// 但别把没有的信息编出来）。此时只报任务数，进度条走 indeterminate。
const label = computed(() => {
  const s = snap.value!
  if (!s.transferTotal || s.transferTotal <= 0) return `${s.transferTasks} 个任务`
  return `${s.transferTasks} 个任务 · ${fmtSize(s.transferDone)} / ${fmtSize(s.transferTotal)}`
})

// 百分比槽**常驻**（.pct 有 min-width 的定宽槽），无字节口径时只清空文字 ——
// 直接 v-if 摘掉元素会让相邻的进度条与「详情」按钮跳位（本项目的铁律：
// 条件渲染的元素不许裸参与 flex 排版）。
const pctText = computed(() => {
  const t = snap.value?.transferTotal
  return t && t > 0 ? `${percent.value}%` : ''
})
</script>

<template>
  <transition name="bar">
    <div v-if="snap?.transferActive" class="cp-transfer">
      <Icon name="sync" :size="16" class="spin" />
      <span class="label">{{ label }}</span>
      <div class="track">
        <ProgressBar
          :value="percent"
          :indeterminate="!snap.transferTotal || snap.transferTotal <= 0"
        />
      </div>
      <span class="pct">{{ pctText }}</span>
      <Button class="detail" icon="chevron_right_med" @click="navigate('transfers')">
        详情
      </Button>
    </div>
  </transition>
</template>

<style scoped>
.cp-transfer {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 40px;
  padding: 0 16px;
  font-size: 0.857rem;
  color: var(--text2);
  /* 底栏外框（背景与顶边）由外壳 .app-foot 统一提供：
     本组件只负责内容，切换活动态时不再有一整行「又出现一条底边」的观感 */
  background: transparent;
}

.label {
  flex: none;
  white-space: nowrap;
}

.track {
  flex: 1;
  min-width: 0;
}

.pct {
  flex: none;
  min-width: 40px;
  text-align: right;
  color: var(--accent);
  font-variant-numeric: tabular-nums;
}

.spin {
  color: var(--accent);
  animation: cpspin 1.2s linear infinite;
}

@keyframes cpspin {
  to {
    transform: rotate(360deg);
  }
}

.bar-enter-active,
.bar-leave-active {
  transition: height var(--dur) var(--ease), opacity var(--dur) var(--ease);
}

.bar-enter-from,
.bar-leave-to {
  height: 0;
  opacity: 0;
  overflow: hidden;
}
</style>
