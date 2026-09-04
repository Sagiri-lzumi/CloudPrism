// Package vault 提供密库生命周期管理与增量同步引擎（非 GUI 内核）。
//
//   - manager：新建 / 连接 / 重命名密库 + 恢复码（v3），
//     对照 WindowsPy/src/cloudprism/core/vault_manager.py；
//   - sync：本地目录 → 云端单向增量同步，
//     对照 WindowsPy/src/cloudprism/core/sync_engine.py。
//
// 密码学一律经 pkg/cryptox / pkg/session 触碰，本包禁止自行拼装格式字节。
// 所有上传都落在 storage.Backend 接口上——三后端在此无差别可用。
package vault

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// 密库操作错误哨兵。文案与 Python 端 VaultError 消息逐字对齐
// （对照 vault_manager.py:123/224/249/269），供绑定层直接展示。
var (
	// ErrVaultExists 目标位置已存在密库（创建前检查，防止覆盖）。
	ErrVaultExists = errors.New("该位置已存在密库，请选择「连接」或更换位置")
	// ErrNoVault 目标位置不存在密库（Python 端 open 返回 None 后由
	// has_vault 二次区分，Go 端直接给准确哨兵）。
	ErrNoVault = errors.New("该位置不存在密库，请检查密库位置")
	// ErrBadPassword 主密码校验失败（GCM 标签不匹配；Marker 被篡改/损坏
	// 也会走到这里——Python 端同语义，无法也不该区分两者）。
	ErrBadPassword = errors.New("主密码错误，请重试")
	// ErrBadRecovery 恢复码无效、或位置没有带恢复码的密库。
	ErrBadRecovery = errors.New("恢复码无效，或该位置不存在带恢复码的密库")
)

// ProgressFn 是长耗时阶段的进度回调（可为 nil）；回调失败不阻断主流程
// （对照 vault_manager._report 的吞异常语义）。
type ProgressFn func(msg string)

// Manager 是密库生命周期管理器：一个实例绑定一个存储后端。
type Manager struct {
	backend storage.Backend
}

// NewManager 构造绑定指定后端的密库管理器。
func NewManager(backend storage.Backend) *Manager {
	return &Manager{backend: backend}
}

// report 触发阶段进度回调；panic 由调用方 guard（回调仅展示用）。
func report(cb ProgressFn, msg string) {
	if cb == nil {
		return
	}
	defer func() { _ = recover() }()
	cb(msg)
}

// markerPath 计算 Marker 完整路径：根密库为根目录下的固定名文件，
// 附加密库在对应子目录下。对照 vault_manager.py:54-61。
func markerPath(vaultPath string) string {
	vaultPath = strings.Trim(vaultPath, "/")
	if vaultPath == "" {
		return protocol.VaultMarkerName
	}
	return vaultPath + "/" + protocol.VaultMarkerName
}

// ---------------------------------------------------------------------------
// 查找与判断
// ---------------------------------------------------------------------------

// HasVault 判断指定位置（根目录或子目录）是否存在 Vault Marker。
func (m *Manager) HasVault(ctx context.Context, vaultPath string) (bool, error) {
	return m.backend.Exists(ctx, markerPath(vaultPath))
}

// VaultInfo 是 ListVaults 返回的一条密库位置信息。
type VaultInfo struct {
	Path string // "" = 后端根目录；否则为一级子目录名（无首尾斜杠）
}

// ListVaults 扫描后端上的全部密库（根目录 + 一级子目录）。
//
// 零协议变更：不新增任何服务端索引，仅探测固定名 Marker 文件——
// 根目录有 Marker 即主密库（Path=""），一级子目录含 Marker 的为附加
// 密库（Path=子目录名）。密库名称需打开后才能解密获得，列表先只给位置。
// 扫描失败返回已探测到的部分（对照 vault_manager.py:76-95 的容错）。
func (m *Manager) ListVaults(ctx context.Context) ([]VaultInfo, error) {
	var out []VaultInfo
	ok, err := m.backend.Exists(ctx, protocol.VaultMarkerName)
	if err != nil {
		return nil, err // 后端故障不能折叠成「没有密库」（会误导初始化向导）
	}
	if ok {
		out = append(out, VaultInfo{Path: ""})
	}

	entries, err := m.backend.ListDir(ctx, "/")
	if err != nil {
		// 根目录都列不出：返回已发现的主密库，子目录扫描留到下次
		return out, nil
	}
	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		sub, err := m.backend.Exists(ctx, markerPath(e.Name))
		if err != nil {
			continue // 单个子目录探测失败不拖累整体
		}
		if sub {
			out = append(out, VaultInfo{Path: e.Name})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Marker 下载与上传（新建 / 重命名 / 恢复码共用）
// ---------------------------------------------------------------------------

// downloadMarker 整读 Vault Marker 内容。Marker 很小（约百余字节），
// 一次 GetSize + 一次全范围下载即可；不存在返回 ErrNoVault。
func (m *Manager) downloadMarker(ctx context.Context, vaultPath string) ([]byte, error) {
	p := markerPath(vaultPath)
	size, err := m.backend.GetSize(ctx, p)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrIsDir) {
			return nil, ErrNoVault
		}
		return nil, err
	}
	if size <= 0 {
		return nil, ErrNoVault // 空文件不是合法 Marker
	}
	return m.backend.DownloadRange(ctx, p, 0, size-1)
}

// uploadMarker 经临时文件上传 Vault Marker（复用后端分块上传接口）。
//
// Marker 为整体重写的小文件：上传前先删除旧文件，规避后端分块上传的
// 断点续传语义（续传会把新内容追加到旧内容之后而非覆盖——Python 端同款
// 处理，见 vault_manager.py:340-367）。临时文件落产品自管 data/tmp/。
func (m *Manager) uploadMarker(ctx context.Context, data []byte, vaultPath string) error {
	remote := markerPath(vaultPath)

	exists, err := m.backend.Exists(ctx, remote)
	if err != nil {
		return err
	}
	if exists {
		if err := m.backend.Delete(ctx, remote); err != nil {
			return err
		}
	}

	dir := ""
	if d, ok := paths.TempDir(true); ok {
		dir = d
	} else {
		dir = os.TempDir() // data/ 不可写时退回系统临时目录
	}
	tmp, err := os.CreateTemp(dir, "cloudprism-*.vault")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // Marker 含 salt/密文，用完即删
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return m.backend.UploadChunked(ctx, tmp.Name(), remote, 0, nil)
}

// ---------------------------------------------------------------------------
// 新建
// ---------------------------------------------------------------------------

// ensureVaultDir 为子目录密库确保目录存在；已存在时容错（幂等）。
//
// 无返回值是有意设计：Python 端此处吞所有异常（vault_manager.py:125-131），
// 后续 upload 失败才是真故障——建目录失败多半是并发建库，目录最终会存在，
// 且本地/WebDAV/百度三后端的 Mkdir 对已存在目录均幂等。
func (m *Manager) ensureVaultDir(ctx context.Context, vaultPath string) {
	vaultPath = strings.Trim(vaultPath, "/")
	if vaultPath == "" {
		return // 根密库不需要建目录
	}
	_ = m.backend.Mkdir(ctx, vaultPath) // 吞错：上传步骤兜底
}

// Create 新建密库：生成元数据并上传 Vault Marker（不生成恢复码）。
// 对照 vault_manager.py:101-135。name 为可选的用户自定义密库名称。
func (m *Manager) Create(ctx context.Context, masterPassword string, filenameEnc bool, name, vaultPath string) (*cryptox.VaultMetadata, error) {
	exists, err := m.HasVault(ctx, vaultPath)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrVaultExists
	}
	m.ensureVaultDir(ctx, vaultPath)

	meta, err := cryptox.GenerateMetadata(filenameEnc, name)
	if err != nil {
		return nil, err
	}
	data, err := cryptox.CreateMarker(meta, masterPassword, nil)
	if err != nil {
		return nil, err
	}
	if err := m.uploadMarker(ctx, data, vaultPath); err != nil {
		return nil, err
	}
	return &meta, nil
}

// CreateWithRecovery 新建密库并同步生成恢复码（一次写入，推荐的新建入口）。
//
// 相比 Create + GenerateRecoveryCode 两步流程，省去开库复核与二次重写
// Marker：PBKDF2 派生从 4 次降到 2 次（每次约数百毫秒到数秒，迭代次数
// 与 Android 端协议绑定不可调），显著缩短建库耗时。
// 对照 vault_manager.py:137-182。
//
// 返回 (元信息, 恢复码)；恢复码仅返回给调用方展示，不落盘/上传；
// 元信息 HasRecovery 恒为 true。
func (m *Manager) CreateWithRecovery(ctx context.Context, masterPassword string, filenameEnc bool, name, vaultPath string, onProgress ProgressFn) (*cryptox.VaultMetadata, string, error) {
	report(onProgress, "正在检查存储位置…")
	exists, err := m.HasVault(ctx, vaultPath)
	if err != nil {
		return nil, "", err
	}
	if exists {
		return nil, "", ErrVaultExists
	}
	m.ensureVaultDir(ctx, vaultPath)

	report(onProgress, "正在生成密库元数据…")
	meta, err := cryptox.GenerateMetadata(filenameEnc, name)
	if err != nil {
		return nil, "", err
	}
	secret, err := randomSecret()
	if err != nil {
		return nil, "", err
	}
	code := EncodeRecoveryCode(secret)

	report(onProgress, "生成恢复码保护块（密钥派生，约需数秒）…")
	blob, err := cryptox.BuildRecoveryBlob(secret, masterPassword)
	if err != nil {
		return nil, "", err
	}
	report(onProgress, "主密钥派生与加密（约需数秒）…")
	data, err := cryptox.CreateMarker(meta, masterPassword, blob)
	if err != nil {
		return nil, "", err
	}
	report(onProgress, "正在上传密库文件…")
	if err := m.uploadMarker(ctx, data, vaultPath); err != nil {
		return nil, "", err
	}
	meta.HasRecovery = true
	return &meta, code, nil
}

// ---------------------------------------------------------------------------
// 连接
// ---------------------------------------------------------------------------

// Open 连接密库：下载并校验 Vault Marker。
//
// 返回校验通过的元信息；位置无密库返回 ErrNoVault、密码错误或文件损坏
// 返回 ErrBadPassword（GCM 标签无法区分两者，与 Python 端一致——
// 见 vault_manager.py:188-202 的 None 语义）。
func (m *Manager) Open(ctx context.Context, masterPassword, vaultPath string, onProgress ProgressFn) (*cryptox.VaultMetadata, error) {
	report(onProgress, "正在载入密库文件…")
	data, err := m.downloadMarker(ctx, vaultPath)
	if err != nil {
		return nil, err
	}
	report(onProgress, "校验主密码（密钥派生，约需数秒）…")
	meta, err := cryptox.VerifyMarker(data, masterPassword)
	if err != nil {
		return nil, ErrBadPassword
	}
	return meta, nil
}

// ---------------------------------------------------------------------------
// 重命名（名称随文件保存，跨设备跟随密库）
// ---------------------------------------------------------------------------

// Rename 修改密库名称：校验主密码后用新名称重新加密 Marker 并覆盖上传。
//
// 仅替换名称字段，vault_id/salt/iv/加密配置均不变，已加密文件不受影响；
// 既有恢复码块（v3 尾部）原样保留，重命名不使恢复码失效。
// 对照 vault_manager.py:208-234。
func (m *Manager) Rename(ctx context.Context, masterPassword, newName, vaultPath string) (*cryptox.VaultMetadata, error) {
	meta, err := m.Open(ctx, masterPassword, vaultPath, nil)
	if err != nil {
		// Python 端此处抛 VaultError("密码错误或后端无密库…")，
		// 底层哨兵已含准确分类，直接透传
		return nil, err
	}

	// 保留既有恢复码块：旧文件尾部 recovery_len(2B)+blob 原样带回
	oldData, err := m.downloadMarker(ctx, vaultPath)
	if err != nil {
		return nil, err
	}
	_, tail := cryptox.SplitRecoveryTail(oldData)
	var blob []byte
	if len(tail) >= 2 {
		blob = tail[2:]
	}

	newMeta := *meta // 浅拷贝：改名不换 id/salt/iv
	newMeta.Name = strings.TrimSpace(newName)
	data, err := cryptox.CreateMarker(newMeta, masterPassword, blob)
	if err != nil {
		return nil, err
	}
	if err := m.uploadMarker(ctx, data, vaultPath); err != nil {
		return nil, err
	}
	return &newMeta, nil
}

// ---------------------------------------------------------------------------
// 恢复码（v3）：编解码
// ---------------------------------------------------------------------------

// randomSecret 生成恢复码随机密钥（10 字节，与 Android/Windows 协议一致）。
func randomSecret() ([]byte, error) {
	secret := make([]byte, protocol.RecoverySecretLen)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("vault: 随机数源失败: %w", err)
	}
	return secret, nil
}

// EncodeRecoveryCode 随机密钥 → 恢复码（Base32 无填充大写字母串）。
func EncodeRecoveryCode(secret []byte) string {
	return cryptox.B32EncodeNoPad(secret)
}

// DecodeRecoveryCode 恢复码 → 随机密钥。
//
// 格式不符（去分隔符/空格、大写化、补填充后仍非法）返回错误，
// 调用方折叠为 ErrBadRecovery。对照 vault_manager.py:317-327。
func DecodeRecoveryCode(code string) ([]byte, error) {
	cleaned := strings.NewReplacer("-", "", " ", "").Replace(strings.ToUpper(code))
	if len(cleaned) != protocol.RecoveryCodeLen {
		return nil, errors.New("恢复码长度不符")
	}
	secret, err := cryptox.B32DecodeNoPad(cleaned)
	if err != nil {
		return nil, errors.New("恢复码编码非法")
	}
	return secret, nil
}

// FormatRecoveryCode 恢复码按 4 字符分组展示（XXXX-XXXX-XXXX-XXXX）。
func FormatRecoveryCode(code string) string {
	var b strings.Builder
	for i := 0; i < len(code); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		end := i + 4
		if end > len(code) {
			end = len(code)
		}
		b.WriteString(code[i:end])
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 恢复码（v3）：生成 / 凭码开库
// ---------------------------------------------------------------------------

// GenerateRecoveryCode 生成（或更换）恢复码：重加密 Marker 并追加恢复块后
// 覆盖上传。旧恢复码在新码生效后立即失效；密钥与恢复码本身不落盘/上传。
// 对照 vault_manager.py:240-273。
func (m *Manager) GenerateRecoveryCode(ctx context.Context, masterPassword, vaultPath string, onProgress ProgressFn) (string, *cryptox.VaultMetadata, error) {
	meta, err := m.Open(ctx, masterPassword, vaultPath, onProgress)
	if err != nil {
		return "", nil, err
	}

	report(onProgress, "正在生成新恢复码…")
	secret, err := randomSecret()
	if err != nil {
		return "", nil, err
	}
	code := EncodeRecoveryCode(secret)
	report(onProgress, "派生保护密钥（约需数秒）…")
	blob, err := cryptox.BuildRecoveryBlob(secret, masterPassword)
	if err != nil {
		return "", nil, err
	}

	report(onProgress, "加密回写密库文件…")
	data, err := cryptox.CreateMarker(*meta, masterPassword, blob)
	if err != nil {
		return "", nil, err
	}
	if err := m.uploadMarker(ctx, data, vaultPath); err != nil {
		return "", nil, err
	}
	out := *meta
	out.HasRecovery = true
	return code, &out, nil
}

// OpenWithRecovery 凭恢复码开库：解密恢复块还原主密码，再走正常校验。
//
// 成功时同时返回还原出的主密码（供调用方构建会话；仅内存，不落盘）。
// 失败一律返回 ErrBadRecovery（与 Python 端 open_vault_with_recovery 的
// None 语义对齐，该错误文案覆盖「码无效/无恢复块/无密库/校验失败」）。
// 对照 vault_manager.py:275-310。
func (m *Manager) OpenWithRecovery(ctx context.Context, recoveryCode, vaultPath string, onProgress ProgressFn) (*cryptox.VaultMetadata, string, error) {
	report(onProgress, "正在解析恢复码…")
	secret, err := DecodeRecoveryCode(recoveryCode)
	if err != nil {
		return nil, "", ErrBadRecovery
	}
	report(onProgress, "正在载入密库文件…")
	data, err := m.downloadMarker(ctx, vaultPath)
	if err != nil {
		return nil, "", ErrBadRecovery // 无密库也归「不存在带恢复码的密库」
	}

	_, tail := cryptox.SplitRecoveryTail(data)
	if len(tail) < 2 {
		return nil, "", ErrBadRecovery
	}
	blobLen := int(binary.BigEndian.Uint16(tail[:2]))
	// 长度守卫在 Go 是硬需求：切片越界直接 panic，而 Python 切片
	// 超界只是自然截断再由 len 比较兜底——这里先检查再切，语义等价
	if blobLen > len(tail)-2 {
		return nil, "", ErrBadRecovery
	}
	blob := tail[2 : 2+blobLen]

	report(onProgress, "正在还原主密码…")
	masterPassword, err := cryptox.DecryptRecoveryBlob(blob, secret)
	if err != nil {
		return nil, "", ErrBadRecovery
	}
	report(onProgress, "校验密库（密钥派生，约需数秒）…")
	meta, err := cryptox.VerifyMarker(data, masterPassword)
	if err != nil {
		return nil, "", ErrBadRecovery
	}
	return meta, masterPassword, nil
}
