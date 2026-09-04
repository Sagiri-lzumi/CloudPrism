// Package storage 提供统一的存储后端抽象与三种实现（本地文件夹 / WebDAV /
// 百度网盘）。
//
// 上层（加解密管线、流式代理、传输队列）只依赖 Backend 接口，不感知后端类型；
// 接口只负责搬运密文、文件头与目录结构，**不接触明文内容与主密码** ——
// 密钥派生与加解密全部在 pkg/pipeline 完成。
//
// 路径约定（两端一致）：所有路径参数均为相对后端根目录的相对路径，以 `/`
// 分隔，不含协议前缀，首尾斜杠可有可无。
//
// 对照 WindowsPy/src/cloudprism/storage/backend.py
package storage

import "context"

// DefaultChunk 是 UploadChunked 的默认分块大小（1 MiB）。
//
// 与设置页「分块大小」的默认档位一致（settings_store.py 的 chunk 默认 1MiB）。
// 注意百度网盘后端会忽略该值：XPAN 分片上传强制单片 4MiB。
const DefaultChunk = 1 << 20

// Entry 是目录条目（文件或子目录）。
//
// 对照 backend.py:18-23 的 RemoteEntry。Name 是云端显示名 ——
// 开启文件名加密的密库里它是 Base32 密文，由上层解密后才展示给用户。
type Entry struct {
	Name  string // 条目名称（可能已加密）
	IsDir bool   // 是否为目录
	Size  int64  // 字节大小（目录通常为 0）
}

// Backend 是统一存储后端接口，9 个方法与 Python 端 StorageBackend 一一对应。
//
// 全部方法以 ctx 为首参：取消与超时是传输队列的硬需求（用户点「取消全部」、
// 自动锁库、进程退出都要能立刻中断在途 I/O），Python 端靠线程 + 标志位轮询
// 实现，Go 端直接由 context 贯穿到 syscall。
//
// 错误语义见 errors.go —— 三后端的原生差异（HTTP 状态码、百度 errno、
// 文件系统错误）都在各自实现里抹平，上层只按 ErrNotFound / ErrRange /
// ErrBackend 等哨兵值分支。
//
// 对照 backend.py:26-80
type Backend interface {
	// ListDir 列出指定目录下的条目。
	// 路径不存在返回 ErrNotFound，不是目录返回 ErrNotDir。
	ListDir(ctx context.Context, path string) ([]Entry, error)

	// GetSize 取文件字节大小（断点续传与流式代理总量计算的基准）。
	// 路径不存在返回 ErrNotFound，是目录返回 ErrIsDir。
	GetSize(ctx context.Context, path string) (int64, error)

	// DownloadRange 按字节范围 [start, end]（**含两端**）拉取密文段。
	//
	// start<0 或 end<start 返回 ErrRange；文件不存在返回 ErrNotFound；
	// start 超出文件大小时返回**空切片而非错误**（Python 端同语义，
	// 上层据此判定「这一段没有数据」而不是「后端坏了」）。
	DownloadRange(ctx context.Context, path string, start, end int64) ([]byte, error)

	// UploadChunked 把本地文件分块写入远端，支持断点续传。
	//
	// chunk ≤ 0 时取 DefaultChunk。onProgress 可为 nil；非 nil 时按
	// 「已写入字节（含续传命中的部分）/ 总大小」回调，末值恒为 1.0。
	// 取消经 ctx 传达，实现应在每个分块边界检查。
	UploadChunked(ctx context.Context, local, remote string, chunk int, onProgress func(float64)) error

	// Head 取远端文件元信息（大小），断点续传基准。语义同 GetSize。
	Head(ctx context.Context, path string) (int64, error)

	// Mkdir 创建目录（含父目录）。已存在视为成功（幂等）。
	Mkdir(ctx context.Context, path string) error

	// Rename 重命名 / 移动。源不存在返回 ErrNotFound。
	Rename(ctx context.Context, old, new string) error

	// Delete 删除文件或目录。目标不存在返回 ErrNotFound。
	Delete(ctx context.Context, path string) error

	// Exists 判断路径是否存在（文件或目录均算）。
	// 后端故障时返回 error 而不是 false —— 否则网络抖动会被误判成
	// 「密库不存在」，用户看到的是「主密码错误」这类完全跑偏的提示。
	Exists(ctx context.Context, path string) (bool, error)
}

// RangeReaderInto 是 Backend 的**可选**优化接口：把区间直接写进调用方提供的
// 缓冲区，省掉一次 []byte 分配 + 拷贝。
//
// 流式代理与缩略图路径每 256KiB 就要取一次数，且缓冲区来自 sync.Pool
// （见 pkg/cryptox/pool.go）；若走 DownloadRange 则每次都要新分配再拷进池化
// 缓冲，GB/s 级别的解密吞吐下这笔分配会实打实地吃掉 CPU。
//
// 未实现本接口的后端由 ReadRangeInto 自动回退到 DownloadRange，因此
// **调用方无需做类型判断之外的任何适配**。
type RangeReaderInto interface {
	// DownloadRangeInto 读取 [start, end]（含两端）写入 dst，返回实际字节数。
	//
	// dst 容量不足时按容量截断读取（返回 n < end-start+1，不报错）；
	// start 超出文件大小时返回 0 与 nil error，与 DownloadRange 语义一致。
	DownloadRangeInto(ctx context.Context, path string, start, end int64, dst []byte) (int, error)
}

// ReadRangeInto 是 RangeReaderInto 的统一入口：后端实现了就走零拷贝路径，
// 没实现就回退到 DownloadRange 再拷贝。
//
// 把回退逻辑收在这里而不是散落到各调用点，是为了保证「优化接口缺席」
// 永远不会变成一个需要调用方处理的分叉。
func ReadRangeInto(ctx context.Context, b Backend, path string, start, end int64, dst []byte) (int, error) {
	if r, ok := b.(RangeReaderInto); ok {
		return r.DownloadRangeInto(ctx, path, start, end, dst)
	}

	data, err := b.DownloadRange(ctx, path, start, end)
	if err != nil {
		return 0, err
	}
	return copy(dst, data), nil
}
