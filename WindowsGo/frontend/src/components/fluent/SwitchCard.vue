<!--
  SwitchCard.vue —— 开关设置行（qfw SwitchSettingCard 对应物）。
  用法：<SwitchCard v-model:checked="…" icon="…" title="…" content="…" />
-->
<script setup lang="ts">
import Icon from './Icon.vue'
import Switch from './Switch.vue'

const props = withDefaults(
  defineProps<{
    icon?: string
    title?: string
    content?: string
    /** 开关状态 */
    checked?: boolean
    disabled?: boolean
  }>(),
  {icon: '', title: '', content: '', checked: false, disabled: false},
)

const emit = defineEmits<{
  'update:checked': [v: boolean]
  /** 状态翻转（checked 已为新值），用于立即落盘 */
  change: [v: boolean]
}>()

function toggle(v: boolean) {
  emit('update:checked', v)
  emit('change', v)
}
</script>

<template>
  <div class="set-card">
    <span v-if="icon" class="set-icon">
      <Icon :name="icon" :size="17" />
    </span>
    <div class="set-body">
      <div class="set-title" v-if="title">{{ title }}</div>
      <div class="set-content" v-if="content">{{ content }}</div>
    </div>
    <div class="set-right">
      <Switch :model-value="checked" :disabled="disabled" @update:model-value="toggle" />
    </div>
  </div>
</template>
