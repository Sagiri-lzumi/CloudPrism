// icons.ts —— 图标注册表对外入口。
//
// 真实注册表在 icons.gen.ts，由 scripts/gen-icons.mjs 按「源码实际引用」裁剪生成：
//   · 源 A assets/fluent-icons/*.svg —— qfluentwidgets 内置微软 Fluent 实心图形
//     （2048 坐标系，fill 型），作为默认与兜底；
//   · 源 B assets/lucide/*.svg —— Lucide 线性图标（24 坐标系，stroke 型），文件名
//     与引用名一一对应（由 lucide 原图归一），**同名覆盖** fluent；
//   · STROKE_NAMES 记录所有来自 Lucide 的名字，Icon.vue 据此自动切 stroke 渲染
//     （fill:none + stroke:currentColor + 24 viewBox），其余走 fill。
//
// 为什么拆两文件：本文件曾用 import.meta.glob(..., {eager:true}) 把两目录下
// **全部** SVG 内联进入口 chunk —— 176 + 59 个共 437KB，占 637KB 入口 JS 的 69%，
// 而源码真正引用的只有 62 个。改为显式 import 后只打包用到的部分，入口砍掉约 400KB，
// 对局域网/手机弱网首屏收益明显。裁剪后若出现未注册的名字，Icon.vue 会回退成
// question 并在 dev 控制台告警。
//
// 新增图标：把 SVG 放进 assets 对应目录 → 源码里照常使用 → 跑一次
// `npm --prefix WindowsGo/frontend run gen:icons` 重新生成。
// （package.json 的 prebuild 也会在每次 build 前自动执行，正常无需手动跑。）

export {ICONS, STROKE_NAMES} from './icons.gen'
export type {IconName} from './icons.gen'
