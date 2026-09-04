package appstate

// 密库级操作（密库信息页 / 设置页入口）：重命名、恢复码生成——对照
// app.py _on_rename_vault 与 _on_generate_recovery_code。两者都需重加密
// Marker（秒级派生）并在成功后同步连接上下文与最近记录。
//
// 凭恢复码开库在 connect.go OpenVault 编排（向导连接模式可空恢复码字段），
// 本文件只收口「连接态内」的密库管理动作。

import (
	"context"
	"errors"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/vault"
)

// RenameCurrentVault 修改当前密库名称：重加密 Marker 并覆盖上传（名称随
// 密库文件跨设备保存）。失败时连接上下文不变（对照 Python：异常分支仅弹窗）。
func (s *State) RenameCurrentVault(ctx context.Context, newName string) error {
	conn, err := s.requireConn()
	if err != nil {
		return err
	}
	newName = trimSpace(newName)
	if newName == "" {
		return errors.New("名称不能为空")
	}
	opCtx, cancel := contextWithTimeout(ctx, opTimeout)
	defer cancel()

	// 重命名需用主密码重加密 Marker：取自会话（密码仅驻内存，不落盘）
	meta, err := vault.NewManager(conn.backend).Rename(
		opCtx, conn.sess.MasterPassword(), newName, conn.vaultPath,
	)
	if err != nil {
		return err
	}

	// 换入新 meta（vault_name / HasRecovery 等字段变化），最近记录同步新名
	s.mu.Lock()
	conn.meta = meta
	s.mu.Unlock()
	s.rememberCurrentVault(conn, s.webdavUserOf(conn.backend))
	return nil
}

// RegenerateRecoveryCode 生成/更换恢复码：主密码确认身份后重加密 Marker，
// 返回 4 字符分组的展示码（XXXX-XXXX-…）。新码生效即旧码失效。
// 对照 app.py _on_generate_recovery_code 与 init_wizard 的展示对话框。
func (s *State) RegenerateRecoveryCode(ctx context.Context, masterPassword string) (string, error) {
	conn, err := s.requireConn()
	if err != nil {
		return "", err
	}
	opCtx, cancel := contextWithTimeout(ctx, opTimeout)
	defer cancel()

	mgr := vault.NewManager(conn.backend)
	code, meta, err := mgr.GenerateRecoveryCode(opCtx, masterPassword, conn.vaultPath,
		func(msg string) { s.emit(EventOpProgress, msg) },
	)
	if err != nil {
		return "", err // 主密码不符 → ErrBadPassword，绑定层映射文案
	}

	// 标记密库已带恢复码（信息页徽标随状态帧刷新）
	s.mu.Lock()
	conn.meta = meta
	s.mu.Unlock()
	return vault.FormatRecoveryCode(code), nil
}
