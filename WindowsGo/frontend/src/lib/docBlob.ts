// docBlob.ts —— 把 /s/ 上的文档按 Range 分片取全，拼成一个 Blob。
//
// 为什么必须这么做（2026-09-22 实测定型，别再退回「直接把令牌 URL 丢给 iframe」）：
// /s/ 端点对**没有 Range 头**的请求也回 206，并把响应截断到 MaxResponseBytes(2MiB)
// （handler.go 的 `end := min(rng.End, rng.Start+MaxResponseBytes-1)`），由
// proxy_test.go 钉死 —— 这是播放器的续请语义，不能改。
// 而浏览器内置 PDF 阅读器（PDFium）**只发一次无 Range 的请求**，拿到 206 + 截断体
// 之后不再续请（实测：3.4MiB 的 PDF 全程只有 1 个 /s/ 请求、共 2MiB，阅读器拿不到
// 尾部 xref 与后半段内容）。结论：>2MiB 的 PDF 在「直连 URL」方案下必然读不全。
//
// 既然阅读器不续请，就由前端替它续请：按 Content-Range 逐段拉，拼成完整 Blob，
// 再用 objectURL 交给它 —— objectURL 是一个完整、可随机访问的源，浏览器对它的行为
// 与「整份 200 响应」一致（这条已用普通静态服务器的 200 对照验证过：阅读器正常接管）。
//
// 代价与边界：整份进内存，故设上限（见调用方的 PDF_INLINE_MAX）；超限时由调用方
// 降级成「只给下载」，而不是把内存赌上。之所以不改成服务端「PDF 就不截断」：
// 那会动到视频共用的流式契约与已被测试钉住的截断行为，收益不抵风险。

/** 单次请求的段长：与 /s/ 的 MaxResponseBytes 对齐（要更多也只会被截断） */
const CHUNK = 2 << 20

export interface WholeDoc {
  blob: Blob
  /** 服务端声明的明文总长（取自 Content-Range 的 /total） */
  total: number
  /** 实际取到的字节数；与服务端忽略 Range 时的 total 一致 */
  bytes: number
}

/** 超过上限时抛出的错误（调用方据此给出「过大」的专门文案） */
export class DocTooLargeError extends Error {
  constructor(readonly total: number) {
    super('document exceeds inline limit')
    this.name = 'DocTooLargeError'
  }
}

/**
 * 用 Range 分片把一个 /s/ 文档取全。
 *
 * 兼容三种服务端行为：正常 206 分片、忽略 Range 回 200 全量、以及多段不足一次
 * 请求就结束（末段短于请求长度）。
 */
export async function fetchWholeViaRanges(url: string, maxBytes: number): Promise<WholeDoc> {
  const parts: ArrayBuffer[] = []
  let got = 0
  let total = -1

  // total < 0 表示「还不知道总长」（首轮）；知道之后按 got < total 收敛
  while (total < 0 || got < total) {
    const end = got + CHUNK - 1
    const resp = await fetch(url, {headers: {Range: `bytes=${got}-${end}`}})
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    const buf = await resp.arrayBuffer()
    if (buf.byteLength === 0) break // 服务端提前收尾，避免死循环
    // Content-Length 是权威段长：arrayBuffer() 与实际字节一致，但显式记一份更清楚
    parts.push(buf)
    got += buf.byteLength

    const m = /\/(\d+)\s*$/.exec(resp.headers.get('content-range') || '')
    if (m) {
      total = Number(m[1])
    } else {
      // 无 Content-Range：服务端忽略了 Range（回了 200 全量），这一轮就是全部
      total = got
    }
    if (total > maxBytes) throw new DocTooLargeError(total)
    // 非分片响应（200）：已经拿到整份，别再多要一轮
    if (resp.status !== 206) {
      total = got
      break
    }
  }

  return {blob: new Blob(parts, {type: 'application/pdf'}), total: total < 0 ? got : total, bytes: got}
}
