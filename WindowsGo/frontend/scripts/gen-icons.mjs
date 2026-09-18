#!/usr/bin/env node
// gen-icons.mjs —— 生成 src/lib/icons.gen.ts（图标显式注册表）。
//
// 为什么需要它
//   原先 src/lib/icons.ts 用 import.meta.glob(..., {eager: true}) 把
//   assets/fluent-icons 与 assets/lucide 下的**全部** SVG 以 ?raw 内联进入口
//   chunk，实测 176 + 59 个文件共 437 KB（占 637 KB 入口 JS 的 69%），
//   而源码真正引用的只有 56 个。改成显式 import 后只打包用到的部分。
//
// 怎么判断「用到」
//   主扫描：遍历 src/**/*.{vue,ts}，抽出全部字符串字面量（'…' / "…" / 无反引号
//   插值的 `…`），与图标文件名求交集。已人工核对全部动态图标名来源（KIND_ICON /
//   kindIcon / bigIcon / NAV_ITEMS / BACKEND_META / iconOf / RoundMenu.item.icon /
//   StubPage.icon / Button 系列 icon prop），它们最终都落在源码里的普通字符串
//   字面量上，没有模板串拼名。
//   补扫描：`ICONS.question`、`ICONS['name']` 这种直接访问图标表的写法字面量扫不到，
//   单独用一条正则补扫（question 兜底正是此形）。
//
//   ⚠️ 三种引号必须各用一条正则独立扫描，不能写成 `A|B|C` 互斥分支 —— Vue 模板里
//   普遍存在 `:name="reveal ? 'hide' : 'eye'"` 的「双引号属性内套单引号」，互斥
//   分支会先吃掉整段双引号属性，内层单引号图标名永远匹配不到。实测曾因此把
//   eye / lock_open / mute / pause / play 五个在用图标误判为未引用而裁掉。
//
// 安全网
//   1. ALWAYS_KEEP：字面量扫不到、但运行期一定会用到的名字。question 是
//      Icon.vue 的兜底图标，以裸标识符 ICONS.question 访问，必须强制保留
//      （补扫描也能抓到，这里是双保险）。
//   2. MIN_EXPECTED：命中数低于下限说明扫描逻辑失效，直接报错退出，绝不写出
//      残缺注册表（宁可不生成，也不能静默丢图标）。
//   3. 扫描时排除本脚本的产物 icons.gen.ts —— 否则上一轮生成的 import 会被
//      当成「引用」，形成自锁，失效图标永远删不掉。
//   4. 内容无变化时不写盘，避免无意义的 mtime 抖动。
//
// 用法：npm run gen:icons（package.json 的 prebuild 会在 build 前自动执行）

import fs from 'node:fs'
import path from 'node:path'
import {fileURLToPath} from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const feRoot = path.resolve(here, '..')
const srcRoot = path.join(feRoot, 'src')

const FLUENT_REL = 'assets/fluent-icons'
const LUCIDE_REL = 'assets/lucide'
const OUT_ABS = path.join(srcRoot, 'lib', 'icons.gen.ts')

/** 字面量扫描覆盖不到、但运行期确实会用到的图标名（必须保留）。 */
const ALWAYS_KEEP = new Set(['question'])

/** 命中数下限：低于此值视为扫描失效，直接失败。当前基线为 56。 */
const MIN_EXPECTED = 30

/** 列出目录下全部 .svg 的基名（排序保证产物稳定，避免 diff 抖动）。 */
function listSvgs(rel) {
  const dir = path.join(srcRoot, rel)
  if (!fs.existsSync(dir)) throw new Error(`图标目录不存在：${dir}`)
  return fs
    .readdirSync(dir)
    .filter((f) => f.endsWith('.svg'))
    .map((f) => f.slice(0, -4))
    .sort()
}

/** 递归收集待扫描的源码文件（排除本脚本产物，见文件头安全网 3）。 */
function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, {withFileTypes: true})) {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) {
      walk(p, out)
    } else if (/\.(vue|ts)$/.test(e.name) && p !== OUT_ABS) {
      out.push(p)
    }
  }
  return out
}

const scanned = walk(srcRoot)
const sourceText = scanned.map((f) => fs.readFileSync(f, 'utf8')).join('\n')

/** 源码中出现过的全部字符串字面量。
 *
 *  三种引号必须各用一条正则**独立**扫描，不能写成 `A|B|C` 的互斥分支：
 *  Vue 模板里普遍存在 `:name="reveal ? 'hide' : 'eye'"` 这种「双引号属性
 *  内再套单引号」的写法，互斥分支会先命中双引号并把整段属性一起吃掉，
 *  内层的 'eye' 永远匹配不到 —— 实测曾因此把 eye / lock_open / mute /
 *  pause / play 五个在用图标误判为未引用而裁掉。
 *
 *  独立扫描的副作用是会把 `"reveal ? 'hide' : 'eye'"` 这类整段也收进集合，
 *  但它在图标表里查不到同名文件，属于无害噪音。 */
function collectLiterals() {
  const set = new Set()
  // 含 ${} 插值的模板串不是确定的名字，排除在外
  const res = [/'([^'\n\r]{1,64})'/g, /"([^"\n\r]{1,64})"/g, /`([^`\n\r$]{1,64})`/g]
  for (const re of res) {
    for (const m of sourceText.matchAll(re)) {
      if (m[1]) set.add(m[1])
    }
  }
  return set
}

/** 扫出直接访问图标表的写法：`ICONS.question`、`ICONS['lock_open']`。
 *
 *  这是字面量扫描唯一的真实盲区（question 兜底正是此形）。相比「词边界扫描」
 *  它精确得多：不会把 `Array.filter`、`arr.return`、`code` 这类普通标识符
 *  误当成图标名，因此可以在不吵人的前提下把盲区堵死。 */
function iconTableAccessors() {
  const out = new Set()
  for (const m of sourceText.matchAll(/\bICONS\s*\.\s*([A-Za-z_$][\w$]*)/g)) out.add(m[1])
  for (const m of sourceText.matchAll(/\bICONS\s*\[\s*['"]([\w-]{1,64})['"]\s*\]/g)) out.add(m[1])
  return out
}

const literals = collectLiterals()
const fluent = listSvgs(FLUENT_REL)
const lucide = listSvgs(LUCIDE_REL)
const lucideSet = new Set(lucide)

// 图标表直接访问（ICONS.question 这种）字面量扫不到，单独补扫
const accessors = iconTableAccessors()
const isUsed = (n) => literals.has(n) || ALWAYS_KEEP.has(n) || accessors.has(n)

const guarded = new Set([...accessors].filter((n) => !literals.has(n) && !ALWAYS_KEEP.has(n)))

// Lucide 同名会覆盖 Fluent，命中 Lucide 的名字无需再带一份 Fluent 实心图
const keepLucide = lucide.filter(isUsed)
const keepFluent = fluent.filter((n) => isUsed(n) && !lucideSet.has(n))
const kept = keepFluent.length + keepLucide.length

if (kept < MIN_EXPECTED) {
  console.error(
    `[gen-icons] 仅命中 ${kept} 个图标（下限 ${MIN_EXPECTED}），字面量扫描疑似失效。` +
      `已终止且未写入任何产物，请检查 src 目录是否可读、图标目录是否搬走。`,
  )
  process.exit(1)
}

// 断言 ALWAYS_KEEP 全部落地，防止日后改名时被静默丢掉
for (const n of ALWAYS_KEEP) {
  const ok = keepFluent.includes(n) || keepLucide.includes(n)
  if (!ok) {
    console.error(`[gen-icons] 强制保留的图标 "${n}" 不在图标目录中，请补齐资源文件。`)
    process.exit(1)
  }
}

const byteOf = (rel, n) => fs.statSync(path.join(srcRoot, rel, n + '.svg')).size
const sum = (rel, arr) => arr.reduce((a, n) => a + byteOf(rel, n), 0)
const droppedFluent = fluent.filter((n) => !keepFluent.includes(n))

const L = []
L.push('// 本文件由 scripts/gen-icons.mjs 自动生成，请勿手工编辑。')
L.push('//')
L.push(`// 图标来源：fluent ${fluent.length} 个 / lucide ${lucide.length} 个；`)
L.push(`// 按源码实际引用裁剪后仅打包 fluent ${keepFluent.length} 个 / lucide ${keepLucide.length} 个。`)
L.push('//')
L.push('// 重新生成：npm --prefix WindowsGo/frontend run gen:icons')
L.push('// 新增图标后若忘记重新生成，运行期会回退成 question 并在 dev 控制台告警。')
L.push('')
L.push('/* eslint-disable */')
L.push('')
keepFluent.forEach((n, i) => {
  L.push(`import f${i} from '../${FLUENT_REL}/${n}.svg?raw'`)
})
if (keepFluent.length) L.push('')
keepLucide.forEach((n, i) => {
  L.push(`import l${i} from '../${LUCIDE_REL}/${n}.svg?raw'`)
})
if (keepLucide.length) L.push('')

// 绑定名一律用序号 f0/f1、l0/l1，规避文件名含非法标识符字符的极端情况
L.push('/** 线性图标名集合（Lucide 源，Icon.vue 据此自动切 stroke 渲染）。 */')
L.push('export const STROKE_NAMES: ReadonlySet<string> = Object.freeze(')
L.push('  new Set([')
keepLucide.forEach((n) => L.push(`    ${JSON.stringify(n)},`))
L.push('  ]),')
L.push(')')
L.push('')
L.push('/** 图标表：Lucide 同名覆盖 Fluent（构建期已去重，无重叠键）。 */')
L.push('export const ICONS: Readonly<Record<string, string>> = Object.freeze({')
keepFluent.forEach((n, i) => L.push(`  ${JSON.stringify(n)}: f${i},`))
keepLucide.forEach((n, i) => L.push(`  ${JSON.stringify(n)}: l${i},`))
L.push('})')
L.push('')
L.push('/** 图标名（供 Icon.vue 的 name prop 静态校验）。 */')
L.push('export type IconName = keyof typeof ICONS')
L.push('')

const next = L.join('\n')
const prev = fs.existsSync(OUT_ABS) ? fs.readFileSync(OUT_ABS, 'utf8') : ''
const changed = next !== prev

if (changed) fs.writeFileSync(OUT_ABS, next, 'utf8')

// 报告与是否写盘无关，每次都打全，便于人工核对裁剪结果
console.log(
  `[gen-icons] ${changed ? '已写出' : '无变化'} ${path.relative(feRoot, OUT_ABS)}：` +
    `fluent ${keepFluent.length}/${fluent.length}（${sum(FLUENT_REL, keepFluent)}B）` +
    ` + lucide ${keepLucide.length}/${lucide.length}（${sum(LUCIDE_REL, keepLucide)}B）`,
)
console.log(`[gen-icons] 保留 fluent：${keepFluent.join(' ')}`)
console.log(
  `[gen-icons] 裁剪 fluent ${droppedFluent.length} 个` +
    `（${sum(FLUENT_REL, droppedFluent)}B）：${droppedFluent.join(' ')}`,
)
// 被裁掉的 lucide 必须逐个核对：lucide 名即引用名，裁错会直接变问号
const droppedLucide = lucide.filter((n) => !keepLucide.includes(n))
console.log(`[gen-icons] 裁剪 lucide ${droppedLucide.length} 个：${droppedLucide.join(' ')}`)

// 兜底命中必须显式暴露：它们绕过了字面量扫描、靠 ICONS 直接访问扫描捞回来，
// 说明有人在用非字面量方式引用图标，值得回头补一次人工核对。
if (guarded.size) {
  console.warn(
    `[gen-icons] 提示：${guarded.size} 个名字经 ICONS 直接访问扫描命中` +
      `（字面量扫描覆盖不到），已并入保留：${[...guarded].join(' ')}`,
  )
}
