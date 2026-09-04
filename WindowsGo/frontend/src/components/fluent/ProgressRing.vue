<!--
  ProgressRing.vue —— 环形进度（qfw ProgressRing 对应物）。
  视觉：SVG 描边环，底环 8% 灰、值环 accent 圆头；
  indeterminate 时整环旋转（qfw 不定环同款）；transparent 模式只留
  值环（信息页统计等大尺寸纯展示场景）。
-->
<script setup lang="ts">
import {computed} from 'vue'

const props = withDefaults(
  defineProps<{
    /** 0-100；indeterminate 时忽略 */
    value?: number
    indeterminate?: boolean
    /** 环直径（px），qfw 默认 32 */
    size?: number
    /** 描边粗细（px），qfw 默认 3 */
    stroke?: number
    /** 仅值环（无底色轨道）：用于叠加在图标/文案之上的装饰环 */
    transparent?: boolean
  }>(),
  {value: 0, indeterminate: false, size: 32, stroke: 3, transparent: false},
)

// 2πr 周长（值环精确到 pathLength=100 更省事：直接用 CSS stroke-dasharray）
const r = computed(() => (props.size - props.stroke) / 2)
const c = computed(() => 2 * Math.PI * r.value)
const dashOffset = computed(() => c.value * (1 - Math.min(100, Math.max(0, props.value)) / 100))
</script>

<template>
  <svg
    class="cp-ring"
    :class="{indet: indeterminate}"
    :width="size"
    :height="size"
    :viewBox="`0 0 ${size} ${size}`"
    role="progressbar"
    :aria-valuenow="indeterminate ? undefined : value"
  >
    <circle
      v-if="!transparent"
      class="track"
      :cx="size / 2"
      :cy="size / 2"
      :r="r"
      :stroke-width="stroke"
      fill="none"
    />
    <circle
      class="value"
      :cx="size / 2"
      :cy="size / 2"
      :r="r"
      :stroke-width="stroke"
      fill="none"
      stroke-linecap="round"
      :stroke-dasharray="c"
      :stroke-dashoffset="indeterminate ? 0 : dashOffset"
    />
  </svg>
</template>

<style scoped>
.cp-ring {
  flex: none;
}

.track {
  stroke: color-mix(in srgb, var(--text) 8%, transparent);
}

.value {
  stroke: var(--accent);
}

/* 不定进度：整环匀速旋转（值环只露一小段弧，靠 dasharray 视觉） */
.indet .value {
  stroke-dasharray: 25 200;
  transform-origin: 50% 50%;
  animation: ring-spin 1.2s linear infinite;
}

@keyframes ring-spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}
</style>
