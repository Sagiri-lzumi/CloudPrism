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

const label = computed(() => {
  const s = snap.value!
  return `${s.transferTasks} 个任务 · ${fmtSize(s.transferDone)} / ${fmtSize(s.transferTotal)}`
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
      <span class="pct">{{ percent }}%</span>
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
  background: var(--bg-page);
  border-top: 1px solid var(--divider);
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
