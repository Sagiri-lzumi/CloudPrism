<!--
  ProgressBar.vue —— 线形进度条（qfw ProgressBar / Indeterminate 对应物）。
  视觉：4px 高圆角轨道，值条 accent；determinate 按 value 定宽，
  indeterminate 走「光带往返扫动」动画（qfw 不定进度同款语义）。
-->
<script setup lang="ts">
withDefaults(
  defineProps<{
    /** 0-100 百分比；indeterminate 时忽略 */
    value?: number
    /** 不定进度：任务总数未知时使用（向导派生阶段等） */
    indeterminate?: boolean
    /** 轨道颜色换主题语义色（成功/错误场景） */
    color?: 'accent' | 'ok' | 'err'
  }>(),
  {value: 0, indeterminate: false, color: 'accent'},
)
</script>

<template>
  <div class="cp-progress" role="progressbar" :aria-valuenow="indeterminate ? undefined : value">
    <div
      class="bar"
      :class="[indeterminate ? 'indet' : 'det', 'color-' + color]"
      :style="indeterminate ? undefined : {width: Math.min(100, Math.max(0, value)) + '%'}"
    ></div>
  </div>
</template>

<style scoped>
.cp-progress {
  position: relative;
  width: 100%;
  height: 4px;
  overflow: hidden;
  background: color-mix(in srgb, var(--text) 8%, transparent);
  border-radius: var(--radius-round);
}

.bar {
  height: 100%;
  border-radius: var(--radius-round);
}

.color-accent {
  background: var(--accent);
}

.color-ok {
  background: var(--ok);
}

.color-err {
  background: var(--err);
}

.det {
  transition: width var(--dur) var(--ease);
}

/* 不定进度：半透明亮带 1.4s 往返扫动 */
.indet {
  width: 30%;
  background: linear-gradient(90deg, transparent, var(--accent) 50%, transparent);
  animation: sweep 1.4s ease-in-out infinite;
}

@keyframes sweep {
  from {
    margin-left: -30%;
  }
  to {
    margin-left: 100%;
  }
}
</style>
