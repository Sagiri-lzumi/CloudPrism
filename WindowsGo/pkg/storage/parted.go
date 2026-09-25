// parted.go —— 云端分卷：把「大文件」在远端拆成多个对象，对上层保持单文件语义。
//
// 为什么需要它：容器（.cpenc）在云端一直是一个整体对象，一个几 GB 的视频就是
// 一个几 GB 的对象。这在三处都吃亏 ——
//
//  1. 单对象过大时后端限制与超时都会咬人（百度 XPAN 单文件上限、
//     WebDAV 一次 PUT 的服务器超时、断线后整份重传）；
//  2. 无法并行取卷，流式播放只能顺序 Range；
//  3. 用户直观看到的也是「几个 G 的文件整份堆在云上」。
//
// 设计：**Parted 是 Backend 的装饰器**，插在后端构造之后、所有上层之前。
// 它把逻辑路径 path 映射成一组分卷对象 <path>.part-1 … <path>.part-N，
// 并在读侧把分卷拼回一段连续字节。上层的流式代理、解密管线、缩略图、
// 目录列表、下载 URL 全都只认 Backend 接口，因此**看不到分卷的存在**：
//
//	逻辑视图（上层）          远端（用户看到）
//	movie.mp4.cpenc     →     movie.mp4.cpenc.part-1
//	(头 + 密文，连续)          movie.mp4.cpenc.part-2
//	                          movie.mp4.cpenc.part-3
//
// 分卷边界直接切在密文字节上、**不改动加解密管线**（pkg/pipeline 仍按
// 1 MiB 分片并行加密成一整份容器，只是落盘后再切片上传），所以：
//   - 每个分卷内部都是原始密文的连续片段，拼接后与未分卷的容器逐字节相同；
//   - 头 51 字节永远在第 1 卷里，读头的路径（Range 映射、解密器）无需变动。
//
// 未达阈值（小于分卷尺寸）的文件仍然是单对象；历史单对象也照旧可读
// ——两者按「先看单对象、再看分卷」的顺序解析，单对象优先（见 resolve）。
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// PartSuffix 是分卷对象的命名中缀：<逻辑名>.part-<序号>（序号从 1 起）。
//
// 只有逻辑名以 .cpenc 结尾（即真的是密文容器）时才认这个后缀，
// 所以用户自己起名叫 `foo.part-1` 的文件（落盘是 foo.part-1.cpenc）
// 绝不会被误当成某个逻辑文件的分卷。
const PartSuffix = ".part-"

// DefaultPartSize 是分卷尺寸兜底值（设置为空/非法时用）。
//
// 与设置页「分卷尺寸」的默认档一致（transfer/chunk_index 的 64MB 档）：
// 64 MiB 一个对象，在网盘上传耗时、单对象风险与分卷数量之间取平衡 ——
// 一个 4 GiB 的视频是 64 卷，而不是几万个。
const DefaultPartSize = 64 << 20

// partLayoutTTL 是分卷表的缓存时长。
//
// 缓存的意义：流式播放一次取 2 MiB、整文件下载按 1 MiB 分片，若每次读都列一遍
// 父目录，网盘后端会被 PROPFIND 打爆。10 秒足够覆盖一次连续读取，
// 又短到别的实例改过文件后能自行收敛（写路径同一实例还会主动失效）。
const partLayoutTTL = 10 * time.Second

// RangeUploader 是 Backend 的**可选**优化接口：上传本地文件的 [off, off+length)
// 区间作为一个独立对象。
//
// 分卷上传的天然形态就是「切一段传一段」：实现该接口的后端（Local / WebDAV）
// 可以直接按区间读源文件，零额外磁盘占用；未实现的后端（百度：XPAN 分片
// 上传以整个文件为单位）由 Parted 退化为「切片落临时文件再整份上传」，
// 正确性不变，只是多一次本地读写。
//
// 与 RangeReaderInto 同一套思路：调用方只做一次类型断言，回退逻辑收在库里。
type RangeUploader interface {
	UploadRange(ctx context.Context, local, remote string, off, length int64, onProgress func(float64)) error
}

// partedPart 是分卷表里的一项。
type partedPart struct {
	name   string // 远端对象名（含 .part-N）
	index  int    // 1 起的序号
	start  int64  // 在逻辑文件里的起始偏移
	length int64  // 字节长度
}

// remoteLayout 是一个逻辑路径在远端的实际布局。
type remoteLayout struct {
	found  bool         // 单对象存在（历史布局，或未达分卷阈值）
	size   int64        // 逻辑文件总大小（分卷时为各卷之和）
	parted bool         // 走的是分卷布局
	parts  []partedPart // 按序号升序
	at     time.Time    // 解析时刻（TTL 用）
}

// known 表示该路径确实是一个存在的文件（单对象或分卷组）。
func (l *remoteLayout) known() bool { return l != nil && (l.found || l.parted) }

// Parted 是分卷装饰器。零值不可用，请用 NewParted 构造。
type Parted struct {
	inner    Backend
	partSize int64

	mu    sync.Mutex
	cache map[string]*remoteLayout
}

// 编译期断言：装饰器必须完整实现 Backend，并保住零拷贝读的优化接口。
var (
	_ Backend         = (*Parted)(nil)
	_ RangeReaderInto = (*Parted)(nil)
)

// NewParted 包装一个后端。partSize ≤ 0 时取 DefaultPartSize。
func NewParted(inner Backend, partSize int64) *Parted {
	if partSize <= 0 {
		partSize = DefaultPartSize
	}
	return &Parted{inner: inner, partSize: partSize, cache: make(map[string]*remoteLayout)}
}

// Inner 返回被包装的后端（Describe 等需要识别真实类型的场景用）。
func (p *Parted) Inner() Backend { return p.inner }

// PartSize 返回当前分卷尺寸（字节）。
func (p *Parted) PartSize() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.partSize
}

// SetPartSize 更新分卷尺寸，仅影响后续写入：分卷表是按实际列目录结果解析的，
// 因此改档位不会让已有的分卷组读不出来（只是下一个大文件按新尺寸切）。
func (p *Parted) SetPartSize(n int64) {
	if n <= 0 {
		return
	}
	p.mu.Lock()
	p.partSize = n
	p.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Backend 接口实现
// ---------------------------------------------------------------------------

// ListDir 列出目录条目，并把分卷折叠回它们所属的逻辑文件。
//
// 这是「用户看不到一堆 .part-N」的关键：远端有 64 个分卷，列表里只有 1 个
// 条目，大小是各卷之和（与未分卷时一致）。
//
// 顺序保持后端返回的顺序（没有分卷时逐项透传，不重排、不改变既有行为）。
func (p *Parted) ListDir(ctx context.Context, path string) ([]Entry, error) {
	entries, err := p.inner.ListDir(ctx, path)
	if err != nil {
		return nil, err
	}

	// 单对象优先：同名的普通条目存在时，忽略其分卷组（与 resolve 同一口径）
	plain := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			plain[e.Name] = true
		}
	}

	sizes := make(map[string]int64)
	order := make([]string, 0, 4)
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		base, ok := partLogicalOf(e.Name)
		if !ok || plain[base] {
			continue
		}
		if _, seen := sizes[base]; !seen {
			order = append(order, base)
		}
		sizes[base] += e.Size
	}
	if len(order) == 0 {
		return entries, nil
	}

	out := make([]Entry, 0, len(entries)-len(order))
	for _, e := range entries {
		if base, ok := partLogicalOf(e.Name); ok && !e.IsDir && !plain[base] {
			continue // 分卷对象不在列表里单独出现
		}
		out = append(out, e)
	}
	for _, base := range order {
		out = append(out, Entry{Name: base, Size: sizes[base]})
	}
	return out, nil
}

// GetSize 返回逻辑文件大小（分卷时为各卷之和）。
func (p *Parted) GetSize(ctx context.Context, path string) (int64, error) {
	if lay, err := p.resolve(ctx, path); err == nil && lay.known() {
		return lay.size, nil
	}
	return p.inner.GetSize(ctx, path)
}

// Head 语义同 GetSize（断点续传基准）。
func (p *Parted) Head(ctx context.Context, path string) (int64, error) {
	if lay, err := p.resolve(ctx, path); err == nil && lay.known() {
		return lay.size, nil
	}
	return p.inner.Head(ctx, path)
}

// DownloadRange 按逻辑偏移取一段字节：落到分卷上就是「按需取涉及的那几卷」。
func (p *Parted) DownloadRange(ctx context.Context, path string, start, end int64) ([]byte, error) {
	if start < 0 || end < start {
		return nil, fmt.Errorf("%w: 区间 [%d,%d] 非法", ErrRange, start, end)
	}
	data, handled, err := p.readLogical(ctx, path, start, end)
	if handled {
		return data, err
	}
	return p.inner.DownloadRange(ctx, path, start, end)
}

// DownloadRangeInto 是 RangeReaderInto 的实现：分卷布局下把各卷直接写进
// 调用方缓冲（零额外分配），单对象则原样交给后端的零拷贝路径。
func (p *Parted) DownloadRangeInto(ctx context.Context, path string, start, end int64, dst []byte) (int, error) {
	if start < 0 || end < start {
		return 0, fmt.Errorf("%w: 区间 [%d,%d] 非法", ErrRange, start, end)
	}
	lay, err := p.resolve(ctx, path)
	if err != nil || !lay.parted {
		return ReadRangeInto(ctx, p.inner, path, start, end, dst)
	}
	if start >= lay.size {
		return 0, nil // 越界读 → 0 字节，与后端语义一致
	}
	dir, _ := splitParent(strings.Trim(path, "/"))
	total := 0
	for _, part := range lay.parts {
		if part.start > end {
			break
		}
		if part.start+part.length-1 < start {
			continue
		}
		lo := max64(start-part.start, 0)
		hi := min64(end-part.start, part.length-1)
		n, err := ReadRangeInto(ctx, p.inner, joinPath(dir, part.name), lo, hi, dst[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// UploadChunked 上传本地文件：超过分卷尺寸就切成多个远端对象，
// 否则维持单对象（并在需要时清掉上一次留下的旧分卷）。
func (p *Parted) UploadChunked(
	ctx context.Context, local, remote string, chunk int, onProgress func(float64),
) error {
	st, err := os.Stat(local)
	if err != nil {
		return notFoundOrBackend(err, local)
	}
	if st.IsDir() {
		return fmt.Errorf("%w: 本地源不是文件：%s", ErrNotFound, local)
	}
	size := st.Size()
	partSize := p.PartSize()

	if partSize <= 0 || size <= partSize {
		// 单对象路径：与未分卷时逐字节等价的既有行为
		if err := p.inner.UploadChunked(ctx, local, remote, chunk, onProgress); err != nil {
			return err
		}
		p.forget(remote)
		// 文件由大变小（或重传）：清掉不再属于它的旧分卷，
		// 否则读路径会优先命中单对象、把分卷留成永久垃圾
		p.pruneParts(ctx, remote, 0)
		return nil
	}

	partCount := int((size + partSize - 1) / partSize)
	reporter := newPartProgress(onProgress, size)
	for i := 1; i <= partCount; i++ {
		off := int64(i-1) * partSize
		length := min64(partSize, size-off)
		if err := p.uploadRange(ctx, local, partName(remote, i), off, length, reporter.window(off, length)); err != nil {
			return err
		}
	}

	// 清理两件事，缺一读路径就会给出错的内容：
	//  1. 多余的旧分卷（这次的文件更小）；
	//  2. **同名的单对象** —— resolve 认「单对象优先」，留着它就会读到旧内容。
	p.pruneParts(ctx, remote, partCount)
	if err := p.deleteSingle(ctx, remote); err != nil {
		return err
	}
	p.forget(remote)
	return nil
}

// Mkdir 直接透传（目录不参与分卷）。
func (p *Parted) Mkdir(ctx context.Context, path string) error {
	return p.inner.Mkdir(ctx, path)
}

// Rename 重命名/移动：分卷组逐卷改名（保持序号），并清理目标上的旧布局。
func (p *Parted) Rename(ctx context.Context, old, new string) error {
	defer p.forget(old)
	defer p.forget(new)

	parts, err := p.listParts(ctx, old)
	if err != nil || len(parts) == 0 {
		return p.inner.Rename(ctx, old, new)
	}
	new = strings.Trim(new, "/")
	oldDir, oldBase := splitParent(strings.Trim(old, "/"))
	for _, name := range parts {
		idx, ok := parsePartIndex(name, oldBase)
		if !ok {
			continue
		}
		if err := p.inner.Rename(ctx, joinPath(oldDir, name), partName(new, idx)); err != nil {
			return err
		}
	}
	// 目标位置可能已有别的布局（旧单对象 / 更多分卷）：留着就会与新内容并存。
	// 清理失败不回滚重命名 —— 名字已经改完了，为清理报错反而更难解释。
	p.pruneParts(ctx, new, len(parts))
	p.deleteSingle(ctx, new)
	return nil
}

// Delete 删除逻辑文件：单对象 + 它的全部分卷一起删；目录路径透传。
//
// 对目录直接透传是安全的：分卷永远与逻辑文件同级，会随目录一起被删掉
// （调用方的递归删除也会逐个走到它们所属的逻辑文件上）。
func (p *Parted) Delete(ctx context.Context, path string) error {
	defer p.forget(path)

	parts, err := p.listParts(ctx, path)
	if err != nil || len(parts) == 0 {
		return p.inner.Delete(ctx, path)
	}
	dir, _ := splitParent(strings.Trim(path, "/"))
	for _, name := range parts {
		if derr := p.inner.Delete(ctx, joinPath(dir, name)); derr != nil && !errors.Is(derr, ErrNotFound) {
			return derr
		}
	}
	// 单对象可能不存在（纯分卷布局）：让 deleteSingle 处理 404
	return p.deleteSingle(ctx, path)
}

// Exists 判断路径是否存在：分卷组也算存在。
func (p *Parted) Exists(ctx context.Context, path string) (bool, error) {
	if lay, err := p.resolve(ctx, path); err == nil && lay.parted {
		return true, nil
	}
	return p.inner.Exists(ctx, path)
}

// ---------------------------------------------------------------------------
// 分卷布局解析
// ---------------------------------------------------------------------------

// resolve 解析一个逻辑路径在远端的布局，结果按 TTL 缓存。
//
// 解析顺序是**先单对象、后分卷**：单对象优先。理由是「两者并存」只可能来自
// 一次被打断的迁移（上传中途被杀），此时单对象是完整的旧内容、分卷是新的
// 半成品，读旧的远比读半成品安全。
//
// 列不出父目录时返回 error，调用方一律回退到直连后端 —— 一次列目录失败
// 不该让整个读取链路（含流式播放）失败。
func (p *Parted) resolve(ctx context.Context, path string) (*remoteLayout, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return &remoteLayout{}, nil // 后端根：不是文件
	}
	if lay, ok := p.cached(path); ok {
		return lay, nil
	}

	dir, base := splitParent(path)
	entries, err := p.inner.ListDir(ctx, dir)
	if err != nil {
		return nil, err
	}

	lay := &remoteLayout{at: time.Now()}
	var parts []partedPart
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if e.Name == base {
			lay.found = true
			lay.size = e.Size
			continue
		}
		if idx, ok := parsePartIndex(e.Name, base); ok {
			parts = append(parts, partedPart{name: e.Name, index: idx, length: e.Size})
		}
	}
	if len(parts) > 0 && !lay.found {
		sort.Slice(parts, func(i, j int) bool { return parts[i].index < parts[j].index })
		var off int64
		for i := range parts {
			parts[i].start = off
			off += parts[i].length
		}
		lay.parts = parts
		lay.parted = true
		lay.size = off
	}

	p.mu.Lock()
	if len(p.cache) > 4096 { // 防御性上限：长会话里不无限增长
		p.cache = make(map[string]*remoteLayout)
	}
	p.cache[path] = lay
	p.mu.Unlock()
	return lay, nil
}

// cached 取缓存（含 TTL 校验）。
func (p *Parted) cached(path string) (*remoteLayout, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	lay, ok := p.cache[path]
	if !ok || time.Since(lay.at) > partLayoutTTL {
		return nil, false
	}
	return lay, true
}

// forget 让某路径的布局缓存立即失效（写路径调用）。
func (p *Parted) forget(path string) {
	path = strings.Trim(path, "/")
	p.mu.Lock()
	delete(p.cache, path)
	p.mu.Unlock()
}

// readLogical 按逻辑偏移读取分卷布局。
//
// handled=false 表示「不是分卷布局」（单对象 / 目录 / 解析不了），
// 调用方据此回退给后端自己处理。
func (p *Parted) readLogical(ctx context.Context, path string, start, end int64) ([]byte, bool, error) {
	lay, err := p.resolve(ctx, path)
	if err != nil || !lay.parted {
		return nil, false, nil
	}
	if start >= lay.size {
		return []byte{}, true, nil // 越界读 → 空切片，与三后端语义一致
	}
	dir, _ := splitParent(strings.Trim(path, "/"))
	data, err := p.readParts(ctx, dir, lay, start, end)
	if err != nil && errors.Is(err, ErrNotFound) {
		// 缓存里的分卷表可能已过期（别的实例刚重传过）：失效后重试一次。
		// 重试仍失败就如实上报，由上层按后端错误处理。
		p.forget(path)
		if lay2, err2 := p.resolve(ctx, path); err2 == nil && lay2.parted {
			data, err = p.readParts(ctx, dir, lay2, start, end)
		}
	}
	return data, true, err
}

// readParts 取 [start,end] 覆盖到的各卷并拼接。
func (p *Parted) readParts(ctx context.Context, dir string, lay *remoteLayout, start, end int64) ([]byte, error) {
	want := end - start + 1
	if want > lay.size-start {
		want = lay.size - start
	}
	out := make([]byte, 0, want)
	for _, part := range lay.parts {
		if part.start > end {
			break
		}
		if part.start+part.length-1 < start {
			continue
		}
		lo := max64(start-part.start, 0)
		hi := min64(end-part.start, part.length-1)
		chunk, err := p.inner.DownloadRange(ctx, joinPath(dir, part.name), lo, hi)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
	}
	return out, nil
}

// listParts 列出某逻辑路径的全部分卷对象名（只看分卷，不看单对象）。
func (p *Parted) listParts(ctx context.Context, path string) ([]string, error) {
	path = strings.Trim(path, "/")
	dir, base := splitParent(path)
	entries, err := p.inner.ListDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if _, ok := parsePartIndex(e.Name, base); ok {
			names = append(names, e.Name)
		}
	}
	return names, nil
}

// pruneParts 删掉序号 > keep 的分卷（keep=0 即全删）。
//
// 失败只记不报：清理是「把上一次留下的垃圾收掉」，不该让本次上传/改名
// 因为一次删除失败而报错（内容已经正确写出了）。
func (p *Parted) pruneParts(ctx context.Context, remote string, keep int) {
	remote = strings.Trim(remote, "/")
	parts, err := p.listParts(ctx, remote)
	if err != nil {
		return
	}
	dir, base := splitParent(remote)
	for _, name := range parts {
		idx, ok := parsePartIndex(name, base)
		if !ok || idx <= keep {
			continue
		}
		_ = p.inner.Delete(ctx, joinPath(dir, name))
	}
}

// deleteSingle 删除同名的单对象；不存在视为成功。
func (p *Parted) deleteSingle(ctx context.Context, path string) error {
	err := p.inner.Delete(ctx, path)
	if err == nil || errors.Is(err, ErrNotFound) {
		return nil
	}
	// 有的后端对「删不存在的路径」返回的是笼统的后端错误：复核一次存在性再决定
	if ok, eerr := p.inner.Exists(ctx, path); eerr == nil && !ok {
		return nil
	}
	return err
}

// uploadRange 把本地文件的一段作为一个远端对象上传。
//
// 后端实现 RangeUploader 时零额外磁盘占用；否则切片落临时文件再整份上传。
func (p *Parted) uploadRange(
	ctx context.Context, local, remote string, off, length int64, onProgress func(float64),
) error {
	if ru, ok := p.inner.(RangeUploader); ok {
		return ru.UploadRange(ctx, local, remote, off, length, onProgress)
	}
	tmp, err := p.sliceToTemp(local, off, length)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	return p.inner.UploadChunked(ctx, tmp, remote, 0, onProgress)
}

// sliceToTemp 把 [off, off+length) 落成一个临时文件（无区间上传能力后端的回退）。
func (p *Parted) sliceToTemp(local string, off, length int64) (string, error) {
	src, err := os.Open(local)
	if err != nil {
		return "", notFoundOrBackend(err, local)
	}
	defer src.Close()

	dir, ok := paths.TempDir(true)
	if !ok {
		dir = os.TempDir() // data/ 不可写时退回系统临时目录（与传输链路同一策略）
	}
	f, err := os.CreateTemp(dir, "cloudprism-part-*")
	if err != nil {
		return "", fmt.Errorf("%w: 创建分卷临时文件失败: %v", ErrBackend, err)
	}
	name := f.Name()
	if _, err := io.Copy(f, io.NewSectionReader(src, off, length)); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("%w: 写出分卷临时文件失败: %v", ErrBackend, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("%w: 关闭分卷临时文件失败: %v", ErrBackend, err)
	}
	return name, nil
}

// ---------------------------------------------------------------------------
// 命名与工具
// ---------------------------------------------------------------------------

// partName 拼一个分卷对象名。
func partName(logical string, index int) string {
	return logical + PartSuffix + strconv.Itoa(index)
}

// splitParent 把远端相对路径拆成（父目录, 最后一段）；根级条目父目录为空串。
func splitParent(path string) (dir, base string) {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}

// joinPath 拼远端路径（父目录为空串时不加分隔符）。
func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// parsePartIndex 判断 name 是否是 logical 的分卷对象，返回其序号。
//
// 只认「逻辑名以 .cpenc 结尾」的组合：密库里所有内容对象都是 .cpenc 容器，
// 因此这个判据不会把用户自己起名的文件误判成分卷。
func parsePartIndex(name, logical string) (int, bool) {
	if logical == "" || !strings.HasSuffix(logical, protocol.FileExtension) {
		return 0, false
	}
	rest, ok := strings.CutPrefix(name, logical+PartSuffix)
	if !ok || rest == "" || strings.Contains(rest, ".") {
		return 0, false
	}
	idx, err := strconv.Atoi(rest)
	if err != nil || idx < 1 {
		return 0, false
	}
	return idx, true
}

// partLogicalOf 从一个分卷对象名反推逻辑名；不是分卷则 ok=false。
func partLogicalOf(name string) (string, bool) {
	i := strings.LastIndex(name, PartSuffix)
	if i <= 0 {
		return "", false
	}
	logical := name[:i]
	rest := name[i+len(PartSuffix):]
	if !strings.HasSuffix(logical, protocol.FileExtension) || rest == "" || strings.Contains(rest, ".") {
		return "", false
	}
	idx, err := strconv.Atoi(rest)
	if err != nil || idx < 1 {
		return "", false
	}
	return logical, true
}

// partProgress 把「第 i 卷内部的 0~1 进度」折算成整文件 0~1。
//
// 分卷是顺序上传的：第 i 卷（起始偏移 base、长度 length）报到 v 时，
// 已完成量就是 base + v×length。这样进度单调递增，切卷不会让进度条回退。
func newPartProgress(onProgress func(float64), total int64) *partProgress {
	return &partProgress{report: onProgress, total: total}
}

type partProgress struct {
	report func(float64)
	total  int64
}

// window 返回某卷（起始偏移 base、长度 length）对应的进度回调。
func (pp *partProgress) window(base, length int64) func(float64) {
	if pp.report == nil {
		return nil
	}
	return func(v float64) {
		switch {
		case v < 0:
			v = 0
		case v > 1:
			v = 1
		}
		done := float64(base) + v*float64(length)
		pp.report(done / float64(pp.total))
	}
}

// min64 / max64 是 Go 1.21 前常用的手写小工具（此处避免引入泛型依赖）。
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
