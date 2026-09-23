<!--
  Card.vue —— 内容卡片容器（qfw CardWidget 对应物）。
  视觉：v1.01 起半透玻璃卡面（--glass-card，无模糊）、radius-card、细描边 + 轻阴影；
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
  background: var(--glass-card);
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
  transition: background var(--dur-fast) var(--ease);
}

/* 苹果风 hover：只提亮底色，不抬升阴影（分层靠明度差，
   原硬编码悬停阴影已随 token 化移除）。
   v1.01：hover/按压混在半透玻璃卡面上（不透明 surface-* 会把玻璃感
   在 hover 瞬间整个抹掉）。 */
.clickable:hover {
  background: color-mix(in srgb, var(--text) 5%, var(--glass-card));
}

.clickable:active {
  background: color-mix(in srgb, var(--text) 10%, var(--glass-card));
  transform: scale(0.99);
}
</style>
