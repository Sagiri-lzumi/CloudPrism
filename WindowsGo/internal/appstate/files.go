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
func (s *State) encryptName(conn *connState, plain string, isFile bool) (string, error) {
	name := plain
	if conn.meta.FilenameEnc {
		key, err := s.deriveKey(conn)
		if err != nil {
			return "", err
		}
		enc, err := cryptox.EncryptFilename(plain, key[:])
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
	return conn.backend.Rename(opCtx,
		joinRemote(conn.vaultPath, remote), joinRemote(conn.vaultPath, dest))
}

// DeleteRemote 删除远端文件或目录（目录递归删除：先列后删，
// 规避三后端目录删除语义差异）。对照 app.py _delete_remote。
func (s *State) DeleteRemote(ctx context.Context, remote string) error {
	conn, err := s.requireConn()
	if err != nil {
		return err
	}
	return deleteRemoteRecursive(ctx, conn, trimSlash(remote))
}

// deleteRemoteRecursive 递归删除：目录深度优先删文件后删目录。
func deleteRemoteRecursive(ctx context.Context, conn *connState, remote string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	full := joinRemote(conn.vaultPath, remote)
	entries, err := conn.backend.ListDir(ctx, full)
	if err != nil {
		// 列不出（非目录/不存在/后端故障）→ 按文件直接删，让后端裁决
		return conn.backend.Delete(ctx, full)
	}
	// 目录：先删内部条目再删自身
	for _, e := range entries {
		child := joinRemote(remote, e.Name)
		if err := deleteRemoteRecursive(ctx, conn, child); err != nil {
			return err
		}
	}
	return conn.backend.Delete(ctx, full)
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
	return conn.proxy.BaseURL() + entry.URLPath(), nil
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
	return conn.proxy.BaseURL() + entry.URLPath(), nil
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
