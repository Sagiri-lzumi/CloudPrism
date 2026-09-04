package bind

import (
	"context"
	"errors"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// Vault 是密库域的 Wails 绑定：连接/锁定/切换/恢复码/续传/统计入口。
//
// 只做薄适配：参数直传 appstate，错误经 Wrap 分类为带 Code 的 ApiError
// 抛给前端（禁止 import cryptox/protocol，见 docs/ARCHITECTURE.md）。
// 每个用户入口先 Activity 续期自动锁空闲计时。
type Vault struct {
	st  *appstate.State
	ctx *ContextHolder
}

// NewVault 构造密库域绑定。
func NewVault(st *appstate.State, ctx *ContextHolder) *Vault {
	return &Vault{st: st, ctx: ctx}
}

// Open 新建或连接密库（req.Create 决定分支）；新建成功返回一次性恢复码。
func (v *Vault) Open(req appstate.OpenRequest) (appstate.OpenVaultResult, error) {
	v.st.Activity()
	res, err := v.st.OpenVault(v.ctx.Context(), req)
	if err != nil {
		return res, Wrap(err)
	}
	return res, nil
}

// Lock 锁定当前密库（打断任务/清缓存/广播 st:locked 都在 core 完成）。
func (v *Vault) Lock() {
	v.st.Activity()
	v.st.Lock()
}

// State 取全局状态快照（页面初始化/下拉刷新的一次性查询）。
func (v *Vault) State() appstate.Snapshot {
	v.st.Activity()
	return v.st.Snapshot()
}

// RecentVaults 最近连接的密库记录（仅连接参数，任何密码均不落盘）。
func (v *Vault) RecentVaults() []map[string]any {
	return v.st.RecentVaults()
}

// ForgetRecent 移除一条最近连接记录。
func (v *Vault) ForgetRecent(key string) error {
	v.st.Activity()
	if err := v.st.ForgetRecent(key); err != nil {
		return Wrap(err)
	}
	return nil
}

// ListOtherVaults 当前后端下其他密库根路径列表（切换密库入口数据）。
func (v *Vault) ListOtherVaults() ([]string, error) {
	v.st.Activity()
	list, err := v.st.ListOtherVaults(v.ctx.Context())
	if err != nil {
		return nil, Wrap(err)
	}
	return list, nil
}

// ConnectOtherVault 用主密码连接同后端下的另一密库。
func (v *Vault) ConnectOtherVault(vaultPath, masterPassword string) error {
	v.st.Activity()
	if err := v.st.ConnectOtherVault(v.ctx.Context(), vaultPath, masterPassword); err != nil {
		return Wrap(err)
	}
	return nil
}

// RenameVault 重命名当前密库（展示名，不影响加密密钥）。
func (v *Vault) RenameVault(newName string) error {
	v.st.Activity()
	if err := v.st.RenameCurrentVault(v.ctx.Context(), newName); err != nil {
		return Wrap(err)
	}
	return nil
}

// RegenerateRecoveryCode 用主密码重新生成恢复码（旧码立即作废）。
func (v *Vault) RegenerateRecoveryCode(masterPassword string) (string, error) {
	v.st.Activity()
	code, err := v.st.RegenerateRecoveryCode(v.ctx.Context(), masterPassword)
	if err != nil {
		return "", Wrap(err)
	}
	return code, nil
}

// ResumePending 重建上次锁库/退出时未完成的上传任务，返回恢复数量。
func (v *Vault) ResumePending() (int, error) {
	v.st.Activity()
	n, err := v.st.ResumePending()
	if err != nil {
		return 0, Wrap(err)
	}
	return n, nil
}

// RequestStats 触发云端占用统计（结果经状态帧回填，不阻塞等待）。
func (v *Vault) RequestStats() error {
	v.st.Activity()
	v.st.RequestStats(v.ctx.Context())
	return nil
}

// Activity 报告一次用户界面交互（自动锁空闲计时刷新）。
func (v *Vault) Activity() { v.st.Activity() }

// ---------------------------------------------------------------------------
// 百度网盘授权（向导百度卡）：oob 授权码模式，凭证 DPAPI 加密落盘
// data/baidu.json（与 Python 端磁盘格式一致，可互读）。
// ---------------------------------------------------------------------------

// BaiduAuthInfo 百度授权状态摘要（前端向导卡渲染用；AppKey 掩码防误抄）。
type BaiduAuthInfo struct {
	Authorized bool   `json:"authorized"`
	AppKey     string `json:"appKey"`
	AppID      string `json:"appId"`
}

// BaiduStatus 查询是否已完成百度授权（Load 损坏/缺失一律视为未授权）。
func (v *Vault) BaiduStatus() BaiduAuthInfo {
	store := v.st.BaiduCreds()
	if store == nil {
		return BaiduAuthInfo{}
	}
	d := store.Load()
	if d == nil || d.AccessToken == "" {
		return BaiduAuthInfo{}
	}
	return BaiduAuthInfo{
		Authorized: true,
		AppKey:     maskBaiduKey(d.AppKey),
		AppID:      maskBaiduKey(d.AppID),
	}
}

// BaiduAuthURL 校验凭证后打开系统浏览器进入百度授权页，返回授权地址
// （oob 模式：页面登录同意后直接展示一次性 code，无回调）。
func (v *Vault) BaiduAuthURL(appID, appKey string) (string, error) {
	v.st.Activity()
	appKey = strings.TrimSpace(appKey)
	if appKey == "" {
		return "", Wrap(errors.New("请先填写 AppKey（必填）"))
	}
	u := storage.BaiduAuthURL(appKey, strings.TrimSpace(appID))
	if ctx := v.ctx.Context(); ctx != nil {
		// 打开失败不阻断：返回 URL 供前端展示/复制兜底
		wruntime.BrowserOpenURL(ctx, u)
	}
	return u, nil
}

// BaiduSaveAuth 用 oob 授权码换 token 并加密落盘（四凭证 + code 一次提交）。
// code 一次性：失败后需重新走 BaiduAuthURL 拿新码。
func (v *Vault) BaiduSaveAuth(appID, appKey, secretKey, signKey, code string) error {
	v.st.Activity()
	appKey = strings.TrimSpace(appKey)
	secretKey = strings.TrimSpace(secretKey)
	code = strings.TrimSpace(code)
	if appKey == "" || secretKey == "" {
		return Wrap(errors.New("请填写 AppKey 与 SecretKey"))
	}
	if code == "" {
		return Wrap(errors.New("请粘贴授权页展示的 code"))
	}
	store := v.st.BaiduCreds()
	if store == nil {
		return Wrap(errors.New("百度凭证存储未装配"))
	}
	// 授权交换是真实网络往返，统一带超时防悬挂
	ctx, cancel := context.WithTimeout(v.ctx.Context(), 60*time.Second)
	defer cancel()
	cred, err := storage.ExchangeBaiduToken(ctx, appKey, secretKey, code)
	if err != nil {
		return Wrap(err)
	}
	cred.AppID = strings.TrimSpace(appID)
	cred.SignKey = strings.TrimSpace(signKey)
	if err := store.Save(cred); err != nil {
		return Wrap(err)
	}
	return nil
}

// BaiduClearAuth 清除本地百度授权记录（仅删本地凭证，不影响百度云端数据）。
func (v *Vault) BaiduClearAuth() error {
	v.st.Activity()
	store := v.st.BaiduCreds()
	if store == nil {
		return Wrap(errors.New("百度凭证存储未装配"))
	}
	store.Clear()
	return nil
}

// maskBaiduKey 掩码展示敏感键：超过 4 位只留前 4 位（对齐 Python 摘要口径）。
func maskBaiduKey(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[:4] + "…"
}

// ChooseLocalDir 弹目录选择框返回密库存放目录；取消返回空串（非错误）。
// 向导本地卡「浏览」按钮用；标题与 Settings.ChooseSyncDir 区分语义。
func (v *Vault) ChooseLocalDir() (string, error) {
	dir, err := wruntime.OpenDirectoryDialog(v.ctx.Context(), wruntime.OpenDialogOptions{
		Title:                "选择密库存放的本地文件夹",
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", Wrap(err)
	}
	return dir, nil
}
