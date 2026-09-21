<!--
  RecoveryCodeDlg.vue —— 恢复码一次性展示框（向导新建成功 / 密库页重新生成共用）。
  对照 Python RecoveryCodeDialog：Consolas 大字等宽分片展示 + 复制按钮 +
  「我已备份，进入」主钮。Esc/遮罩关闭 = 取消等待（不标记已备份）。

  浮层结构（遮罩/面板/页头/焦点/Esc）一律走 ModalShell —— 本组件只管内容。
-->
<script setup lang="ts">
import {ref, watch} from 'vue'
import Button from '../../components/fluent/Button.vue'
import PrimaryButton from '../../components/fluent/PrimaryButton.vue'
import ModalShell from '../../components/layout/ModalShell.vue'

const props = defineProps<{
  open: boolean
  code: string
}>()

const emit = defineEmits<{close: []}>()

// 恢复码展示：后端返回 16 字符原生码（无分隔符），按 4 段分片空格展示；
// 若已带连字符（防御性兼容）则原样展示。复制恒为原生内容。
const segments = () => {
  const c = props.code.trim().toUpperCase()
  return c.includes('-') ? c.split('-') : (c.match(/.{1,4}/g) ?? [])
}

// 必须是 ref：原先写成普通 let，模板读的是非响应式绑定，
// 于是「已复制」这个反馈永远不变 —— 点了复制却像没反应。
const copied = ref(false)
watch(
  () => props.open,
  (v) => {
    if (v) copied.value = false
  },
)

function onCopy() {
  // 剪贴板不可用时退化为「选中文本」，用户手动 Ctrl+C。
  // writeText 是异步的：同步 try/catch 兜不住它的 reject，
  // 不接 catch 会变成 unhandled rejection（实测会在控制台留下 pageerror）。
  const selectFallback = () => {
    const el = document.querySelector<HTMLElement>('.rc-code')
    if (!el) return
    const range = document.createRange()
    range.selectNodeContents(el)
    const sel = window.getSelection()
    sel?.removeAllRanges()
    sel?.addRange(range)
  }
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(props.code.trim()).catch(selectFallback)
  } else {
    selectFallback()
  }
  copied.value = true
}
</script>

<template>
  <ModalShell
    :open="open"
    title="保存恢复码"
    icon="save"
    tone="ok"
    :width="480"
    @close="emit('close')"
  >
    <p class="rc-tip">
      请复制并妥善备份此恢复码（建议离线保存）。忘记主密码时可凭它开库；
      恢复码一旦丢失将无法找回。此码只显示这一次，不会写入云端。
    </p>

    <div class="rc-code" dir="ltr" aria-label="恢复码内容">
      <span v-for="(s, i) in segments()" :key="i" class="rc-seg">{{ s }}</span>
    </div>

    <p class="rc-note">重新生成后旧恢复码立即失效；恢复码不会上传到云端。</p>

    <template #actions>
      <Button icon="copy" @click="onCopy">{{ copied ? '已复制' : '复制恢复码' }}</Button>
      <PrimaryButton icon="completed" @click="emit('close')">我已备份，进入密库</PrimaryButton>
    </template>
  </ModalShell>
</template>

<style scoped>
.rc-tip {
  margin: 0 0 12px;
  font-size: 0.857rem;
  line-height: 1.5;
  color: var(--text2);
}

.rc-code {
  display: flex;
  justify-content: center;
  gap: 14px;
  padding: 14px;
  margin-bottom: 10px;
  background: var(--bg-page);
  border: 1px dashed var(--stroke);
  border-radius: var(--radius-ctrl);
  user-select: all;
}

.rc-seg {
  font-family: Consolas, 'Cascadia Mono', monospace;
  font-size: 1.429rem; /* 20px：对照 Python Consolas 18pt 视觉权重 */
  font-weight: 600;
  letter-spacing: 2px;
  color: var(--heading);
}

.rc-note {
  margin: 0;
  font-size: 0.786rem;
  line-height: 1.4;
  color: var(--muted);
}
</style>
