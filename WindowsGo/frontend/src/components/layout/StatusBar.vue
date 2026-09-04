<!--
  StatusBar.vue —— 底部状态条（极简）。
  连接详情（密库名/后端/时长）不在此实时展示——那属于「密库」页信息卡；
  这里只保留：最简连接态点 + 文案（未连接/已连接），以及瞬时辅助
  （连接中的阶段文案、完整性统计、可续传提醒，均为事件驱动非走秒）。
-->
<script setup lang="ts">
import {computed} from 'vue'
import {ui, navigate} from '../../lib/store'
import Icon from '../fluent/Icon.vue'

const snap = computed(() => ui.snap)

// 统计阶段（statsDone=false 且总数>0 才显示：后端重启后自动跑一轮）
// 快照无「已核对数」字段（statsDone 为完成标记 bool），故只展示总数
const statsLabel = computed(() => {
  const s = snap.value
  if (!s?.connected || s.statsDone || s.statsTotal <= 0) return ''
  const err = s.statsFailed ? '（部分失败）' : ''
  return `完整性核对中（共 ${s.statsTotal} 项）${err}`
})

const connected = computed(() => !!snap.value?.connected)
// 连接中：后端阶段文案经忙碌态直通（快照未连接时优先展示）
const connecting = computed(() => ui.opBusy && !connected.value)
</script>

<template>
  <footer class="cp-status">
    <span class="state" :class="connecting ? 'busy' : connected ? 'on' : 'off'">
      <span class="dot" />
      <span v-if="connecting" class="state-text">{{ ui.opText || '正在连接…' }}</span>
      <span v-else-if="connected" class="state-text">已连接</span>
      <span v-else class="state-text">未连接</span>
    </span>

    <span class="spacer" />

    <span v-if="statsLabel" class="aux stat" :class="snap?.statsFailed ? 'bad' : ''">
      <Icon name="history" :size="12" />
      {{ statsLabel }}
    </span>

    <button
      v-if="snap?.resumeCount && snap.resumeCount > 0"
      type="button"
      class="aux resume"
      title="上次有任务未完成，点击查看"
      @click="navigate('transfers')"
    >
      <Icon name="cloud" :size="12" />
      {{ snap.resumeCount }} 个任务可续传
    </button>
  </footer>
</template>

<style scoped>
.cp-status {
  display: flex;
  align-items: center;
  gap: 14px;
  height: 28px; /* 低调细条：连接详情已移入密库页，不再占用底部视觉 */
  padding: 0 12px;
  font-size: 0.786rem;
  color: var(--text2);
  background: var(--bg-page);
  border-top: 1px solid var(--divider);
  user-select: none;
}

/* 连接态：小圆点 + 简短文字（无密库名/后端/时长等实时详情） */
.state {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}

.state .dot {
  flex: none;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--text2);
  opacity: 0.5;
}

.state.on {
  color: var(--ok);
}

.state.on .dot {
  background: var(--ok);
  opacity: 1;
}

.state.busy .dot {
  background: var(--warn);
  opacity: 1;
  animation: status-pulse 1.4s var(--ease) infinite;
}

.state.off .dot {
  background: var(--warn);
  opacity: 0.9;
}

.state-text {
  color: var(--text2);
}

.state.on .state-text {
  color: var(--ok);
}

@keyframes status-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

.spacer {
  flex: 1;
}

/* 右侧瞬时辅助信息 */
.aux {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  white-space: nowrap;
  color: var(--text2);
}

.aux.stat.bad {
  color: var(--err);
}

.aux.resume {
  height: 22px;
  padding: 0 8px;
  font-family: inherit;
  font-size: inherit;
  color: var(--warn);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  cursor: pointer;
  transition: background var(--dur-fast) var(--ease);
}

.aux.resume:hover {
  background: color-mix(in srgb, var(--warn) 12%, transparent);
}
</style>
