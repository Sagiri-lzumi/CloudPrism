// audit-css-tokens.mjs —— CSS 自定义属性对账（定义 vs 使用 + 深色转发完整性）。
//
// 为什么需要它：CSS 的 `var(--x)` 拼错**不会报任何错**，只会静默取到空值 ——
// 圆角变 0、阴影消失、遮罩透明。`vue-tsc` 与 `vite build` 全绿也照样发现不了，
// 只能靠人眼或这类对账工具。改样式后跑一次：`npm run audit:css`。
//
// 判定：
//   - 「未定义即使用」必须为 0（否则就是拼错或漏定义）。
//   - 「已定义未使用」也应为 0（本项目的既定标准：无消费点的 token 一律删掉，
//     历史上连亚克力近似 token 都因无消费点被删除）。
//   - 「深色转发」必须齐全且两支一致。theme.css 的深色是「--dk-* 单一真源 +
//     html[data-theme=dark] 与 @media 里的 html[data-theme=auto] 两支别名转发」，
//     漏转发不会报错：文字仍是浅色档的近黑色，压在深色底上**整段看不见**
//     （实测踩过：--heading 因「heading 不覆盖」的注释被两支同时漏掉，
//     深色下所有标题、设置卡标题、空态大标题几乎与背景同色）。
//     两支不一致则更隐蔽：手动切深色正常、跟随系统坏（或反之）。
//
// 豁免：由 JS 在运行时写到元素 style 上的变量（如 --insp-w），
// 按设计不出现在 CSS 里，列在 RUNTIME_SET。

import {readFileSync, readdirSync, statSync} from 'node:fs'
import {join, relative} from 'node:path'
import {fileURLToPath} from 'node:url'

const SRC = join(fileURLToPath(new URL('.', import.meta.url)), '..', 'src')

/** 由前端 JS 运行时写入内联样式的自定义属性，CSS 侧自然"未定义"。 */
/** 运行时注入的变量（JS :style 写入，不在 CSS 里定义）：
 *  · --insp-w：检查器分栏宽度（splitMove 拖动）；
 *  · --i：条目错峰入场序号（FilesView 的 v-for 注入，GridCard/.row 的
 *    animation-delay 消费）。带默认值兜底（var(--i, 0)）。 */
const RUNTIME_SET = new Set(['--insp-w', '--i'])

/** 刻意不做深色转发的 token：
 *  · 与明暗无关：字体/字号/圆角/动效/层级；
 *  · 毛玻璃的模糊半径与饱和度：不是颜色，明暗共用一档；
 *  · 刻意恒定：两类非模态黑罩（压在内容之上的"暗罩"，浅色下也不该变亮）；
 *  · 压色语义恒定：--text-on-accent（坐在品牌色填充上的字，两档都是白）。
 *  新增这类 token 时**必须**加进来 —— 加漏了会在这里大声失败，而不是静默。 */
const NO_DARK_NEEDED = new Set([
  '--font-family',
  '--font-size',
  '--radius-ctrl',
  '--radius-card',
  '--radius-round',
  '--z-raise',
  '--z-sheet',
  '--z-head',
  '--z-veil',
  '--z-menu',
  '--z-modal',
  '--z-toast',
  '--page-head-h',
  '--scrim-hint',
  '--scrim-media',
  '--dur',
  '--dur-fast',
  '--dur-spring',
  '--ease',
  '--ease-emphasized',
  '--ease-spring',
  '--ease-spring-soft',
  '--glass-blur',
  '--glass-sat',
  '--text-on-accent',
])

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

/** 取出 `选择器 { … }` 的块体。theme.css 里这几个块都是浅层平铺（无嵌套大括号），
 *  取到第一个 `}` 即可；若找不到选择器/括号不配对，直接抛错而不是静默跳过 ——
 *  主题文件被改名或结构重写时，这条护栏必须"响"。 */
function blockBody(text, selector) {
  const at = text.indexOf(selector)
  if (at < 0) throw new Error(`theme.css 里找不到选择器：${selector}（深色转发审计失效）`)
  const open = text.indexOf('{', at)
  const close = text.indexOf('}', open)
  if (open < 0 || close < 0) throw new Error(`选择器 ${selector} 的块不完整`)
  return text.slice(open + 1, close)
}

// 定位选择器前必须先剥掉注释：theme.css 的文件头注释里就写着
// `html[data-theme="dark"]` 这几个字，直接 indexOf 会命中注释、把紧随其后的
// :root 块当成深色块来比对（实测踩过：于是"两支不一致"刷出满屏 --dk-*，
// 而真正的漏转发被淹没）。
const themeText = readFileSync(join(SRC, 'styles', 'theme.css'), 'utf8').replace(
  /\/\*[\s\S]*?\*\//g,
  '',
)
const rootTokens = new Set(allMatches(blockBody(themeText, ':root'), DEF_RE))
const darkTokens = new Set(allMatches(blockBody(themeText, 'html[data-theme="dark"]'), DEF_RE))
const autoTokens = new Set(allMatches(blockBody(themeText, 'html[data-theme="auto"]'), DEF_RE))

// 浅色档里"需要深色另一套取值"的 token：--dk-* 是深色真源本身、NO_DARK_NEEDED 见上
const needForward = [...rootTokens].filter(
  (n) => !n.startsWith('--dk-') && !NO_DARK_NEEDED.has(n),
).sort()
const notInDark = needForward.filter((n) => !darkTokens.has(n))
const notInAuto = needForward.filter((n) => !autoTokens.has(n))
const onlyInDark = [...darkTokens].filter((n) => !autoTokens.has(n)).sort()
const onlyInAuto = [...autoTokens].filter((n) => !darkTokens.has(n)).sort()
const forwardBroken =
  notInDark.length || notInAuto.length || onlyInDark.length || onlyInAuto.length

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

console.log(`\n[深色转发] 浅色档需转发的 ${needForward.length} 个 token，须在两支里各转发一次：`)
console.log(
  notInDark.length
    ? `  漏在 html[data-theme="dark"]（手动深色下不可见）：\n${notInDark.map((n) => `    ${n}`).join('\n')}`
    : '  html[data-theme="dark"]：齐（无）',
)
console.log(
  notInAuto.length
    ? `  漏在 html[data-theme="auto"]（跟随系统深色下不可见）：\n${notInAuto.map((n) => `    ${n}`).join('\n')}`
    : '  html[data-theme="auto"]：齐（无）',
)
if (onlyInDark.length || onlyInAuto.length) {
  console.log('  两支转发集合不一致：')
  for (const n of onlyInDark) console.log(`    仅 dark 有：${n}`)
  for (const n of onlyInAuto) console.log(`    仅 auto 有：${n}`)
}

if (missing.length || unused.length || forwardBroken) {
  console.error('\n审计未通过：请清理上面的 token / 补齐深色转发。')
  process.exit(1)
}
console.log('\n审计通过。')
