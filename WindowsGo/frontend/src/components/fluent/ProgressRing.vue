<!--
  ProgressRing.vue —— 圆环进度（上传占位用）。
  为什么不复用 ProgressBar：用户明确要的是「一个圆圈的进度」，条状在网格卡片上
  没有落位处；圆环能压在缩略图面正中，且小尺寸下仍可辨认。

  单位约定：`ratio` 是 **0~1 的比例**（与 appstate.TaskView.progress 同单位），
  不是百分数 —— 名字里带 ratio 就是为了防止两边各写一套换算。
  ratio = null → 不定态（进度未知，例如本地暂存阶段）：画一段旋转的弧。
-->
<script setup lang="ts">
import {computed} from 'vue'

const props = withDefaults(
  defineProps<{
    /** 进度比例 0~1；null = 未知（不定态） */
    ratio?: number | null
    /** 圆环外径（px） */
    size?: number
  }>(),
  {ratio: null, size: 44},
)

// viewBox 固定 36×36，半径 15.5 → 周长 2πr；stroke-width 3 时描边落在 14~17，
// 留 1px 余量不会被 viewBox 裁掉。
const R = 15.5
const C = 2 * Math.PI * R

const pct = computed(() =>
  props.ratio == null ? 0 : Math.min(100, Math.max(0, props.ratio * 100)),
)
const offset = computed(() => C * (1 - pct.value / 100))
/** 小尺寸（列表行 22px）里塞百分比会糊成一团，只有网格卡那种大环才显示数字 */
const showNum = computed(() => props.size >= 36)
</script>

<template>
  <span
    class="ring"
    :class="{indet: ratio == null}"
    :style="{width: size + 'px', height: size + 'px'}"
    role="progressbar"
    :aria-valuenow="ratio == null ? undefined : Math.round(pct)"
    aria-valuemin="0"
    aria-valuemax="100"
  >
    <svg class="svg" viewBox="0 0 36 36" aria-hidden="true">
      <circle class="track" cx="18" cy="18" :r="R" />
      <circle
        v-if="ratio != null"
        class="bar"
        cx="18"
        cy="18"
        :r="R"
        :stroke-dasharray="C"
        :stroke-dashoffset="offset"
      />
      <!-- 不定态：固定 1/4 弧 + 整体旋转，与「进度未知」语义一致 -->
      <circle v-else class="bar" cx="18" cy="18" :r="R" :stroke-dasharray="`${C / 4} ${C}`" />
    </svg>
    <span v-if="showNum" class="num">{{ Math.round(pct) }}%</span>
  </span>
</template>

<style scoped>
.ring {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  /* 承托底：圆环常压在缩略图/网格卡上，没有这层底会看不清 */
  background: color-mix(in srgb, var(--surface) 88%, transparent);
  border-radius: var(--radius-round);
}

.svg {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  transform: rotate(-90deg); /* 从 12 点方向起画 */
}

.indet .svg {
  transform-origin: 50% 50%;
  animation: ringspin 1.1s linear infinite;
}

.track {
  fill: none;
  stroke: color-mix(in srgb, var(--text) 16%, transparent);
  stroke-width: 3;
}

.bar {
  fill: none;
  stroke: var(--accent);
  stroke-width: 3;
  stroke-linecap: round;
  transition: stroke-dashoffset var(--dur-fast) var(--ease);
}

.num {
  position: relative; /* 压在 svg 之上 */
  font-size: 0.714rem;
  font-variant-numeric: tabular-nums;
  color: var(--text);
}

@keyframes ringspin {
  to {
    transform: rotate(270deg);
  }
}
</style>
