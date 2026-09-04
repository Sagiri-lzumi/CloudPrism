<!--
  PushSettingCard.vue —— 可整卡点击的设置行（qfw PushSettingCard 对应物）。
  用于「选择本地目录」「清除缓存」等单动作项：整卡 hover 提亮，
  点击上抛 click；右侧默认 slot 放值文本/箭头等装饰。
-->
<script setup lang="ts">
import Icon from './Icon.vue'

const props = withDefaults(
  defineProps<{
    icon?: string
    title?: string
    content?: string
    disabled?: boolean
    /** 右侧是否显示默认的右箭头（隐藏后自定义 slot 更自由） */
    showArrow?: boolean
  }>(),
  {icon: '', title: '', content: '', disabled: false, showArrow: true},
)

const emit = defineEmits<{click: []}>()

function onKey(e: KeyboardEvent) {
  // 键盘可达性：Enter/Space 等效点击（整卡非原生按钮）
  if (!props.disabled && (e.key === 'Enter' || e.key === ' ')) {
    e.preventDefault()
    emit('click')
  }
}
</script>

<template>
  <div
    class="set-card clickable"
    :class="{disabled}"
    role="button"
    :tabindex="disabled ? -1 : 0"
    @click="!disabled && emit('click')"
    @keydown="onKey"
  >
    <span v-if="icon" class="set-icon">
      <Icon :name="icon" :size="17" />
    </span>
    <div class="set-body">
      <div class="set-title" v-if="title">{{ title }}</div>
      <div class="set-content" v-if="content">{{ content }}</div>
    </div>
    <div class="set-right">
      <slot />
      <Icon v-if="showArrow && $slots.default === undefined" name="chevron_right" :size="12" class="arrow" />
    </div>
  </div>
</template>

<style scoped>
.arrow {
  color: var(--text2);
}

.disabled {
  opacity: 0.4;
  pointer-events: none;
}
</style>
