<!--
  StatusBar.vue —— 底部状态条（对照 Python 状态栏）。
  左：状态点 + 文案三态——连接中（忙碌态直通后端 progress 文案，橙点）、
  已连接（密库名/后端/时长，10Hz 快照驱动）、未连接（灰点文案）。
  右：完整性统计进度（stats 未完成时）、可续传任务提醒（点按跳传输页）。
-->
<script setup lang="ts">
import {computed} from 'vue'
import {ui, navigate} from '../../lib/store'
import {fmtConnectSec, fmtPct} from '../../lib/format'
import Icon from '../fluent/Icon.vue'

const snap = computed(() => ui.snap)

// 统计阶段（statsDone=false 且总数>0 才显示：后端重启后自动跑一轮）
const statsLabel = computed(() => {
  const s = snap.value
  if (!s?.connected || s.statsDone || s.statsTotal <= 0) return ''
  const err = s.statsFailed ? '（部分失败）' : ''
  return `完整性核对中 ${s.statsDone}/${s.statsTotal}${err}`
})

const connected = computed(() => !!snap.value?.connected)
// 连接中：后端阶段文案经忙碌态直通（快照未连接时优先展示）
const connecting = computed(() => ui.opBusy && !connected.value)
const connText = computed(() => {
  const s = snap.value!
  const backend = s.backend ? ` · ${s.backend}` : ''
  return `${s.vaultName || '密库'}${backend} · 已连接 ${fmtConnectSec(s.connectedSec)}`
})
</script>

<template>
  <footer class="cp-status">
    <span
      class="status-chip"
      :class="connecting ? 'busy' : connected ? 'ok' : 'off'"
      :title="connText"
    >
      <span class="dot" />
      <span v-if="connecting" class="chip-text">{{ ui.opText || '正在连接…' }}</span>
      <span v-else-if="connected" class="chip-text">{{ connText }}</span>
      <span v-else class="chip-text">未连接</span>
    </span>

    <span class="spacer" />

    <span v-if="statsLabel" class="status-chip stat" :class="snap?.statsFailed ? 'bad' : ''">
      <Icon name="history" :size="12" />
      <span class="chip-text">{{ statsLabel }}</span>
    </span>

    <button
      v-if="snap?.resumeCount && snap.resumeCount > 0"
      type="button"
      class="status-chip resume"
      title="上次有任务未完成，点击查看"
      @click="navigate('transfers')"
    >
      <Icon name="cloud" :size="12" />
      <span class="chip-text">{{ snap.resumeCount }} 个任务可续传</span>
    </button>
  </footer>
</template>

<style scoped>
.cp-status {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 34px; /* 微调：与设置页卡片同 8px 圆角节奏更协调 */
  padding: 0 14px;
  font-size: 0.786rem;
  color: var(--text2);
  background: var(--bg-page);
  border-top: 1px solid var(--divider);
  user-select: none;
}

/* 状态胶囊：参考设置页 chip 风格（圆角+语义色底+透明层级） */
.status-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 22px;
  padding: 0 10px;
  white-space: nowrap;
  border-radius: var(--radius-round);
  background: transparent;
  border: none;
  color: var(--text2);
  font: inherit;
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

.status-chip.ok {
  color: var(--ok);
  background: color-mix(in srgb, var(--ok) 12%, transparent);
}

.status-chip.busy {
  color: var(--text);
  background: color-mix(in srgb, var(--warn) 16%, transparent);
}

.status-chip.off {
  color: var(--muted);
  background: color-mix(in srgb, var(--text) 6%, transparent);
}

.status-chip.stat {
  color: var(--text2);
  background: color-mix(in srgb, var(--text) 6%, transparent);
}

.status-chip.stat.bad {
  color: var(--err);
  background: color-mix(in srgb, var(--err) 12%, transparent);
}

.status-chip.resume {
  cursor: pointer;
  color: var(--warn);
  background: color-mix(in srgb, var(--warn) 14%, transparent);
}

.status-chip.resume:hover {
  background: color-mix(in srgb, var(--warn) 22%, transparent);
}

.dot {
  flex: none;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: currentColor;
}

/* 连接中：呼吸脉冲保留（替换原在 dot 上的动画） */
.status-chip.busy .dot {
  animation: status-pulse 1.4s var(--ease) infinite;
}

@keyframes status-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.45; }
}

.chip-text {
  font-variant-numeric: tabular-nums;
}

.spacer {
  flex: 1;
}
</style>
