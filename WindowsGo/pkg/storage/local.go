package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Local 是本地文件夹存储后端：把任意本地目录当作存储根，方法直接映射到
// 文件系统调用。适合单机使用、局域网共享目录与开发调试。
//
// 本地后端不涉及网络，但落盘的加密文件格式与云端完全一致，整个目录可随时
// 迁移到 WebDAV 或百度网盘后端。
//
// 对照 WindowsPy/src/cloudprism/storage/local_backend.py
type Local struct {
	root string // 已 EvalSymlinks + Abs 规范化的绝对根路径
}

// 编译期断言：Local 同时实现 Backend 与可选的 RangeReaderInto。
var (
	_ Backend         = (*Local)(nil)
	_ RangeReaderInto = (*Local)(nil)
)

// NewLocal 构造本地后端。root 必须已存在且是目录 —— 与 Python 端一致
// （local_backend.py:28-31），刻意**不自动创建**：根目录是用户在向导里
// 明确选定的，静默 mkdir 会把「选错盘符/路径打错」这类用户失误掩盖成
// 「连接成功但密库是空的」，排查成本极高。
func NewLocal(root string) (*Local, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: 本地后端根目录为空", ErrBackend)
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: 解析根目录 %q 失败: %v", ErrBackend, root, err)
	}
	// 解析符号链接，使 root 成为真实物理路径。否则 resolve 里的
	// filepath.Rel 前缀比较会被「根是 junction」这类布局绕过。
	if resolved, err2 := filepath.EvalSymlinks(abs); err2 == nil {
		abs = resolved
	}

	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: 后端根目录不存在：%s", ErrNotFound, abs)
		}
		return nil, fmt.Errorf("%w: 检查后端根目录失败: %v", ErrBackend, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: 后端根不是目录：%s", ErrNotDir, abs)
	}

	return &Local{root: abs}, nil
}

// Root 返回规范化后的根目录绝对路径（供连接信息展示与测试断言使用）。
func (b *Local) Root() string { return b.root }

// resolve 把相对路径解析为 root 内的绝对路径，越界返回 ErrPathEscape。
//
// 对照 local_backend.py:37-49，但**比 Python 多两道显式检查**，因为
// filepath.Join 与 pathlib 的 `/` 运算符在绝对路径上的行为不同：
//
//   - pathlib：Path("C:/root") / "D:/evil" → **D:/evil**（右侧绝对路径直接
//     覆盖左侧），随后 relative_to 失败 → 抛 ValueError，安全；
//   - filepath.Join("C:\\root", "D:\\evil") → **C:\root\D:\evil**，
//     随后 filepath.Rel 成功 → **不报错，静默逃逸**。
//
// 因此这里先拒绝绝对路径与 ".." 前缀，Join 之后再核一次相对关系做双保险。
func (b *Local) resolve(path string) (string, error) {
	rel := strings.Trim(path, "/")
	if rel == "" {
		return b.root, nil // 空路径或 "/" 视为根
	}

	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(cleaned) || isDotDot(cleaned) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, path)
	}

	full := filepath.Join(b.root, cleaned)
	if back, err := filepath.Rel(b.root, full); err != nil || isDotDot(back) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, path)
	}
	return full, nil
}

// isDotDot 判断路径是否为 ".." 或以 ".." 开头（即会向上越出一级）。
func isDotDot(p string) bool {
	return p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator))
}

// resolveNonRoot 在 resolve 之上再拒绝「根本身」，供破坏性操作使用。
//
// 空路径与 "/" 按约定解析为根（ListDir/Exists 需要这个语义），但 Delete 与
// Rename 拿到根就是灾难：Delete("") 会 RemoveAll 掉用户选定的整个文件夹，
// Rename("", x) 会把后端根本身搬走，之后所有操作全部报 ErrNotFound。
//
// Python 端没有这道防护（delete("") 真的会 rmtree 根目录），只是上层从不
// 传空路径所以未暴露。Go 端显式拒绝：**这是一处有意的安全加固**，
// 且不可能破坏任何合法调用 —— 没有任何业务场景需要删除或移动后端根。
//
// 归类到 ErrPathEscape 而非 ErrBackend：它是确定性失败，绝不能被传输队列
// 当作「网络抖动」重试。
func (b *Local) resolveNonRoot(path string) (string, error) {
	p, err := b.resolve(path)
	if err != nil {
		return "", err
	}
	if p == b.root {
		return "", fmt.Errorf("%w: 禁止对后端根目录本身执行该操作", ErrPathEscape)
	}
	return p, nil
}

// ListDir 列出目录下条目。
//
// 对照 local_backend.py:55-71。符号链接等非常规条目按 Python 语义处理：
// is_file() 为假时 size 取 0，且**不调用 stat**（Python 那边对断链调用
// stat 会抛异常，靠 `if child.is_file()` 短路避开；Go 侧用 Type().IsRegular()
// 达到同样效果）。
func (b *Local) ListDir(ctx context.Context, path string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("%w: 读取目录 %s 失败: %v", ErrBackend, path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotDir, path)
	}

	items, err := os.ReadDir(p)
	if err != nil {
		return nil, fmt.Errorf("%w: 读取目录 %s 失败: %v", ErrBackend, path, err)
	}

	out := make([]Entry, 0, len(items))
	for _, item := range items {
		e := Entry{Name: item.Name(), IsDir: item.IsDir()}
		if !e.IsDir && item.Type().IsRegular() {
			if fi, err2 := item.Info(); err2 == nil {
				e.Size = fi.Size()
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// GetSize 取文件字节大小。对照 local_backend.py:73-80。
func (b *Local) GetSize(ctx context.Context, path string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	p, err := b.resolve(path)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return 0, fmt.Errorf("%w: 读取 %s 元信息失败: %v", ErrBackend, path, err)
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%w: %s", ErrIsDir, path)
	}
	return info.Size(), nil
}

// Head 取远端文件大小（断点续传基准）。本地后端与 GetSize 同义。
func (b *Local) Head(ctx context.Context, path string) (int64, error) {
	return b.GetSize(ctx, path)
}

// DownloadRange 按字节范围 [start, end]（含两端）读取。
//
// 对照 local_backend.py:82-96。两处必须逐字对齐的语义：
//   - start >= size → **返回空切片、不报错**（上层据此判定「无数据」）；
//   - end 收敛到 size-1，绝不越界读。
//
// 另注：Python 这里只判 `is_file()`，因此**目录也走 FileNotFoundError 分支**
// （与 GetSize 抛 IsADirectoryError 不同）。保持一致，避免两端在
// 「对目录取范围」时的错误分类不同。
func (b *Local) DownloadRange(ctx context.Context, path string, start, end int64) ([]byte, error) {
	if start < 0 || end < start {
		return nil, fmt.Errorf("%w: [%d, %d]", ErrRange, start, end)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, notFoundOrBackend(err, path)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: 读取 %s 元信息失败: %v", ErrBackend, path, err)
	}
	if info.IsDir() {
		// Python 这里只判 is_file()，因此目录走 FileNotFoundError 分支
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if start >= info.Size() {
		return []byte{}, nil // 区间落在文件末尾之后：无数据，但不是错误
	}

	realEnd := min(end, info.Size()-1)
	buf := make([]byte, realEnd-start+1)
	// 本地文件读取本身无法中断，取消只在入口检查
	if _, err := io.ReadFull(io.NewSectionReader(f, start, int64(len(buf))), buf); err != nil {
		return nil, fmt.Errorf("%w: 读取 %s 的 [%d,%d] 失败: %v", ErrBackend, path, start, end, err)
	}
	return buf, nil
}

// DownloadRangeInto 是 DownloadRange 的零拷贝版本：用 File.ReadAt 直接写入
// 调用方缓冲区。
//
// ReadAt 不依赖也不修改文件偏移量，因此**天然支持多 goroutine 并发分片读**
// 同一个句柄 —— 并行下载与流式代理都靠这一点，无需加锁。
func (b *Local) DownloadRangeInto(ctx context.Context, path string, start, end int64, dst []byte) (int, error) {
	if start < 0 || end < start {
		return 0, fmt.Errorf("%w: [%d, %d]", ErrRange, start, end)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	p, err := b.resolve(path)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return 0, notFoundOrBackend(err, path)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("%w: 读取 %s 元信息失败: %v", ErrBackend, path, err)
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if start >= info.Size() {
		return 0, nil
	}

	// 同时受「请求区间」与「dst 容量」两个上界约束
	want := min(end-start+1, int64(len(dst)))
	want = min(want, info.Size()-start)
	n, err := f.ReadAt(dst[:want], start)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("%w: 读取 %s 的 [%d,%d] 失败: %v", ErrBackend, path, start, end, err)
	}
	return n, nil
}

// UploadChunked 从本地文件流式写入目标路径，支持断点续传。
//
// 对照 local_backend.py:98-136。续传基准是目标已存在的字节数；目标比源大
// 视为异常，从头重传（Python 同语义）。
//
// **一处有意的行为差异**：Python 在「目标已与源等长」（续传无需再写）时
// 一个进度都不 yield，上层进度条会永远停在 99%；Go 端保证末值恒为 1.0。
func (b *Local) UploadChunked(
	ctx context.Context, local, remote string, chunk int, onProgress func(float64),
) error {
	// 必须在打开（并可能截断）目标之前检查取消：offset==0 时会以 O_TRUNC 打开，
	// 若任务在启动前就已被取消，少了这道检查就会把上次留下的半成品清零 ——
	// 用户点「取消全部」反而毁掉了本可续传的部分。
	if err := ctx.Err(); err != nil {
		return err
	}

	if chunk <= 0 {
		chunk = DefaultChunk
	}
	emit := func(v float64) {
		if onProgress != nil {
			onProgress(v)
		}
	}

	src, err := os.Open(local)
	if err != nil {
		return notFoundOrBackend(err, local)
	}
	defer src.Close()

	srcInfo, err := src.Stat()
	if err != nil {
		return fmt.Errorf("%w: 读取本地源文件 %s 失败: %v", ErrBackend, local, err)
	}
	if srcInfo.IsDir() {
		return fmt.Errorf("%w: 本地源不是文件：%s", ErrNotFound, local)
	}
	total := srcInfo.Size()

	dstPath, err := b.resolve(remote)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("%w: 创建目录 %s 失败: %v", ErrBackend, filepath.Dir(dstPath), err)
	}

	// 断点续传基准：目标已存在的字节数
	var offset int64
	if di, err2 := os.Stat(dstPath); err2 == nil && !di.IsDir() {
		offset = di.Size()
	}
	if offset > total {
		offset = 0 // 目标比源大，异常，重传
	}

	flags := os.O_RDWR | os.O_CREATE
	if offset == 0 {
		flags |= os.O_TRUNC // 对应 Python 的 "wb"
	}
	dst, err := os.OpenFile(dstPath, flags, 0o644)
	if err != nil {
		return fmt.Errorf("%w: 打开目标 %s 失败: %v", ErrBackend, remote, err)
	}
	defer dst.Close()

	buf := make([]byte, chunk)
	var written int64
	emitted := false
	for offset+written < total {
		if err := ctx.Err(); err != nil { // 每个分块边界检查取消
			return err
		}
		n, err := src.ReadAt(buf[:min(int64(chunk), total-offset-written)], offset+written)
		if n > 0 {
			if _, werr := dst.WriteAt(buf[:n], offset+written); werr != nil {
				return fmt.Errorf("%w: 写入 %s 失败: %v", ErrBackend, remote, werr)
			}
			written += int64(n)
			// 进度含续传命中的部分，与 Python 的 (offset+written)/total 一致
			emit(float64(offset+written) / float64(total))
			emitted = true
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("%w: 读取本地源文件 %s 失败: %v", ErrBackend, local, err)
		}
	}

	if !emitted {
		emit(1.0) // 空文件，或续传时目标已完整
	}
	return nil
}

// Mkdir 创建目录（含父目录），已存在视为成功。对照 local_backend.py:147-150。
func (b *Local) Mkdir(_ context.Context, path string) error {
	p, err := b.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return fmt.Errorf("%w: 创建目录 %s 失败: %v", ErrBackend, path, err)
	}
	return nil
}

// Rename 重命名 / 移动。对照 local_backend.py:152-159 的 shutil.move。
//
// 源与目标都在 root 之下，正常情况下必然同卷，os.Rename 足够；仍保留
// 「复制 + 删除」兜底，覆盖根目录跨卷 junction 这类罕见布局
// （shutil.move 同样会走 copy2 + unlink）。目录不做递归兜底：
// 跨卷移动目录树属于极端场景，失败时给出明确错误比静默半搬更安全。
func (b *Local) Rename(_ context.Context, old, new string) error {
	src, err := b.resolveNonRoot(old)
	if err != nil {
		return err
	}
	dst, err := b.resolveNonRoot(new)
	if err != nil {
		return err
	}

	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: 源路径不存在：%s", ErrNotFound, old)
		}
		return fmt.Errorf("%w: 检查源路径 %s 失败: %v", ErrBackend, old, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("%w: 创建目标目录失败: %v", ErrBackend, err)
	}

	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if info.IsDir() {
		return fmt.Errorf("%w: 移动目录 %s 失败: %v", ErrBackend, old, err)
	}

	// 跨卷兜底：复制后删源
	if cerr := copyFile(src, dst, info.Mode().Perm()); cerr != nil {
		return fmt.Errorf("%w: 移动 %s 失败: %v", ErrBackend, old, cerr)
	}
	if rerr := os.Remove(src); rerr != nil {
		return fmt.Errorf("%w: 已复制到 %s 但删除源失败: %v", ErrBackend, new, rerr)
	}
	return nil
}

// Delete 删除文件或目录树。对照 local_backend.py:161-169。
//
// 注意：接口文档写的是「删除文件或空目录」，但本地实现与 Python 一致地
// 用 RemoveAll **递归删除整棵子树**（shutil.rmtree）。上层删除文件夹时
// 依赖该行为，故不改成「仅空目录」。
func (b *Local) Delete(_ context.Context, path string) error {
	p, err := b.resolveNonRoot(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return fmt.Errorf("%w: 检查 %s 失败: %v", ErrBackend, path, err)
	}
	if err := os.RemoveAll(p); err != nil {
		return fmt.Errorf("%w: 删除 %s 失败: %v", ErrBackend, path, err)
	}
	return nil
}

// Exists 判断路径是否存在。对照 local_backend.py:171-173。
//
// 用 Lstat 而非 Stat：断链的符号链接在 Python 的 Path.exists() 下为假，
// 但条目本身确实占位；这里取「文件系统里有没有这个名字」的语义，
// 与 os.Lstat 一致，避免因链接目标缺失而让上层误判「密库不存在」。
func (b *Local) Exists(_ context.Context, path string) (bool, error) {
	p, err := b.resolve(path)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("%w: 检查 %s 失败: %v", ErrBackend, path, err)
	}
	return true, nil
}

// notFoundOrBackend 把文件系统错误折叠成 ErrNotFound / ErrBackend。
func notFoundOrBackend(err error, path string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	return fmt.Errorf("%w: 访问 %s 失败: %v", ErrBackend, path, err)
}

// copyFile 按固定缓冲复制文件，用于跨卷移动兜底。
func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	// 复制失败时不留半成品，否则续传会把截断文件当成已传部分
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
