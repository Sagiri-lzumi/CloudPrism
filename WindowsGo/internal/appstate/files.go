package appstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/pipeline"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/thumb"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/transfer"
)

// FileEntry 是目录列表条目（前端直接展示与操作）。
//
// Name 为后端原始名（文件名加密开启时为密文，文件带 .cpenc 后缀）；
// Display 为解密后的展示名（解密失败回退原名，与 Python 目录树同规则）；
// Remote 为可直接回传操作的完整后端相对路径（含子目录密库根前缀）。
type FileEntry struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	Remote  string `json:"remote"`
}

// ListDir 列出目录条目：过滤系统内部文件（密库 Marker 与同步索引），
// 解密展示名并按「目录在前、展示名升序」排序（对照 dir_tree_model.py
// 的过滤与排序规则）。remote 为空串表示密库根目录。
func (s *State) ListDir(ctx context.Context, remote string) ([]FileEntry, error) {
	conn, err := s.requireConn()
	if err != nil {
		return nil, err
	}
	opCtx, cancel := contextWithTimeout(ctx, opTimeout)
	defer cancel()

	// 子目录密库：UI 传入的 remote 以密库根为基准，拼上 vault 前缀
	full := conn.vaultPath
	if remote != "" {
		full = joinRemote(conn.vaultPath, trimSlash(remote))
	}

	entries, err := conn.backend.ListDir(opCtx, full)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		if e.Name == protocol.VaultMarkerName || e.Name == protocol.SyncIndexName {
			continue // 系统内部文件不上界面
		}
		fe := FileEntry{
			Name:   e.Name,
			IsDir:  e.IsDir,
			Size:   e.Size, // 密文大小（与 Python 端展示一致）
			Remote: joinRemote(trimSlash(remote), e.Name),
		}
		if e.IsDir {
			fe.Display = s.decryptName(conn, e.Name)
		} else if strings.HasSuffix(e.Name, protocol.FileExtension) {
			base := strings.TrimSuffix(e.Name, protocol.FileExtension)
			fe.Display = s.decryptName(conn, base)
		} else {
			fe.Display = e.Name // 非加密容器（异常残留）原样展示
		}
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir // 目录在前
		}
		return out[i].Display < out[j].Display
	})
	return out, nil
}

// decryptName 解密单段名称（文件名加密开启时）；失败回退原名。
// 对照 dir_tree_model.display_name 的容错语义。
func (s *State) decryptName(conn *connState, enc string) string {
	if !conn.meta.FilenameEnc {
		return enc
	}
	key, err := s.deriveKey(conn)
	if err != nil {
		return enc
	}
	plain, err := cryptox.DecryptFilename(enc, key[:])
	if err != nil || plain == "" {
		return enc // 密钥不符或未加密名：回退原名
	}
	return plain
}

// deriveKey 取文件名加解密密钥（命中 Session 密钥缓存）。
func (s *State) deriveKey(conn *connState) ([protocol.KeyLen]byte, error) {
	return conn.sess.DeriveKey(conn.meta.Salt)
}

// encryptName 加密单段名称（文件名加密开启时），并视 isFile 追加扩展名。
//
// isFile=false 即**目录段**，走确定性加密（cryptox.EncryptDirName）：
// 同一逻辑目录名恒得同一密文名，Mkdir 才真正幂等。用随机 nonce 加密目录名是
// v1.01 及更早的真实缺陷 —— 同一目录在一次上传里被加密多次（Mkdir 一次、
// 每个文件的父目录段各一次），于是云端炸出一堆「解密后同名」的目录，
// 用户看到的就是「上传一个文件夹却生成多个文件夹」。
//
// 文件段仍用随机 nonce（同名文件在云端不应暴露「内容相同」），
// 且文件本来只在一个地方加密一次，不需要确定性。
func (s *State) encryptName(conn *connState, plain string, isFile bool) (string, error) {
	name := plain
	if conn.meta.FilenameEnc {
		key, err := s.deriveKey(conn)
		if err != nil {
			return "", err
		}
		var enc string
		if isFile {
			enc, err = cryptox.EncryptFilename(plain, key[:])
		} else {
			enc, err = cryptox.EncryptDirName(plain, key[:])
		}
		if err != nil {
			return "", err
		}
		name = enc
	}
	if isFile {
		name += protocol.FileExtension
	}
	return name, nil
}

// NewFolder 在父目录下创建文件夹（显示名 → 加密后端名）。
func (s *State) NewFolder(ctx context.Context, parentRemote, displayName string) error {
	conn, err := s.requireConn()
	if err != nil {
		return err
	}
	displayName = trimSpace(displayName)
	if displayName == "" || strings.ContainsAny(displayName, "/\\") {
		return errors.New("文件夹名不能为空或包含路径分隔符")
	}
	enc, err := s.encryptName(conn, displayName, false)
	if err != nil {
		return err
	}
	remote := joinRemote(trimSlash(parentRemote), enc)
	opCtx, cancel := contextWithTimeout(ctx, opTimeout)
	defer cancel()
	if err := conn.backend.Mkdir(opCtx, joinRemote(conn.vaultPath, remote)); err != nil {
		return err
	}
	return nil
}

// RenameRemote 重命名文件/目录（remote 为完整远端路径；文件须带 .cpenc）。
func (s *State) RenameRemote(ctx context.Context, remote, newDisplay string) error {
	conn, err := s.requireConn()
	if err != nil {
		return err
	}
	newDisplay = trimSpace(newDisplay)
	if newDisplay == "" || strings.ContainsAny(newDisplay, "/\\") {
		return errors.New("名称不能为空或包含路径分隔符")
	}
	isFile := strings.HasSuffix(remote, protocol.FileExtension)
	enc, err := s.encryptName(conn, newDisplay, isFile)
	if err != nil {
		return err
	}
	dir := ""
	if idx := strings.LastIndex(remote, "/"); idx >= 0 {
		dir = remote[:idx]
	}
	dest := joinRemote(dir, enc)
	if dest == remote {
		return nil // 名字未变
	}
	opCtx, cancel := contextWithTimeout(ctx, opTimeout)
	defer cancel()
	oldFull := joinRemote(conn.vaultPath, remote)
	newFull := joinRemote(conn.vaultPath, dest)
	if err := conn.backend.Rename(opCtx, oldFull, newFull); err != nil {
		return err
	}
	// 旧名已不存在、新名可能是覆盖目标：两边的派生缓存都要丢
	invalidateCaches(conn, oldFull, newFull)
	return nil
}

// DeleteRemotes 把一批远端条目转成**删除任务**入队，返回入队任务数。
//
// 为什么是入队而不是同步删（用户 2026-09-27 的原话：「我删东西，不知道删除
// 好了没有」）：同步接口在整棵目录删完之前不给任何信号，大目录要等几十秒，
// 期间界面既无进度也无凭据。入队后删除与上传/下载共享同一套状态机与任务
// 列表，等待/进行/完成/失败/取消五种状态全部可见，还能取消与重试。
//
// 每**个选中项**一个任务（不是每个文件一个）：用户心智里的「一笔删除」就是
// 他勾的那几项；任务内部自行递归，条目数作为进度上报（见 runDeleteTask）。
// 所以 M 个文件的目录是 1 个任务，而不是 M 个任务刷屏。
func (s *State) DeleteRemotes(ctx context.Context, remotes []string) (int, error) {
	conn, err := s.requireConn()
	if err != nil {
		return 0, err
	}

	// 归一 + 排序 + 去重 + 丢掉被子项包含的项。
	//
	// 「丢掉被包含项」不是洁癖：多选里若同时选中 a 与 a/b，删完 a 之后 a/b
	// 已经不存在，第二个任务只会白跑一趟并报「找不到」—— 用户看到的是
	// 一条莫名其妙的失败任务。排序后祖先必然紧邻其后代之前，一趟前缀比较
	// 即可同时完成去重（child == parent）与包含判定（child 以 parent+"/" 开头）。
	cleaned := make([]string, 0, len(remotes))
	for _, r := range remotes {
		if r = trimSlash(r); r != "" {
			cleaned = append(cleaned, r)
		}
	}
	sort.Strings(cleaned)
	roots := cleaned[:0]
	for _, r := range cleaned {
		if n := len(roots); n > 0 {
			if last := roots[n-1]; r == last || strings.HasPrefix(r, last+"/") {
				continue
			}
		}
		roots = append(roots, r)
	}
	if len(roots) == 0 {
		return 0, nil
	}

	tasks := make([]*transfer.Task, 0, len(roots))
	for _, r := range roots {
		// RemotePath 存**含密库前缀的完整路径**（与 UploadPaths/DownloadFiles
		// 的约定一致），执行器直接照用，不再二次拼前缀。
		t := transfer.NewTask("", joinRemote(conn.vaultPath, r), transfer.DirDelete)
		t.DisplayName = s.displayNameOf(conn, r)
		t.RemoteDir = parentRemote(r) // 前端按此判定「删的是不是当前浏览目录」
		tasks = append(tasks, t)
	}
	if err := s.enqueueTasks(conn, tasks); err != nil {
		return 0, err
	}
	return len(tasks), nil
}

// deleteRemoteFull 递归删除**完整远端路径**（含 vaultPath 前缀）。
//
// onItem 每成功删掉一个节点（文件或目录各算一个）回调一次，可为 nil；
// 删除任务据此上报条目进度。**先列后删**是为了规避三后端目录删除语义差异
// （有的后端删非空目录直接报错）。
func deleteRemoteFull(ctx context.Context, conn *connState, full string, onItem func()) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	entries, err := conn.backend.ListDir(ctx, full)
	if err != nil {
		// 列不出（非目录/不存在/后端故障）→ 按文件直接删，让后端裁决
		if derr := conn.backend.Delete(ctx, full); derr != nil {
			return derr
		}
		invalidateCaches(conn, full)
		onItemDone(onItem)
		return nil
	}
	// 目录：先删内部条目再删自身。逐节点检查取消 —— 大目录递归可能跑很久，
	// 取消（CancelAll / 锁库 Clear）必须在条目边界就能生效，只在入口查一次
	// 会让「取消」对一棵大树形同虚设。
	for _, e := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err := deleteRemoteFull(ctx, conn, joinRemote(full, e.Name), onItem); err != nil {
			return err
		}
	}
	if err := conn.backend.Delete(ctx, full); err != nil {
		return err
	}
	invalidateCaches(conn, full)
	onItemDone(onItem)
	return nil
}

// onItemDone 回调的空安全包装。
func onItemDone(onItem func()) {
	if onItem != nil {
		onItem()
	}
}

// displayNameOf 取远端条目的解密展示名（只解末段；文件先去掉 .cpenc 后缀）。
//
// 判据与 ListDir 同源（后缀即文件）——不另做一次后端探测：目录名恰好以
// .cpenc 结尾属于病态情形，两处保持同一规则即可，不会自相矛盾。
func (s *State) displayNameOf(conn *connState, remote string) string {
	base := remote
	if i := strings.LastIndex(remote, "/"); i >= 0 {
		base = remote[i+1:]
	}
	if strings.HasSuffix(base, protocol.FileExtension) {
		return s.decryptName(conn, strings.TrimSuffix(base, protocol.FileExtension))
	}
	return s.decryptName(conn, base)
}

// parentRemote 取远端路径的父目录（根级条目返回空串）。
func parentRemote(remote string) string {
	if i := strings.LastIndex(remote, "/"); i >= 0 {
		return remote[:i]
	}
	return ""
}

// ExportRemote 解密导出到本地目录（替代 Python drag-out；文件保留展示名，
// 已存在时自动追加序号）。返回落盘路径。目录入口由绑定层对话框选择。
func (s *State) ExportRemote(ctx context.Context, remote, localDir string) (string, error) {
	conn, err := s.requireConn()
	if err != nil {
		return "", err
	}
	name := filepath.Base(remote)
	name = strings.TrimSuffix(name, protocol.FileExtension)
	display := s.decryptName(conn, name)
	dest := uniqueLocalPath(filepath.Join(localDir, display))
	opts := s.cfg.Queue.Options()
	dec := &pipeline.Decryptor{Sess: conn.sess, Backend: conn.backend, Workers: opts.MaxWorkers}
	err = dec.DownloadAndDecrypt(ctx, joinRemote(conn.vaultPath, trimSlash(remote)), dest, nil)
	return dest, err
}

// ---------------------------------------------------------------------------
// 预览端点（图片缩略图 / 媒体播放）
// ---------------------------------------------------------------------------

// 下列三个方法一律返回**相对路径**（形如 /s/<token>/<name>），由浏览器按
// 当前页面 origin 自行解析。
//
// 历史坑：早期这里拼的是代理自己的绝对地址 http://127.0.0.1:<动态端口>。
// 本机访问看不出问题，但局域网档下远端浏览器会去连**它自己**的 127.0.0.1，
// 导致预览/缩略图/下载全部失效。改为相对路径后，本机与远端走同一条
// 同源路由（web 的 /s/ /t/ /d/，直接复用 appstate 的代理处理器），
// 代理也就不需要独立监听端口——pkg/streaming 顶部「代理只监听 127.0.0.1、
// 不把令牌与解密能力暴露给局域网」的约束由此天然成立。

// ThumbURL 取远端图片的缩略图端点 URL（两级缓存 + 服务端缩放 JPEG）。
// 幂等：同一远程路径重复调用返回既有令牌（流式注册表按路径判重）。
// 生成失败返回错误，前端回退占位图标（对照 fetch_thumbnail 吞异常语义）。
func (s *State) ThumbURL(ctx context.Context, remote string) (string, error) {
	conn, err := s.requireConn()
	if err != nil {
		return "", err
	}
	if conn.proxy == nil {
		return "", errors.New("流式服务不可用（代理未启动）")
	}
	// 缓存键与注册键都用完整密文路径（含密库前缀），避免不同密库同名冲突
	full := joinRemote(conn.vaultPath, trimSlash(remote))
	payload, err := thumb.Fetch(ctx, conn.sess, conn.backend, full, conn.cache)
	if err != nil {
		return "", err
	}
	entry, err := conn.proxy.RegisterThumb(full, filepath.Base(remote), payload, "image/jpeg")
	if err != nil {
		return "", err
	}
	return entry.URLPath(), nil
}

// MediaURL 注册媒体流端点 URL（播放器 <video>/<audio> 直连）。
// 幂等语义与错误分类（404/非密文/后端故障）见 streaming.RegisterStream。
// displayName 为解密展示名 —— MIME 推断与 URL 可读性都依赖它。
func (s *State) MediaURL(ctx context.Context, remote, displayName string) (string, error) {
	conn, err := s.requireConn()
	if err != nil {
		return "", err
	}
	if conn.proxy == nil {
		return "", errors.New("流式服务不可用（代理未启动）")
	}
	full := joinRemote(conn.vaultPath, trimSlash(remote))
	entry, err := conn.proxy.RegisterStream(ctx, full, displayName)
	if err != nil {
		return "", err
	}
	return entry.URLPath(), nil
}

// DownloadURL 注册下载端点 URL（浏览器 <a download> 直连，服务端全文件
// 流式解密 + Content-Disposition: attachment）。幂等语义与错误分类同
// MediaURL；displayName 用于响应头的下载文件名。
func (s *State) DownloadURL(ctx context.Context, remote, displayName string) (string, error) {
	conn, err := s.requireConn()
	if err != nil {
		return "", err
	}
	if conn.proxy == nil {
		return "", errors.New("流式服务不可用（代理未启动）")
	}
	full := joinRemote(conn.vaultPath, trimSlash(remote))
	entry, err := conn.proxy.RegisterDownload(ctx, full, displayName)
	if err != nil {
		return "", err
	}
	return entry.URLPath(), nil
}

// RevokeMedia 撤销媒体/缩略图令牌（token 为端点 URL 的最后一段）。
// 播放器关闭时调用，释放注册表容量与派生密钥。
func (s *State) RevokeMedia(token string) {
	conn, err := s.requireConn()
	if err != nil {
		return
	}
	conn.proxy.Revoke(token)
}

// uniqueLocalPath 本地路径冲突自动序号化（a.txt → a (1).txt）。
func uniqueLocalPath(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		cand := base + " (" + strconv.Itoa(i) + ")" + ext
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}
