<!--
  MessageBox.vue —— 模态对话框（qfw MessageBox + 前端输入对话框合一）。
  语义分三层：
    提示  —— 无输入，单「确定」；
    确认  —— 无输入，确定/取消（危险操作确认按钮转语义红）；
    输入  —— 带输入框（input.label），密码场景用 LineEdit 掩码 +
            内置「显示/隐藏」钮（对应计划第 8 条：Wails v2 无输入对话框，
            输入类交互全部由前端模态完成）。
  键盘：Esc=取消、Enter=确认；遮罩点击不关闭（防误触，qfw 同款语义）。
-->
<script setup lang="ts">
import {computed, nextTick, ref, watch} from 'vue'
import Icon from './Icon.vue'
import LineEdit from './LineEdit.vue'
import PrimaryButton from './PrimaryButton.vue'
import Button from './Button.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title?: string
    /** 说明/错误正文（纯文本，自动折行；复杂内容用默认插槽） */
    content?: string
    /** 输入对话框：标签文案；存在即渲染输入框 */
    inputLabel?: string
    /** 输入框为密码掩码 */
    password?: boolean
    /** 输入初值（密码场景请勿传入——仅用于明文编辑场景） */
    initial?: string
    /** 危险操作（确认按钮转语义红 + 图标感叹） */
    danger?: boolean
    showCancel?: boolean
    confirmText?: string
    cancelText?: string
  }>(),
  {
    title: '',
    content: '',
    inputLabel: '',
    password: false,
    initial: '',
    danger: false,
    showCancel: true,
    confirmText: '确定',
    cancelText: '取消',
  },
)

const emit = defineEmits<{
  /** 确认：input 模式载荷为输入值，否则 true */
  confirm: [payload: string | boolean]
  cancel: []
}>()

const inputVal = ref(props.initial)
const inputRef = ref<InstanceType<typeof LineEdit>>()
const panel = ref<HTMLElement>()

// 打开时重置输入并聚焦（密码场景聚焦输入框；无输入聚焦取消钮）
watch(
  () => props.open,
  async (open) => {
    if (!open) return
    inputVal.value = props.initial
    await nextTick()
    if (props.inputLabel) {
      inputRef.value?.select()
    } else {
      panel.value?.querySelector<HTMLElement>('.btn-cancel')?.focus()
    }
  },
)

function onConfirm() {
  emit('confirm', props.inputLabel ? inputVal.value : true)
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter') onConfirm()
  else if (e.key === 'Escape') emit('cancel')
}

const headIcon = computed(() => (props.danger ? 'cancel' : 'info'))
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="open" class="cp-mask" @keydown="onKey">
        <div ref="panel" class="cp-mbox" role="dialog" :aria-label="title" tabindex="-1">
          <div class="head">
            <span v-if="title" class="head-icon" :class="{danger}">
              <Icon :name="headIcon" :size="18" />
            </span>
            <div class="head-title">{{ title }}</div>
          </div>

          <div v-if="content" class="body-text">{{ content }}</div>
          <slot v-else />

          <div v-if="inputLabel" class="body-input">
            <div class="input-label">
              {{ inputLabel }}
              <span v-if="password" class="hint">（可点右侧眼睛显示/隐藏）</span>
            </div>
            <LineEdit
              ref="inputRef"
              v-model="inputVal"
              :password="password"
              autofocus
              @enter="onConfirm"
            />
          </div>

          <div class="actions">
            <Button v-if="showCancel" class="btn-cancel" @click="emit('cancel')">
              {{ cancelText }}
            </Button>
            <PrimaryButton :class="{danger}" @click="onConfirm">
              {{ confirmText }}
            </PrimaryButton>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.cp-mask {
  position: fixed;
  inset: 0;
  z-index: 1500;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.4); /* qfw 遮罩同款 */
}

.cp-mbox {
  width: min(420px, calc(100vw - 96px));
  padding: 20px;
  background: var(--surface);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow-pop);
  outline: none;
}

.head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}

.head-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 36px;
  height: 36px;
  color: var(--accent);
  background: var(--accent-soft);
  border-radius: var(--radius-ctrl);
}

.head-icon.danger {
  color: var(--err);
  background: color-mix(in srgb, var(--err) 12%, transparent);
}

.head-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--heading);
}

.body-text {
  font-size: 0.857rem;
  line-height: 1.5;
  color: var(--text2);
  white-space: pre-wrap; /* 保留错误栈换行 */
  overflow-wrap: anywhere;
}

.body-input {
  margin-top: 12px;
}

.input-label {
  margin-bottom: 6px;
  font-size: 0.857rem;
  color: var(--text);
}

.hint {
  margin-left: 6px;
  font-size: 0.786rem;
  color: var(--text2);
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}
</style>
