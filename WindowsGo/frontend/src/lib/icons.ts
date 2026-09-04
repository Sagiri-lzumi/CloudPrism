// icons.ts —— Fluent 图标注册表。
//
// 源：assets/fluent-icons/*.svg —— 由 qfluentwidgets 内置资源（与 Python
// 版 GUI 完全同源的微软 Fluent 图形）一次性导出，共 174 枚浅色版。
// 图标以 SVG 原文内联进 Icon.vue，由 CSS fill: currentColor 随主题着色，
// 明暗两套共用同一份图形。
//
// 经 import.meta.glob 全量导入：eager + query '?raw' 在构建期把全部 SVG
// 文本打进产物（合计约 250KB，gzip 后 <80KB），换取任意 IconName 零
// 再编译直接可用。

const raw = import.meta.glob('../assets/fluent-icons/*.svg', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

function baseName(p: string): string {
  // 形如 ../assets/fluent-icons/folder.svg → folder
  const m = /\/([^/]+)\.svg$/.exec(p)
  return m ? m[1] : p
}

/** 全量图标表：key 为文件名（不含 .svg），值与 qfw FluentIcon 枚举名一一对应。 */
export const ICONS: Readonly<Record<string, string>> = Object.freeze(
  Object.fromEntries(Object.entries(raw).map(([p, s]) => [baseName(p), s])),
)

/** 图标名（字符串字面量联合，供 Icon.vue 的 name prop 静态校验）。 */
export type IconName = keyof typeof ICONS
