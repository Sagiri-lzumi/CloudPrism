package bind

import (
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
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
