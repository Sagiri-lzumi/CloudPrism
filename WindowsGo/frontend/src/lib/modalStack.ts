// modalStack.ts —— 模态栈（**真·模块级单例**）。
//
// 为什么必须单独成文件，而不是写在 ModalShell.vue 的 <script setup> 里：
// `<script setup>` 的顶层代码会被编译进 setup() 函数体，是**每个组件实例跑一次**，
// 不是每个模块跑一次。所以 `const stack = []` 写在那个块里，等于每个浮层各自持
// 有一份栈 —— 「谁在最上层」这件事根本无法跨浮层共享，两个后果都是静默的：
//
//   1. 层级打平：每个浮层入栈都得到深度 1 → z-index 全是 calc(--z-modal + 10)
//      → 谁盖住谁只剩 DOM 顺序决定。Teleport 到 body 的锚点在**组件挂载时**就
//      插好了，App.vue 里常驻的目录选择器锚点永远排在向导之前 ⇒ 后开的向导
//      反而盖住先开的（后挂的）选择器。
//      实测（v1.6 打包件，向导 → 选存放目录 → 点「浏览…」）：
//        pickerZ=1510，面板中心命中 .wiz-body，hitInsidePicker=false
//      —— 选择器整块被向导盖死，用户看到的就是「点浏览没反应」。
//   2. Esc 失守：isTop() 在单元素数组里恒真 ⇒ 每个浮层都以为自己是最上层
//      ⇒ 一次 Esc 把同屏所有浮层一起关掉（本栈存在的唯一理由就是防这个）。
//
// 教训：需要跨实例共享的状态，一律放 .ts 模块；.vue 的 <script setup> 里
// 只有「本实例自己的状态」才是安全的。

const stack: symbol[] = []

/** 入栈并返回本次入栈后的深度（1 起）。深度决定 z-index，必须来自同一个栈。 */
export function pushModal(id: symbol): number {
  stack.push(id)
  return stack.length
}

/** 出栈（重复出栈/未入栈都是安全的空操作）。 */
export function popModal(id: symbol): void {
  const i = stack.indexOf(id)
  if (i >= 0) stack.splice(i, 1)
}

/** 是否当前最上层 —— 只有它该响应 Esc。 */
export function isTopModal(id: symbol): boolean {
  return stack.length > 0 && stack[stack.length - 1] === id
}
