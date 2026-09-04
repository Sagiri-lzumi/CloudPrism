<!--
  StatusBar.vue —— 底部状态条（对照 Python 状态栏）。
  左：连接状态点 + 密库名/后端/连接时长（10Hz 快照驱动，随帧刷新）；
  右：完整性统计进度（stats 未完成时）、可续传任务提醒（点按跳传输页）。
-->
<script setup lang="ts">
import {computed} from 'vue'
import {ui, navigate} from '../../lib/store'
import {fmtConnectSec, fmtPct} from '../../lib/format'

const snap = computed(() => ui.snap)

// 统计阶段（statsDone=false 且总数>0 才显示：后端重启后自动跑一轮）
const statsLabel = computed(() => {
  const s = snap.value
  if (!s?.connected || s.statsDone || s.statsTotal <= 0) return ''
  const err = s.statsFailed ? '（部分失败）' : ''
  return `完整性核对中 ${s.statsDone}/${s.statsTotal}${err}`
})

const connected = computed(() => !!snap.value?.connected)
const connText = computed(() => {
  const s = snap.value!
  const backend = s.backend ? ` · ${s.backend}` : ''
  return `${s.vaultName || '密库'}${backend} · 已连接 ${fmtConnectSec(s.connectedSec)}`
})
</script>

<template>
  <footer class="cp-status">
    <span class="seg left">
      <span class="dot" :class="connected ? 'on' : 'off'" />
      <span v-if="connected" class="conn">{{ connText }}</span>
      <span v-else class="conn off-text">未连接</span>
    </span>

    <span class="spacer" />

    <span v-if="statsLabel" class="seg stat" :class="snap?.statsFailed ? 'bad' : ''">
      {{ statsLabel }}
    </span>

    <button
      v-if="snap?.resumeCount && snap.resumeCount > 0"
      type="button"
      class="seg resume"
      title="上次有任务未完成，点击查看"
      @click="navigate('transfers')"
    >
      {{ snap.resumeCount }} 个任务可续传
    </button>
  </footer>
</template>

<style scoped>
.cp-status {
  display: flex;
  align-items: center;
  gap: 16px;
  height: 28px;
  padding: 0 12px;
  font-size: 0.786rem; /* 11px：状态条信息弱化一档 */
  color: var(--text2);
  background: var(--bg-page);
  border-top: 1px solid var(--divider);
  user-select: none;
}

.seg {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}

.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--text2);
  opacity: 0.5;
}

.dot.on {
  background: var(--ok);
  opacity: 1;
}

.dot.off {
  background: var(--warn);
  opacity: 1;
}

.off-text {
  color: var(--muted);
}

.spacer {
  flex: 1;
}

.stat.bad {
  color: var(--err);
}

.resume {
  height: 22px;
  padding: 0 8px;
  font-family: inherit;
  font-size: inherit;
  color: var(--warn);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  cursor: pointer;
}

.resume:hover {
  background: color-mix(in srgb, var(--warn) 12%, transparent);
}
</style>
