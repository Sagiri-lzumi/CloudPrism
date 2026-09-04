package storage

import "errors"

// 后端错误哨兵值。
//
// 存在的理由：三种后端的原生失败形态完全不同 —— 本地是 syscall errno、
// WebDAV 是 HTTP 状态码、百度是 JSON 里的 errno 字段。若把这些原样透出去，
// 上层（流式代理决定回 404 还是 502、传输队列决定是否重试、密库管理决定
// 提示「密码错误」还是「网络故障」）就得为每个后端写一套分支，
// 「后端可无缝互换」这个契约在错误路径上直接失效。
//
// 因此各实现负责在边界处把原生错误折叠成下面这几个语义值，上层只认哨兵：
//
//	errors.Is(err, ErrNotFound)  → 代理回 404
//	errors.Is(err, ErrBackend)   → 代理回 502 + 传输队列重试
//
// 对照 Python 端的异常类型映射见各条注释。
var (
	// ErrNotFound 表示目标路径不存在。
	// 对照 Python 的 FileNotFoundError（local_backend.py:59 等、
	// webdav_backend.py:143 的 404 分支、baidu_backend.py:280）。
	ErrNotFound = errors.New("路径不存在")

	// ErrNotDir 表示目标存在但不是目录。
	// 对照 Python 的 NotADirectoryError（local_backend.py:61）。
	ErrNotDir = errors.New("不是目录")

	// ErrIsDir 表示目标是目录而非文件。
	// 对照 Python 的 IsADirectoryError（local_backend.py:79、144）。
	ErrIsDir = errors.New("不是文件（目标是目录）")

	// ErrRange 表示字节范围参数非法（start<0 或 end<start）。
	// 对照 Python 的 ValueError（local_backend.py:85、webdav_backend.py:137、
	// baidu_backend.py:325）—— 这是调用方的编程错误，不是后端故障，
	// 因此单列一个哨兵，绝不与 ErrBackend 混淆。
	ErrRange = errors.New("非法字节范围")

	// ErrPathEscape 表示相对路径试图越出后端根目录。
	// 对照 local_backend.py:48 的 ValueError("路径越界…")。
	//
	// 这是**安全边界**而非普通校验：本地后端的根目录由用户在向导里选定，
	// 而路径片段可能来自云端目录树（攻击者可控 —— 例如 WebDAV/百度侧
	// 被塞进一个名为 "..\\..\\Windows" 的条目）。一旦逃逸，删除/覆盖
	// 就会落到用户任意文件上。
	ErrPathEscape = errors.New("路径越界，禁止访问后端根之外")

	// ErrBackend 表示后端通信或服务端错误（HTTP 非预期状态码、百度 errno≠0、
	// 网络中断等）。对照 Python 的 ConnectionError。
	//
	// 这是唯一「可能重试成功」的错误类，传输队列据此决定重试；
	// 其余哨兵都是确定性失败，重试只是浪费时间。
	ErrBackend = errors.New("存储后端故障")
)
