<!--
  Card.vue —— 内容卡片容器（qfw CardWidget 对应物）。
  视觉：surface 底、radius 8、细描边 + 轻阴影；
  clickable 模式提供 hover 提亮与 :active 按压反馈，供密库卡片等使用。
-->
<script setup lang="ts">
withDefaults(
  defineProps<{
    /** 可点击（hover/按压反馈；点击事件由父级监听） */
    clickable?: boolean
    /** 内边距；none 用于整卡嵌入表格/网格的行列 */
    padding?: 'md' | 'none'
  }>(),
  {clickable: false, padding: 'md'},
)

const emit = defineEmits<{click: []}>()
</script>

<template>
  <div
    class="cp-card"
    :class="[clickable && 'clickable', padding === 'none' && 'no-pad']"
    @click="emit('click')"
  >
    <slot />
  </div>
</template>

<style scoped>
.cp-card {
  background: var(--surface);
  border: 1px solid var(--stroke-card);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow-card);
}

.md {
  padding: 16px;
}

.no-pad {
  padding: 0;
}

.clickable {
  cursor: default;
  transition: background var(--dur-fast) var(--ease), box-shadow var(--dur-fast) var(--ease);
}

.clickable:hover {
  background: var(--surface-hover);
  box-shadow: var(--shadow-card-hover);
}

.clickable:active {
  background: var(--surface-pressed);
  transform: scale(0.99);
}
</style>
