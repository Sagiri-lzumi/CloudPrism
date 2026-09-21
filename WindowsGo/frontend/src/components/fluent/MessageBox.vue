<!--
  MessageBox.vue —— 模态对话框（qfw MessageBox + 前端输入对话框合一）。
  语义分三层：
    提示  —— 无输入，单「确定」；
    确认  —— 无输入，确定/取消（危险操作确认按钮转语义红）；
    输入  —— 带输入框（input.label），密码场景用 LineEdit 掩码 +
            内置「显示/隐藏」钮（对应计划第 8 条：浏览器原生 prompt 无样式且不可控，
            输入类交互全部由前端模态完成）。
  键盘：Esc=取消（由 ModalShell 统一处理）；Enter=激活当前聚焦的按钮。
  遮罩点击不关闭（防误触，四处浮层同款语义）。

  关于 Enter 的落点：打开时把焦点放在「哪个按钮」上，Enter 就等于按哪个 ——
  所以这里刻意区分：危险操作聚焦「取消」（避免顺手一个回车就删了），
  普通确认聚焦「确定」（顺键盘流），输入框场景聚焦输入框、回车即提交。
  此前是「遮罩上拦 Enter 一律确认」+「焦点在取消钮上」，于是回车会同时触发
  确认与取消两个回调 —— 靠调用方顺序掩盖，这里改成唯一落点。
-->
<script setup lang="ts">
import {computed, nextTick, ref, watch} from 'vue'
import LineEdit from './LineEdit.vue'
import PrimaryButton from './PrimaryButton.vue'
import Button from './Button.vue'
import ModalShell from '../layout/ModalShell.vue'

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

// 打开时重置输入并选中（密码场景便于直接覆写）
watch(
  () => props.open,
  async (open) => {
    if (!open) return
    inputVal.value = props.initial
    if (!props.inputLabel) return
    await nextTick()
    inputRef.value?.select()
  },
)

function onConfirm() {
  emit('confirm', props.inputLabel ? inputVal.value : true)
}

const headIcon = computed(() => (props.danger ? 'cancel' : 'info'))
const headTone = computed(() => (props.danger ? ('danger' as const) : ('accent' as const)))

/** 打开时的焦点落点（ModalShell 负责聚焦，此处只决定落在哪个控件上）。 */
const autofocusSel = computed(() => {
  if (props.inputLabel) return '.cp-line-edit input'
  if (props.danger && props.showCancel) return '.btn-cancel'
  return '.btn-confirm'
})
</script>

<template>
  <ModalShell
    :open="open"
    :title="title"
    :icon="title ? headIcon : ''"
    :tone="headTone"
    :width="420"
    :autofocus="autofocusSel"
    @close="emit('cancel')"
  >
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

    <template #actions>
      <Button v-if="showCancel" class="btn-cancel" @click="emit('cancel')">
        {{ cancelText }}
      </Button>
      <PrimaryButton class="btn-confirm" :danger="danger" @click="onConfirm">
        {{ confirmText }}
      </PrimaryButton>
    </template>
  </ModalShell>
</template>

<style scoped>
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
</style>
