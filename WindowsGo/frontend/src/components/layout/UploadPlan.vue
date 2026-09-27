<!--
  UploadPlan.vue —— 「确认上传」清单（直读本机路径通路的确认步骤）。

  为什么要有这一步：按钮通路选出来的是**本机绝对路径**，后端要自己 walk 一遍
  才知道有哪些文件；在此之前前端给不出任何预览。而「不小心选了一个几十 GB 的
  目录」代价很大（整个目录直接进加密上传队列），所以先扫描、把总数与总大小摆
  出来让用户确认。

  为什么清单由后端给而不是前端自己数：前端**拿不到**这些文件 —— 它只有路径
  字符串。而且清单与真正入队的展开规则必须同源（后端 expandLocalPaths），
  否则用户看到的数字就是骗人的。

  诚实性约定：后端扫描有上限，命中时 truncated 为真 —— 此时必须如实说明
  「只统计到前 N 项」，不能拿一个看起来完整的数字糊弄用户。
-->
<script setup lang="ts">
import {computed} from 'vue'
import {cancelUploadPlan, confirmUploadByPaths, uploadPlan} from '../../lib/store'
import {fmtSize} from '../../lib/format'
import ModalShell from './ModalShell.vue'
import Button from '../fluent/Button.vue'
import PrimaryButton from '../fluent/PrimaryButton.vue'
import Icon from '../fluent/Icon.vue'

/** 明细最多渲染这么多行：再多也只是刷屏，总数已经在上方给足。 */
const MAX_ROWS = 200

const plan = computed(() => uploadPlan.plan)
const rows = computed(() => (plan.value?.items ?? []).slice(0, MAX_ROWS))
/** 明细被截掉的条数（与「扫描被截断」是两回事，分别说明）。 */
const hiddenRows = computed(() => Math.max(0, (plan.value?.items.length ?? 0) - MAX_ROWS))

const canUpload = computed(
  () => !uploadPlan.scanning && !uploadPlan.uploading && (plan.value?.totalFiles ?? 0) > 0,
)
</script>

<template>
  <ModalShell
    :open="uploadPlan.open"
    title="确认上传"
    icon="send"
    :width="620"
    closable
    @close="cancelUploadPlan"
  >
    <!-- 扫描中：后端在 walk 本机路径，大目录可能要几秒 -->
    <p v-if="uploadPlan.scanning" class="up-hint">正在扫描所选内容…</p>

    <template v-else>
      <p v-if="uploadPlan.error" class="up-err">
        <Icon name="cancel" :size="14" />
        <span>{{ uploadPlan.error }}</span>
      </p>

      <template v-if="plan">
        <div class="up-sum">
          <div class="up-sum-line">
            <strong>{{ plan.totalFiles }}</strong> 个文件
            <span v-if="plan.totalDirs">· {{ plan.totalDirs }} 个子文件夹</span>
            <span class="up-sum-size">共 {{ fmtSize(plan.totalBytes) }}</span>
          </div>
          <div class="up-sum-dest">
            目标目录：{{ uploadPlan.remoteDir || '（密库根目录）' }}
          </div>
          <p v-if="plan.truncated" class="up-warn">
            <Icon name="info" :size="14" />
            <span>
              内容过多，统计只覆盖了前 {{ plan.totalFiles }} 个文件 —— 实际上传会包含
              所选范围内的全部文件，总数与总大小可能比上面更大。
            </span>
          </p>
        </div>

        <!-- 明细：相对路径 + 大小。超过 MAX_ROWS 只列前若干条，并如实说明。 -->
        <div v-if="rows.length" class="up-list">
          <div v-for="(it, i) in rows" :key="i" class="up-row">
            <Icon name="document" :size="14" class="up-row-ic" />
            <span class="up-row-name" :title="it.rel">{{ it.rel }}</span>
            <span class="up-row-size">{{ fmtSize(it.size) }}</span>
          </div>
          <p v-if="hiddenRows" class="up-hint">
            另有 {{ hiddenRows }} 项未列出（清单仅显示前 {{ MAX_ROWS }} 条）。
          </p>
        </div>
        <p v-else-if="!plan.totalFiles" class="up-hint">所选内容里没有文件。</p>
      </template>
    </template>

    <template #actions>
      <Button :disabled="uploadPlan.uploading" @click="cancelUploadPlan">取消</Button>
      <PrimaryButton :disabled="!canUpload" @click="confirmUploadByPaths">
        {{ uploadPlan.uploading ? '正在加入队列…' : '上传' }}
      </PrimaryButton>
    </template>
  </ModalShell>
</template>

<style scoped>
/* 概要块：数字先行，路径与告警紧随其后 */
.up-sum {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 10px;
}

.up-sum-line {
  font-size: 0.929rem;
  color: var(--text);
}

.up-sum-size {
  margin-left: 8px;
  color: var(--text2);
}

.up-sum-dest {
  font-size: 0.857rem;
  color: var(--text2);
  overflow-wrap: anywhere;
}

/* 明细列表：行式，各自一行文本；列表自身不滚动（面板由 ModalShell 统一滚） */
.up-list {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.up-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 26px;
  font-size: 0.857rem;
}

.up-row-ic {
  flex: none;
  color: var(--text2);
}

.up-row-name {
  flex: 1;
  min-width: 0;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.up-row-size {
  flex: none;
  font-size: 0.786rem;
  color: var(--text2);
}

.up-hint {
  margin: 6px 0;
  font-size: 0.857rem;
  color: var(--text2);
}

/* 错误行与截断告警：都带图标，语义不同但版式一致 */
.up-err,
.up-warn {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 8px;
  font-size: 0.857rem;
  overflow-wrap: anywhere;
}

.up-err {
  color: var(--err);
}

.up-warn {
  color: var(--warn);
}

.up-err svg,
.up-warn svg {
  flex: none;
  margin-top: 2px;
}
</style>
