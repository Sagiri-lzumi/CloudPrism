// upload.ts —— 把「拖放 / 文件选择」的结果还原为「文件 + 相对路径」列表。
//
// 为什么单独做这一层：浏览器原生 API 在两处会丢掉目录信息，而密库上传必须
// 保留目录结构，否则「上传文件夹」会静默丢内容：
//
//  1. `dataTransfer.files` 对**文件夹**只给一个 0 字节占位 File，内部文件全都
//     拿不到。只有 `DataTransferItem.webkitGetAsEntry()` 能区分文件与目录，
//     并递归读下去。
//  2. `<input type="file" multiple>` 的 `File.name` 只有文件名，目录结构在
//     `File.webkitRelativePath` 里（仅 `webkitdirectory` 选择器会填）。
//
// 收集结果的**顺序即后端 multipart 中 files 的顺序**，`paths[i]` 是第 i 个
// 文件的相对路径。后端据此把 staging 镜像成嵌套目录，再交给
// appstate.UploadPaths 逐段加密——那条链路本来就支持相对路径，缺的只是
// 「web 层把路径传下去」。

/** 一个待上传条目：浏览器 File + 相对当前上传根的路径（POSIX 分隔符）。 */
export interface UploadItem {
  file: File
  /** 相对路径，如 `photos/2026/a.jpg`；纯文件上传时即文件名。 */
  rel: string
}

/** 单次拖放的条目上限。误拖整个盘符时及时止损，避免浏览器卡死。 */
const MAX_ITEMS = 5000

/**
 * 读取拖放内容（含文件夹递归展开）。
 *
 * 优先走 `items` + `webkitGetAsEntry`（唯一能识别目录的途径）；若浏览器不支持
 * 或拿不到 entry，退化为 `dataTransfer.files` 平铺（此时文件夹仍会丢内容，
 * 但至少文件不丢）。
 */
export async function collectDropped(dt: DataTransfer): Promise<UploadItem[]> {
  const entries: FileSystemEntry[] = []
  const items = dt.items
  for (let i = 0; i < items.length; i++) {
    const item = items[i]
    // 只处理 file 类别；kind === 'string' 是拖来的文本/URL，与上传无关
    if (item.kind !== 'file') continue
    const entry = item.webkitGetAsEntry?.() ?? null
    if (entry) entries.push(entry)
  }

  if (entries.length) {
    const out: UploadItem[] = []
    for (const entry of entries) await walkEntry(entry, '', out)
    if (out.length) return out
  }
  return collectFromFileList(dt.files)
}

/**
 * 读取 `<input type="file">` 或 `<input type="file" webkitdirectory>` 的选择结果。
 *
 * `webkitRelativePath` 形如 `sub/dir/a.txt`；普通（非目录）选择器下为空串，
 * 此时退回文件名，等价于「平铺上传」。
 */
export function collectFromFileList(files: FileList | File[]): UploadItem[] {
  return Array.from(files).map((file) => ({
    file,
    rel: normalizeRel(file.webkitRelativePath) || file.name,
  }))
}

/** 递归展开一个 FileSystemEntry；目录只贡献路径前缀，不产生条目。 */
async function walkEntry(entry: FileSystemEntry, prefix: string, out: UploadItem[]): Promise<void> {
  if (out.length >= MAX_ITEMS) return

  if (entry.isFile) {
    const file = await readFileEntry(entry as FileSystemFileEntry)
    if (!file) return // 读取失败（权限/文件被删）时跳过单个文件，不整体失败
    out.push({file, rel: prefix ? `${prefix}/${file.name}` : file.name})
    return
  }

  if (!entry.isDirectory) return
  const dir = entry as FileSystemDirectoryEntry
  const children = await readAllEntries(dir.createReader())
  const next = prefix ? `${prefix}/${entry.name}` : entry.name
  for (const child of children) await walkEntry(child, next, out)
}

/** `FileSystemFileEntry.file()` 的 Promise 化；失败返回 null 由调用方跳过。 */
function readFileEntry(entry: FileSystemFileEntry): Promise<File | null> {
  return new Promise((resolve) => {
    entry.file(
      (file) => resolve(file),
      () => resolve(null),
    )
  })
}

/**
 * 读完一个目录的全部子项。
 *
 * 注意：`readEntries` **一次最多返回 100 项**，必须反复调用直到返回空数组，
 * 否则大目录会被静默截断（这是最容易漏的一处）。
 */
function readAllEntries(reader: FileSystemDirectoryReader): Promise<FileSystemEntry[]> {
  const all: FileSystemEntry[] = []
  return new Promise((resolve) => {
    const step = () => {
      reader.readEntries(
        (batch) => {
          if (!batch.length) {
            resolve(all)
            return
          }
          all.push(...batch)
          step()
        },
        () => resolve(all), // 读失败时返回已收集部分，避免整批丢弃
      )
    }
    step()
  })
}

/** 归一化相对路径：反斜杠转正斜杠、剥掉全部前导 `./` 与 `/`（可叠加出现）。 */
function normalizeRel(raw: string | undefined): string {
  if (!raw) return ''
  return raw.replace(/\\/g, '/').replace(/^(?:\.\/|\/)+/, '')
}
