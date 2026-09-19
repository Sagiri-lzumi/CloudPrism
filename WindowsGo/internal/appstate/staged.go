package appstate

import (
	"os"
	"path/filepath"
	"strings"
)

// StagedUploadDirPrefix 是浏览器上传暂存目录的名字前缀。
//
// web 层用 os.MkdirTemp(tmpBase, StagedUploadDirPrefix) 建目录，本包据此
// 辨认「这个本地文件是暂存副本、任务用完必须删」。两侧共用同一常量而不是
// 各写一份字面量，避免前缀被改一处后清理逻辑静默失效（表现为暂存明文
// 永久留在 data/tmp 里）。
const StagedUploadDirPrefix = "cp-upload-"

// maxStageDepth 是向上回溯目录的深度上限，防御异常深路径造成的长循环。
const maxStageDepth = 64

// reapStagedUpload 回收浏览器上传的暂存明文：删掉文件本身，并逐级向上
// 清理因此变空的目录，直到并包含暂存根目录。
//
// 为什么必须由任务终态触发而不能在 HTTP handler 里删：入队是异步的，
// handler 返回时任务往往还没开始读本地文件。早期实现在 handler 里
// `defer os.RemoveAll(stageDir)`，导致大文件或队列繁忙时上传报
// 「系统找不到指定的文件」——小文件只是靠竞态侥幸通过。
//
// 非暂存路径（例如下载任务落盘的用户文件、本地上传的原始文件）一律不动：
// 判据是路径中是否含有 StagedUploadDirPrefix 目录段。
func reapStagedUpload(local string) {
	if local == "" || !isStagedUploadPath(local) {
		return
	}
	_ = os.Remove(local)

	dir := filepath.Dir(local)
	for depth := 0; depth < maxStageDepth; depth++ {
		base := filepath.Base(dir)
		if !isDirEmpty(dir) {
			// 同批还有别的文件在传（或暂存里还有残留）：留给它们收尾
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		if strings.HasPrefix(base, StagedUploadDirPrefix) {
			return // 暂存根已回收，收工
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// isStagedUploadPath 判断路径是否位于某个 cp-upload-* 暂存目录之下。
func isStagedUploadPath(local string) bool {
	dir := filepath.Dir(local)
	for depth := 0; depth < maxStageDepth; depth++ {
		if strings.HasPrefix(filepath.Base(dir), StagedUploadDirPrefix) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false // 已到根，未命中
		}
		dir = parent
	}
	return false
}

// isDirEmpty 判断目录是否为空（不存在也视为"空"，便于回收流程继续向上）。
func isDirEmpty(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return true
	}
	defer f.Close()
	// 只取一个条目即可判断是否为空；目录下条目多时不会全量读入
	names, err := f.Readdirnames(1)
	return err != nil || len(names) == 0
}
