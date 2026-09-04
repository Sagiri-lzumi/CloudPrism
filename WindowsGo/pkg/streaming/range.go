package streaming

import (
	"regexp"
	"strconv"
)

// ByteRange 是明文字节区间（含两端），语义对齐
// proxy_server.py:44-66 的 parse_range_header 返回值。
type ByteRange struct {
	Start int64 // 明文起始偏移（含）
	End   int64 // 明文结束偏移（含）
}

// rangeRe 匹配 "bytes=start-end"（两端均可省略）：
// Python 用 re.search 只取第一个匹配，无完整匹配时回退全文件，
// Go 用 FindStringSubmatch 语义一致。
var rangeRe = regexp.MustCompile(`bytes=(\d*)-(\d*)`)

// ParseRangeHeader 解析 HTTP Range 请求头为明文区间（含两端）。
//
// 返回 ok=false 表示区间非法（start 越界或 start>end），调用方必须回
// 416 而不能回退整文件 —— 否则播放器的尾部探测（bytes=N-）会触发
// 全量下载（proxy_server.py:50-52 的注释同因）。
//
// 边界语义逐条对照 proxy_server.py:44-66：
//   - 无 Range 头 / 格式不匹配 / total<=0 → 全文件 [0, max(total-1, 0)]
//   - end 省略（bytes=N-）→ 取到文件末尾
//   - end 超过总量 → 截断到 total-1
//   - start 越界（>= total）或 start>end → ok=false（416）
func ParseRangeHeader(header string, total int64) (r ByteRange, ok bool) {
	if header == "" || total <= 0 {
		return ByteRange{Start: 0, End: max(total-1, 0)}, true
	}
	m := rangeRe.FindStringSubmatch(header)
	if m == nil {
		// 非法格式（如 "items=1-5"）回退整文件（Python 同）
		return ByteRange{Start: 0, End: total - 1}, true
	}
	start, end := int64(0), total-1
	if m[1] != "" {
		start = parseDigits(m[1], total) // 超长数字按越界处理
	}
	if m[2] != "" {
		end = parseDigits(m[2], total-1)
	}
	if end > total-1 {
		end = total - 1
	}
	if start > end || start >= total {
		return ByteRange{}, false
	}
	return ByteRange{Start: start, End: end}, true
}

// parseDigits 解析 Range 头里的纯数字字段。
//
// Python 的 int() 是任意精度，Go 的 int64 会溢出 —— 溢出/非法时返回
// fallback（end 字段给 total-1 等效于 Python 的 min(end, total-1) 截断，
// start 字段给 total 使其落入 start>=total 的 416 分支）。
func parseDigits(s string, fallback int64) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
