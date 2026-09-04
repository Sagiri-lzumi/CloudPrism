package vault

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// 增量同步引擎（首期：本地 → 云端单向增量）。
//
// 核心思路与 Python 端一致（对照 core/sync_engine.py）：
//   - 索引文件 .cloudprism_index 存放于密库根（或子目录密库位置），
//     内容为 JSON {相对路径: {size, mtime, uploaded_at}}，整体加密为
//     .cpenc 容器上传——文件名加密开或关均可按固定文件名定位索引；
//   - Plan() 对比本地文件 size+mtime 与索引：新文件/已变更/未变三态；
//   - BuildTasks() 把需同步文件转为统一传输任务（进度由调用方聚合）；
//   - UpdateIndex() 在传输完成后整体重写索引（先删后传，规避后端分块
//     上传的续传语义）。
//
// 首期边界（与 Python 端完全一致）：仅单向增量；本地删除不传播到云端；
// 云端冲突检测留给双向同步期；索引解析失败视为空索引（全量重扫）。

// MTIME_TOLERANCE 是 mtime 比较容差（秒）：文件系统时间戳精度差异
// 不视为变更。对照 sync_engine.py:35。
const MTIME_TOLERANCE = 1.0

// IndexEntry 是索引中单个文件的记录（JSON 结构固定，见包注释）。
type IndexEntry struct {
	Size       int64   `json:"size"`
	Mtime      float64 `json:"mtime"`
	UploadedAt float64 `json:"uploaded_at"`
}

// SyncItem 是本地文件的三态计划条目。
type SyncItem struct {
	RelPath   string  // 相对本地同步目录（/ 分隔）
	LocalPath string  // 本地绝对路径
	Size      int64   // stat 快照（BuildTasks 携带给传输队列）
	Mtime     float64 // 秒级浮点（含小数，与 Python st_mtime 同精度）
}

// SyncPlan 是一次同步计划：本地目录与索引对比的三态结果。
type SyncPlan struct {
	LocalDir  string
	New       []SyncItem // 索引中不存在
	Changed   []SyncItem // size/mtime 变化
	Unchanged []SyncItem // 与索引一致
}

// PendingCount 返回需要同步上传的文件数。对照 SyncPlan.pending_count。
func (p *SyncPlan) PendingCount() int {
	return len(p.New) + len(p.Changed)
}

// SyncTask 是同步产生的上传任务描述。
//
// 字段对齐 Python 端 TransferTask 的上传语义（transfer_queue.py:41-68）：
// expected_size/expected_mtime 为本地源文件快照，供脏续传校验用。
// display_name 为相对路径（子目录层级可见）。
type SyncTask struct {
	LocalPath     string
	RemotePath    string
	DisplayName   string
	ExpectedSize  int64
	ExpectedMtime float64
}

// SyncEngine 是本地目录 → 云端密库的单向增量同步引擎。
type SyncEngine struct {
	sess        *session.Session
	backend     storage.Backend
	vaultPath   string // 密库位置（空=后端根目录；否则为子目录名，无首尾斜杠）
	filenameEnc bool   // 密库是否开启文件名加密
	nameSalt    []byte // 文件名加密密钥盐（= 密库元信息 salt）
}

// NewSyncEngine 构造同步引擎。filename_enc 为 True 时 nameSalt 必传
// （对应密库元信息的 salt），否则文件名加密产物不可复原。
func NewSyncEngine(sess *session.Session, backend storage.Backend, vaultPath string, filenameEnc bool, nameSalt []byte) *SyncEngine {
	return &SyncEngine{
		sess:        sess,
		backend:     backend,
		vaultPath:   strings.Trim(vaultPath, "/"),
		filenameEnc: filenameEnc,
		nameSalt:    nameSalt,
	}
}

// ---------------------------------------------------------------------------
// 路径处理
// ---------------------------------------------------------------------------

// remoteJoin 把密库内相对路径拼上子目录密库前缀。
func (e *SyncEngine) remoteJoin(relRemote string) string {
	if e.vaultPath != "" {
		return e.vaultPath + "/" + relRemote
	}
	return relRemote
}

// encName 文件名加密开启时加密单个路径段。
func (e *SyncEngine) encName(name string) (string, error) {
	if !e.filenameEnc {
		return name, nil
	}
	key, err := e.sess.DeriveKey(e.nameSalt)
	if err != nil {
		return "", err
	}
	return cryptox.EncryptFilename(name, key[:])
}

// RemotePath 本地相对路径 → 后端加密容器路径。
//
// 逐段处理文件名加密（目录段同样加密，路径结构对外不可见），
// 末段追加 .cpenc 扩展名。对照 sync_engine.py:96-104。
func (e *SyncEngine) RemotePath(relPath string) (string, error) {
	segs := strings.Split(strings.ReplaceAll(relPath, "\\", "/"), "/")
	enc := make([]string, 0, len(segs))
	for _, s := range segs {
		if s == "" {
			continue
		}
		name, err := e.encName(s)
		if err != nil {
			return "", err
		}
		enc = append(enc, name)
	}
	if len(enc) == 0 {
		return "", errors.New("vault: 空相对路径无法生成远程路径")
	}
	enc[len(enc)-1] += protocol.FileExtension
	return e.remoteJoin(strings.Join(enc, "/")), nil
}

// indexRemotePath 索引文件的后端路径（固定名，不参与文件名加密）。
func (e *SyncEngine) indexRemotePath() string {
	return e.remoteJoin(protocol.SyncIndexName)
}

// ---------------------------------------------------------------------------
// .cpenc 容器的整文件字节加解密（小文件专用）
// ---------------------------------------------------------------------------
//
// 同步索引通常只有几十 KB，不值得走 pkg/pipeline 的分段并行管线；
// 这里直接整块内存处理。**边界声明**：只允许小文件走这两个函数，
// 大文件的加密/解密一律交给传输管线（pipeline 按 1MiB 对齐分段，
// 见 pkg/pipeline 的 ShardAlign 注释）。

// encryptWhole 把明文整体加密为 .cpenc 容器字节（随机 salt/iv）。
//
// 产物结构与 Python 端 Encryptor.encrypt_to_bytes（encryptor.py:249-281）
// 逐字节一致：header 在前、AES-CTR 密文在后，明文长度 == 密文长度。
func (e *SyncEngine) encryptWhole(payload []byte) ([]byte, error) {
	salt := make([]byte, protocol.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("vault: 随机数源失败: %w", err)
	}
	iv := make([]byte, protocol.IVLen)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("vault: 随机数源失败: %w", err)
	}
	key, err := e.sess.DeriveKey(salt)
	if err != nil {
		return nil, err
	}
	header, err := cryptox.Build(salt, iv, 0x00, protocol.Version)
	if err != nil {
		return nil, err
	}
	ctr, err := cryptox.NewCTR(key[:], iv)
	if err != nil {
		return nil, err
	}
	ciphertext := make([]byte, len(payload))
	ctr.XORAt(ciphertext, payload, 0) // 从头开始，blockIdx 0
	return append(header, ciphertext...), nil
}

// decryptWholeFile 整读远程 .cpenc 容器并解密为明文。
// 错误（含格式损坏）原样返回，由调用方按「索引损坏视为空」折叠。
func (e *SyncEngine) decryptWholeFile(ctx context.Context, remotePath string) ([]byte, error) {
	size, err := e.backend.GetSize(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	if size <= 0 {
		return nil, errors.New("vault: 空密文文件")
	}
	ct, err := e.backend.DownloadRange(ctx, remotePath, 0, size-1)
	if err != nil {
		return nil, err
	}
	header, err := cryptox.ParseBytes(ct)
	if err != nil {
		return nil, err
	}
	offset := int64(header.HeaderLength)
	if size <= offset {
		return nil, errors.New("vault: 密文长度小于文件头")
	}
	key, err := e.sess.DeriveKey(header.Salt)
	if err != nil {
		return nil, err
	}
	ctr, err := cryptox.NewCTR(key[:], header.IV)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, size-offset)
	ctr.XORAt(plain, ct[offset:], 0)
	return plain, nil
}

// ---------------------------------------------------------------------------
// 索引读写
// ---------------------------------------------------------------------------

// LoadIndex 读取并解密索引；不存在或损坏时返回空索引（全量重扫），
// 不阻断同步。对照 sync_engine.py:114-125 的 try/except 全吞语义。
func (e *SyncEngine) LoadIndex(ctx context.Context) map[string]IndexEntry {
	plain, err := e.decryptWholeFile(ctx, e.indexRemotePath())
	if err != nil {
		return map[string]IndexEntry{}
	}
	var idx map[string]IndexEntry
	if err := json.Unmarshal(plain, &idx); err != nil {
		return map[string]IndexEntry{}
	}
	if idx == nil {
		return map[string]IndexEntry{}
	}
	return idx
}

// FileState 是 UpdateIndex 入参的单文件最新状态（来自传输成功回调）。
type FileState struct {
	Size  int64
	Mtime float64
}

// UpdateIndex 合并上传成功的条目并整体重写索引（加密后先删后传）。
// 对照 sync_engine.py:127-176。
func (e *SyncEngine) UpdateIndex(ctx context.Context, entries map[string]FileState) error {
	index := e.LoadIndex(ctx) // 失败为空索引：本批成为唯一内容（自愈语义）
	now := float64(time.Now().UnixNano()) / 1e9
	for rel, st := range entries {
		index[rel] = IndexEntry{Size: st.Size, Mtime: st.Mtime, UploadedAt: now}
	}

	// ensure_ascii=False 等价输出 + 不转义 HTML（路径可能含 < > &）
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(index); err != nil {
		return err
	}
	payload := []byte(buf.String())

	encrypted, err := e.encryptWhole(payload)
	if err != nil {
		return err
	}

	dir := ""
	if d, ok := paths.TempDir(true); ok {
		dir = d
	} else {
		dir = os.TempDir()
	}
	tmp, err := os.CreateTemp(dir, "cloudprism-*.cpenc")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(encrypted); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	remote := e.indexRemotePath()
	exists, err := e.backend.Exists(ctx, remote)
	if err != nil {
		return err
	}
	if exists {
		// 先删后传：续传语义会把新内容追加到旧内容后而非覆盖
		if err := e.backend.Delete(ctx, remote); err != nil {
			return err
		}
	}
	return e.backend.UploadChunked(ctx, tmp.Name(), remote, 0, nil)
}

// ---------------------------------------------------------------------------
// 计划与任务
// ---------------------------------------------------------------------------

// Plan 扫描本地目录，对比索引生成三态同步计划。
//
// 判定规则（对照 sync_engine.py:182-220）：
//   - 索引无该相对路径 → new
//   - size 相同且 |mtime 差| ≤ 容差 → unchanged
//   - 其余 → changed
//
// 扫描期间单个文件 stat 失败（被删除/权限）→ 跳过该文件，不整体失败。
func (e *SyncEngine) Plan(ctx context.Context, localDir string) (*SyncPlan, error) {
	absDir, err := filepath.Abs(localDir)
	if err != nil {
		return nil, err
	}
	index := e.LoadIndex(ctx)
	plan := &SyncPlan{LocalDir: absDir}

	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if walkErr != nil {
			return nil // 单目录遍历失败跳过（对齐 Python 容错精神）
		}
		if d.IsDir() {
			return nil
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil // 扫描期间被删除等
		}
		rel, err := filepath.Rel(absDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		mtime := float64(st.ModTime().UnixNano()) / 1e9
		item := SyncItem{RelPath: rel, LocalPath: path, Size: st.Size(), Mtime: mtime}

		old, ok := index[rel]
		switch {
		case !ok:
			plan.New = append(plan.New, item)
		case old.Size == st.Size() && absDiff(old.Mtime, mtime) <= MTIME_TOLERANCE:
			plan.Unchanged = append(plan.Unchanged, item)
		default:
			plan.Changed = append(plan.Changed, item)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// BuildTasks 把计划中的新增/变更文件转为上传任务（进度由调用方聚合）。
// 对照 sync_engine.py:222-239。
func (e *SyncEngine) BuildTasks(plan *SyncPlan) ([]SyncTask, error) {
	var tasks []SyncTask
	for _, item := range append(append([]SyncItem{}, plan.New...), plan.Changed...) {
		remote, err := e.RemotePath(item.RelPath)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, SyncTask{
			LocalPath:     item.LocalPath,
			RemotePath:    remote,
			DisplayName:   item.RelPath,
			ExpectedSize:  item.Size,
			ExpectedMtime: item.Mtime,
		})
	}
	return tasks, nil
}

// absDiff 浮点绝对值（math.Abs 导入过重，手写对等快路径）。
func absDiff(a, b float64) float64 {
	if a >= b {
		return a - b
	}
	return b - a
}
