// audit-css-tokens.mjs —— CSS 自定义属性对账（定义 vs 使用）。
//
// 为什么需要它：CSS 的 `var(--x)` 拼错**不会报任何错**，只会静默取到空值 ——
// 圆角变 0、阴影消失、遮罩透明。`vue-tsc` 与 `vite build` 全绿也照样发现不了，
// 只能靠人眼或这类对账工具。改样式后跑一次：`npm run audit:css`。
//
// 判定：
//   - 「未定义即使用」必须为 0（否则就是拼错或漏定义）。
//   - 「已定义未使用」也应为 0（本项目的既定标准：无消费点的 token 一律删掉，
//     历史上连亚克力近似 token 都因无消费点被删除）。
//
// 豁免：由 JS 在运行时写到元素 style 上的变量（如 --tree-w / --split-l），
// 按设计不出现在 CSS 里，列在 RUNTIME_SET。

import {readFileSync, readdirSync, statSync} from 'node:fs'
import {join, relative} from 'node:path'
import {fileURLToPath} from 'node:url'

const SRC = join(fileURLToPath(new URL('.', import.meta.url)), '..', 'src')

/** 由前端 JS 运行时写入内联样式的自定义属性，CSS 侧自然"未定义"。 */
const RUNTIME_SET = new Set(['--tree-w', '--split-l', '--preview-w'])

// 定义：`--x: <值>` 且前面是行首/`;`/`{`/空白 —— 不能简单用 `^` 锚行首，
// 否则 `:root{--a:1px;--b:2px}` 这种写在一行的定义会被漏掉，审计就会误报
// 「未定义即使用」。（负向用例实测过：漏检时 --used-ok 也会被判为未定义。）
const DEF_RE = /(?:^|[;{\s])(--[a-zA-Z0-9-]+)\s*:/gm
const USE_RE = /var\(\s*(--[a-zA-Z0-9-]+)/g

/** 递归收集需要扫描的文件：styles/*.css + 所有 .vue（含 scoped <style>）。 */
function collect(dir) {
  const out = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      out.push(...collect(p))
      continue
    }
    if (name.endsWith('.vue')) out.push(p)
    if (name.endsWith('.css') && dir === join(SRC, 'styles')) out.push(p)
  }
  return out
}

function allMatches(text, re) {
  const out = []
  for (const m of text.matchAll(re)) out.push(m[1])
  return out
}

const defined = new Map()
const used = new Map()

for (const file of collect(SRC)) {
  const text = readFileSync(file, 'utf8')
  const rel = relative(SRC, file)
  for (const name of allMatches(text, DEF_RE)) {
    if (!defined.has(name)) defined.set(name, new Set())
    defined.get(name).add(rel)
  }
  for (const name of allMatches(text, USE_RE)) {
    if (!used.has(name)) used.set(name, new Set())
    used.get(name).add(rel)
  }
}

const fmt = (name, files) => `  ${name}  <- ${[...files].sort().join(', ')}`

console.log(`定义了 ${defined.size} 个 token，使用了 ${used.size} 个`)

const missing = [...used].filter(([name]) => !defined.has(name) && !RUNTIME_SET.has(name))
console.log('\n[未定义即使用] 必须为 0：')
console.log(missing.length ? missing.map(([n, f]) => fmt(n, f)).join('\n') : '  （无）')

// --dk-* 是深色调色板的单一真源，通过别名转发被间接消费，不参与"未使用"判定
const unused = [...defined].filter(
  ([name]) => !used.has(name) && !name.startsWith('--dk-'),
)
console.log('\n[已定义未使用] 无消费点的死 token：')
console.log(unused.length ? unused.map(([n, f]) => fmt(n, f)).join('\n') : '  （无）')

if (missing.length || unused.length) {
  console.error('\n审计未通过：请清理上面的 token。')
  process.exit(1)
}
console.log('\n审计通过。')
