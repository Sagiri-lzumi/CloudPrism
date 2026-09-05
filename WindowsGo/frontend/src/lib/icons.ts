// icons.ts —— 图标注册表（Fluent 填充 + Lucide 线性 双源）。
//
// 源 A：assets/fluent-icons/*.svg —— qfluentwidgets 内置微软 Fluent 实心
// 图形（2048 坐标系，fill 型），作为默认与兜底；
// 源 B：assets/lucide/*.svg —— Lucide 线性图标（24 坐标系，stroke 型），
// 文件名与引用名一一对应（由 lucide 原图归一），**同名覆盖** fluent。
//
// STROKE_NAMES 记录所有来自 Lucide 的名字，Icon.vue 据此自动切 stroke
// 渲染（fill:none + stroke:currentColor + 24 viewBox），其余走 fill。

const rawFlu = import.meta.glob('../assets/fluent-icons/*.svg', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

const rawLuc = import.meta.glob('../assets/lucide/*.svg', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

function baseName(p: string): string {
  // 形如 ../assets/xxx/folder.svg → folder
  const m = /\/([^/]+)\.svg$/.exec(p)
  return m ? m[1] : p
}

const fluentIcons: Record<string, string> = Object.fromEntries(
  Object.entries(rawFlu).map(([p, s]) => [baseName(p), s]),
)
const lucideIcons: Record<string, string> = Object.fromEntries(
  Object.entries(rawLuc).map(([p, s]) => [baseName(p), s]),
)

/** 线性图标名集合（Lucide 源，stroke 渲染）。 */
export const STROKE_NAMES: ReadonlySet<string> = Object.freeze(
  new Set(Object.keys(lucideIcons)),
)

/** 全量图标表：Lucide 同名覆盖 Fluent；未覆盖名保持 Fluent 图形。 */
export const ICONS: Readonly<Record<string, string>> = Object.freeze({
  ...fluentIcons,
  ...lucideIcons,
})

/** 图标名（字符串字面量联合，供 Icon.vue 的 name prop 静态校验）。 */
export type IconName = keyof typeof ICONS
