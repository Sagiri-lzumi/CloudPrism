<!--
  DirTree.vue —— 目录树抽屉（E 方案双栏抽屉的左栏）。
  懒加载：展开节点时才调 Files.List 拉子目录，缓存已加载目录避免重复请求。
  根节点=「我的密库」。点节点选中并跳转文件区；点箭头展开/收拢（不跳转）。
  渲染：把树拍平为 visible 列表（仅展开的节点子孙），用 depth 缩进。
-->
<script setup lang="ts">
import {computed, ref} from 'vue'
import {ui, listDir} from '../lib/store'
import {Files, unwrap} from '../lib/api'
import {showError} from '../lib/toast'
import Icon from '../components/fluent/Icon.vue'

interface TreeNode {
  remote: string
  label: string
  children: TreeNode[] | null
  expanded: boolean
  loading: boolean
}

const root = ref<TreeNode>({
  remote: '',
  label: '我的密库',
  children: null,
  expanded: true,
  loading: false,
})

const current = computed(() => ui.remote)

/** 拍平树为可见列表（仅展开节点的子孙），记录 depth 供缩进 */
const visible = computed(() => {
  const out: Array<{node: TreeNode; depth: number}> = []
  const walk = (node: TreeNode, depth: number) => {
    out.push({node, depth})
    if (node.expanded && node.children) {
      for (const c of node.children) walk(c, depth + 1)
    }
  }
  walk(root.value, 0)
  return out
})

async function loadChildren(node: TreeNode) {
  if (node.children !== null) return
  node.loading = true
  try {
    const entries = await Files.List(node.remote)
    const dirs = (entries ?? [])
      .filter((e) => e.isDir)
      .sort((a, b) => a.display.localeCompare(b.display, 'zh-CN'))
    node.children = dirs.map((e) => ({
      remote: e.remote,
      label: e.display,
      children: null,
      expanded: false,
      loading: false,
    }))
  } catch (e) {
    showError('加载目录失败：' + unwrap(e).message)
    node.children = []
  } finally {
    node.loading = false
  }
}

async function toggle(node: TreeNode) {
  node.expanded = !node.expanded
  if (node.expanded && node.children === null) {
    await loadChildren(node)
  }
}

async function open(node: TreeNode) {
  if (node.remote === current.value) return
  // listDir 走顶部静态导入：store 在本文件已静态引入，此处再 await import 既拆不出
  // 独立 chunk（vite 会告警 "dynamic import will not move module into another
  // chunk"），又会把该告警写到 stderr 上，导致 release.ps1 的 npm run build 被
  // PowerShell 判成 NativeCommandError 而中断打包。
  ui.crumbs = buildCrumbs(root.value, node.remote)
  await listDir(node.remote)
}

function buildCrumbs(node: TreeNode, target: string): Array<{label: string; remote: string}> {
  const chain: Array<{label: string; remote: string}> = []
  findPath(node, target, chain)
  return chain.slice(1) // 去掉根节点「我的密库」
}

function findPath(node: TreeNode, target: string, acc: Array<{label: string; remote: string}>): boolean {
  if (node.remote === target) {
    acc.push({label: node.label, remote: node.remote})
    return true
  }
  if (node.children) {
    for (const child of node.children) {
      if (findPath(child, target, acc)) {
        acc.push({label: child.label, remote: child.remote})
        return true
      }
    }
  }
  return false
}

// 根节点首次加载
void loadChildren(root.value)
</script>

<template>
  <div class="dt">
    <div
      v-for="item in visible"
      :key="item.node.remote || '__root__'"
      class="dt-row"
      :class="{on: item.node.remote === current, loading: item.node.loading}"
      :style="{paddingLeft: item.depth * 14 + 10 + 'px'}"
      @click="open(item.node)"
    >
      <!-- 展开箭头：有子目录或未加载才显示 -->
      <button
        v-if="item.node.children === null || (item.node.children && item.node.children.length > 0)"
        type="button"
        class="dt-arrow"
        @click.stop="toggle(item.node)"
      >
        <Icon :name="item.node.expanded ? 'chevron_down_med' : 'chevron_right_med'" :size="12" />
      </button>
      <span v-else class="dt-arrow-placeholder" />
      <Icon :name="item.node.remote === '' ? 'cloud' : 'folder'" :size="15" class="dt-ic" />
      <span class="dt-label">{{ item.node.label }}</span>
    </div>
  </div>
</template>

<style scoped>
.dt {
  /* 宿主是外壳侧栏的「目录」分区：底色/边框由侧栏统一提供，
     本组件只负责可滚动的树本身（v1.3 前它是文件页里独立的一栏，
     自带 surface 底与右分隔线） */
  flex: 1;
  min-height: 0;
  padding: 0 0 8px;
  overflow-y: auto;
  background: transparent;
}

.dt-row {
  display: flex;
  align-items: center;
  gap: 6px;
  height: 30px;
  padding: 0 8px 0 0;
  border-radius: var(--radius-ctrl);
  cursor: default;
  transition: background var(--dur-fast) var(--ease);
}

.dt-row:hover {
  background: var(--surface-hover);
}

.dt-row.on {
  background: var(--accent-soft);
}

.dt-arrow {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 16px;
  height: 16px;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: 2px;
  cursor: pointer;
}

.dt-arrow:hover {
  color: var(--text);
}

.dt-arrow-placeholder {
  flex: none;
  width: 16px;
  height: 16px;
}

.dt-ic {
  flex: none;
  color: var(--text2);
}

.dt-row.on .dt-ic {
  color: var(--accent);
}

.dt-label {
  flex: 1;
  min-width: 0;
  /* 与外壳侧栏导航项同字号：树与导航在同一栏里，字号不一致会显得是两套控件 */
  font-size: 0.857rem;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.dt-row.on .dt-label {
  color: var(--accent);
  font-weight: 600;
}
</style>
