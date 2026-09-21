// audit-modal-stack.mjs —— 守卫「模态栈必须是真的模块级」。
//
// 为什么需要它：v1.6 出过一个静默且致命的问题 —— ModalShell 把模态栈写成
// `<script setup>` 里的顶层常量。那个块的顶层代码会被编译进 setup()，是**每个
// 组件实例跑一次**，于是每个浮层各持一份栈：
//   · z-index 全部算成深度 1 而互相打平 → 谁盖住谁只剩 DOM 顺序决定。Teleport 到
//     body 的锚点在组件挂载时就插好了，常驻的目录选择器永远排在向导之前 ⇒ 后开的
//     向导盖住选择器，用户点「浏览…」看着毫无反应（实测命中 .wiz-body）。
//   · isTop() 在单元素数组里恒真 ⇒ 一次 Esc 连关同屏所有浮层。
// 这类写法编译/类型检查全绿、跑单浮层场景也全绿，只有「两个浮层同屏」才暴露，
// 靠人眼复读代码挡不住 —— 所以钉成静态检查。
//
// 判定三条：
//   1. src/lib/modalStack.ts 存在，且导出 pushModal / popModal / isTopModal；
//   2. ModalShell.vue 从 lib/modalStack 引入这三个函数；
//   3. ModalShell.vue 的 <script setup> 里**不再**自带栈（const stack / symbol[]）。
// 检查前必须先剥注释：两个文件的注释里都直接引用了这些标识符做说明，
// 不剥注释就会把说明文字当成违规（css token 审计踩过同一个坑）。
//
// `--selftest` 用合成样本正反各跑一遍，证明它不是「永远通过」的橡皮图章。

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const STACK_TS = 'src/lib/modalStack.ts';
const SHELL = 'src/components/layout/ModalShell.vue';

/** 剥掉块注释与行注释（先块后行，避免 /* 位于 // 之后被漏掉）。 */
function stripComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/^[ \t]*\/\/.*$/gm, '');
}

/** 取出 <script setup> 段（取不到返回 null，交给调用方判为致命）。 */
function scriptSetup(src) {
  const m = src.match(/<script\s+setup[^>]*>([\s\S]*?)<\/script>/);
  return m ? m[1] : null;
}

/** 对一个 {stackTs, shellVue} 组合做全部判定，返回问题列表（空 = 通过）。 */
export function audit({ stackTs, shellVue }) {
  const problems = [];

  if (!stackTs) {
    problems.push(`${STACK_TS} 不存在 —— 模态栈必须落在这个模块里`);
    return problems;
  }
  const ts = stripComments(stackTs);
  for (const fn of ['pushModal', 'popModal', 'isTopModal']) {
    if (!new RegExp(`export\\s+function\\s+${fn}\\b`).test(ts)) {
      problems.push(`${STACK_TS} 未导出 ${fn}()`);
    }
  }

  if (!shellVue) {
    problems.push(`${SHELL} 不存在`);
    return problems;
  }
  const setup = scriptSetup(shellVue);
  if (setup === null) {
    problems.push(`${SHELL} 里找不到 <script setup>`);
    return problems;
  }

  const body = stripComments(setup);
  // ③ 自带栈 = 回归。只认「声明一个 symbol 数组」这一种形态，避免误伤普通数组。
  if (/const\s+stack\b/.test(body) || /symbol\s*\[\s*\]\s*=/.test(body)) {
    problems.push(
      `${SHELL} 的 <script setup> 里又出现了自带栈 —— 那是每个实例各一份，` +
        `会让 z-index 打平、Esc 连关。请改用 ${STACK_TS} 的 pushModal/popModal/isTopModal`,
    );
  }
  // ② 必须真的用上模块里的栈
  const imported = /from\s+['"][^'"]*modalStack['"]/.test(body);
  const used = ['pushModal', 'popModal', 'isTopModal'].every((f) => new RegExp(`\\b${f}\\b`).test(body));
  if (!imported || !used) {
    problems.push(
      `${SHELL} 未从 modalStack 引入并使用 pushModal/popModal/isTopModal` +
        `（imported=${imported} used=${used}）`,
    );
  }
  return problems;
}

/** 正反样本自检：好样本必须过、坏样本必须挂。 */
function selftest() {
  const goodTs = `export function pushModal(id: symbol): number { return 1 }\n` +
    `export function popModal(id: symbol): void {}\n` +
    `export function isTopModal(id: symbol): boolean { return true }\n`;
  const goodShell = `<script setup lang="ts">\n` +
    `import {isTopModal, popModal, pushModal} from '../../lib/modalStack'\n` +
    `const id = Symbol('modal')\n` +
    `depth.value = pushModal(id)\n` +
    `if (!isTopModal(id)) return\n` +
    `popModal(id)\n` +
    `</script>\n`;
  // 坏样本 = v1.6 的真实形态：栈写在 <script setup> 里
  const badShell = `<script setup lang="ts">\n` +
    `import {computed} from 'vue'\n` +
    `const stack: symbol[] = []\n` +
    `const id = Symbol('modal')\n` +
    `stack.push(id)\n` +
    `</script>\n`;
  const cases = [
    ['好样本（模块级栈）', { stackTs: goodTs, shellVue: goodShell }, 0],
    ['坏样本（<script setup> 自带栈）', { stackTs: goodTs, shellVue: badShell }, 1],
    ['坏样本（缺 modalStack 文件）', { stackTs: '', shellVue: goodShell }, 1],
  ];
  let bad = 0;
  for (const [name, fixture, expectFail] of cases) {
    const p = audit(fixture);
    const failed = p.length > 0;
    const ok = failed === (expectFail === 1);
    if (!ok) bad++;
    console.log(`${ok ? 'ok  ' : 'FAIL'} ${name} → ${failed ? `报错 ${p.length} 条` : '通过'}`);
  }
  if (bad) {
    console.error(`[audit-modal] 自检失败 ${bad} 例 —— 检查逻辑本身有问题`);
    process.exit(1);
  }
  console.log('[audit-modal] 自检通过（正反两向都符合预期）');
}

if (process.argv.includes('--selftest')) {
  selftest();
} else {
  const read = (rel) => {
    try {
      return fs.readFileSync(path.join(ROOT, rel), 'utf8');
    } catch {
      return '';
    }
  };
  const problems = audit({ stackTs: read(STACK_TS), shellVue: read(SHELL) });
  if (problems.length) {
    console.error('[audit-modal] 发现 ' + problems.length + ' 个问题：');
    for (const p of problems) console.error('  - ' + p);
    process.exit(1);
  }
  console.log('[audit-modal] 通过：模态栈落在 lib/modalStack.ts，ModalShell 不再自带栈');
}
