<!--
  StubPage.vue —— 页面级占位（transfers/settings 在 s6c 落地前的正式
  空态，避免 skeleton 残留）。视觉与 .empty-state 全局样式一致。
-->
<script setup lang="ts">
import Icon from '../components/fluent/Icon.vue'

withDefaults(
  defineProps<{
    icon: string
    title: string
    sub: string
    /** 引导按钮文案；空串不渲染按钮 */
    action?: string
  }>(),
  {action: ''},
)

const emit = defineEmits<{act: []}>()
</script>

<template>
  <section class="page-stub">
    <div class="empty-state">
      <Icon :name="icon" :size="40" class="dim" />
      <p class="lead">{{ title }}</p>
      <p class="sub">{{ sub }}</p>
      <button v-if="action" type="button" class="act" @click="emit('act')">{{ action }}</button>
    </div>
  </section>
</template>

<style scoped>
.page-stub {
  height: 100%;
}

.dim {
  color: var(--text2);
}

.act {
  height: 32px;
  padding: 0 16px;
  margin-top: 6px;
  font-family: inherit;
  font-size: 0.857rem;
  color: var(--text-on-accent);
  background: var(--accent);
  border: none;
  border-radius: var(--radius-ctrl);
}

.act:hover {
  background: var(--accent-hover);
}
</style>
