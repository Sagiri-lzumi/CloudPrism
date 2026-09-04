package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// newLocalManager 用临时目录搭一个本地后端 + 密库管理器。
func newLocalManager(t *testing.T) (*Manager, *storage.Local) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "backend_root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(b), b
}

// markerExistsOnLocal 直接检查本地根下的 Marker 文件（不经过后端接口，
// 用于验证「先删后传」没有残留双文件等实现细节）。
func markerExistsOnLocal(b *storage.Local, vaultPath string) bool {
	p := protocol.VaultMarkerName
	if vaultPath = strings.Trim(vaultPath, "/"); vaultPath != "" {
		p = filepath.Join(vaultPath, protocol.VaultMarkerName)
	}
	_, err := os.Stat(filepath.Join(b.Root(), p))
	return err == nil
}

// TestCreateOpenRename 建库 → 校验 → 改名全链路（根目录密库）。
func TestCreateOpenRename(t *testing.T) {
	ctx := context.Background()
	m, b := newLocalManager(t)

	ok, err := m.HasVault(ctx, "")
	if err != nil || ok {
		t.Fatalf("初始不应有密库: ok=%v err=%v", ok, err)
	}

	meta, err := m.Create(ctx, "主密码-1", true, "我的密库", "")
	if err != nil {
		t.Fatal(err)
	}
	if meta.FilenameEnc != true || meta.Name != "我的密库" {
		t.Errorf("元信息不符: %+v", meta)
	}
	if meta.Version != protocol.VaultVersion {
		t.Errorf("版本应为 %d，实得 %d", protocol.VaultVersion, meta.Version)
	}
	if !markerExistsOnLocal(b, "") {
		t.Fatal("Marker 应已上传到本地根")
	}

	// 密码正确可开；错误密码 ErrBadPassword
	got, err := m.Open(ctx, "主密码-1", "", nil)
	if err != nil || got == nil {
		t.Fatalf("正确密码应开库成功: %v", err)
	}
	if got.Name != "我的密库" || string(got.Salt) != string(meta.Salt) {
		t.Error("回读元信息应与建库一致")
	}
	if _, err := m.Open(ctx, "wrong-pw", "", nil); !errors.Is(err, ErrBadPassword) {
		t.Errorf("错误密码应报 ErrBadPassword，实得 %v", err)
	}

	// 改名：名称更新、salt/iv 不变、可用新名开库
	renamed, err := m.Rename(ctx, "主密码-1", "  新名字  ", "")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "新名字" {
		t.Errorf("名称应去首尾空白，实得 %q", renamed.Name)
	}
	if string(renamed.Salt) != string(meta.Salt) || string(renamed.VaultID) != string(meta.VaultID) {
		t.Error("改名不应动 salt/vault_id")
	}
	reopened, err := m.Open(ctx, "主密码-1", "", nil)
	if err != nil || reopened.Name != "新名字" {
		t.Fatalf("改名后应可用原密码开库且名称为新名: %v", err)
	}
}

// TestCreateVaultExists 重复建库必须拒绝（防覆盖）。
func TestCreateVaultExists(t *testing.T) {
	ctx := context.Background()
	m, _ := newLocalManager(t)
	if _, err := m.Create(ctx, "pw", false, "", ""); err != nil {
		t.Fatal(err)
	}
	_, err := m.Create(ctx, "pw", false, "", "")
	if !errors.Is(err, ErrVaultExists) {
		t.Errorf("重复建库应报 ErrVaultExists，实得 %v", err)
	}
}

// TestSubdirVaultAndList 子目录密库 + ListVaults 扫描两类密库。
func TestSubdirVaultAndList(t *testing.T) {
	ctx := context.Background()
	m, _ := newLocalManager(t)

	// 空后端：无密库
	vaults, err := m.ListVaults(ctx)
	if err != nil || len(vaults) != 0 {
		t.Fatalf("空后端应无密库: %v %v", vaults, err)
	}

	// 根密库
	if _, err := m.Create(ctx, "pw", false, "", ""); err != nil {
		t.Fatal(err)
	}
	// 一级子目录密库（目录自动创建）
	if _, _, err := m.CreateWithRecovery(ctx, "pw2", false, "", "team", nil); err != nil {
		t.Fatal(err)
	}
	// 二级子目录密库同样可建可连（mkdir 递归），但按协议边界
	// 不出现在一级列表扫描里（与 Python list_vaults 行为一致）
	if _, _, err := m.CreateWithRecovery(ctx, "pw3", false, "", "team/deep", nil); err != nil {
		t.Fatal(err)
	}

	vaults, err = m.ListVaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, v := range vaults {
		paths[v.Path] = true
	}
	if !paths[""] || !paths["team"] {
		t.Errorf("应同时扫到根密库与 team 一级子目录密库，实得 %v", vaults)
	}
	if paths["team/deep"] {
		t.Error("二级子目录密库不应出现在一级扫描里（协议边界）")
	}

	// 二级子目录密库可用 HasVault 直接命中
	ok, err := m.HasVault(ctx, "team")
	if err != nil || !ok {
		t.Errorf("team 密库应有 Marker: ok=%v err=%v", ok, err)
	}
	ok, err = m.HasVault(ctx, "team/deep")
	if err != nil || !ok {
		t.Errorf("team/deep 密库应有 Marker: ok=%v err=%v", ok, err)
	}
}

// TestOpenNoVault 位置无密库 → ErrNoVault。
func TestOpenNoVault(t *testing.T) {
	ctx := context.Background()
	m, _ := newLocalManager(t)
	_, err := m.Open(ctx, "pw", "", nil)
	if !errors.Is(err, ErrNoVault) {
		t.Errorf("无密库应报 ErrNoVault，实得 %v", err)
	}
}

// TestRecoveryLifecycle 建库带恢复码 → 凭码开库还原密码 → 密码可直开；
// 错码/无恢复块的库开不出。
func TestRecoveryLifecycle(t *testing.T) {
	ctx := context.Background()
	m, _ := newLocalManager(t)

	var progress []string
	meta, code, err := m.CreateWithRecovery(ctx, "原密码", true, "带码库", "", func(msg string) {
		progress = append(progress, msg)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !meta.HasRecovery {
		t.Error("create_with_recovery 后 HasRecovery 应为 true")
	}
	if len(progress) == 0 {
		t.Error("应产生阶段进度回调")
	}
	// 16 字符恢复码应完整分组为 XXXX-XXXX-XXXX-XXXX（19 字符、3 个连字符）
	if got := FormatRecoveryCode(code); len(got) != protocol.RecoveryCodeLen+3 || strings.Count(got, "-") != 3 {
		t.Errorf("恢复码分组异常: %q", got)
	}

	// 凭码开库：还原出主密码
	meta2, recovered, err := m.OpenWithRecovery(ctx, code, "", nil)
	if err != nil || meta2 == nil {
		t.Fatalf("凭正确恢复码应开库: %v", err)
	}
	if recovered != "原密码" {
		t.Errorf("应还原出原密码，实得 %q", recovered)
	}

	// 带分隔符/小写的码同样可解析（清洗逻辑）
	lower := strings.ToLower(code)
	meta3, _, err := m.OpenWithRecovery(ctx, FormatRecoveryCode(lower), "", nil)
	if err != nil || meta3 == nil {
		t.Fatalf("带分隔符小写码应可开库: %v", err)
	}

	// 错误码 ErrBadRecovery
	if _, _, err := m.OpenWithRecovery(ctx, "AAAAAAAAAAAAAAAA", "", nil); !errors.Is(err, ErrBadRecovery) {
		t.Errorf("错码应报 ErrBadRecovery，实得 %v", err)
	}
}

// TestGenerateRecoveryCodeOnOldVault 对无恢复码的旧库补发恢复码。
func TestGenerateRecoveryCodeOnOldVault(t *testing.T) {
	ctx := context.Background()
	m, _ := newLocalManager(t)
	if _, err := m.Create(ctx, "pw", false, "", ""); err != nil {
		t.Fatal(err)
	}

	code, meta, err := m.GenerateRecoveryCode(ctx, "pw", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.HasRecovery || len(code) != protocol.RecoveryCodeLen {
		t.Errorf("生成恢复码后元信息/码长不符: %q %+v", code, meta)
	}
	// 旧码补发后凭码可开
	_, recovered, err := m.OpenWithRecovery(ctx, code, "", nil)
	if err != nil || recovered != "pw" {
		t.Fatalf("凭新码应可开库: err=%v pw=%q", err, recovered)
	}
	// 密码错误时不能生成（防篡改）
	if _, _, err := m.GenerateRecoveryCode(ctx, "bad", "", nil); !errors.Is(err, ErrBadPassword) {
		t.Errorf("错密码生成恢复码应报 ErrBadPassword，实得 %v", err)
	}
}

// TestRenameKeepsRecovery 改名保留既有恢复块：改名后旧恢复码仍可开库。
func TestRenameKeepsRecovery(t *testing.T) {
	ctx := context.Background()
	m, _ := newLocalManager(t)
	_, code, err := m.CreateWithRecovery(ctx, "pw", false, "原名", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rename(ctx, "pw", "新名", ""); err != nil {
		t.Fatal(err)
	}
	_, recovered, err := m.OpenWithRecovery(ctx, code, "", nil)
	if err != nil || recovered != "pw" {
		t.Fatalf("改名后旧恢复码应仍有效: err=%v", err)
	}
}

// TestRecoveryCodeCoding 编解码与清洗的边界行为。
func TestRecoveryCodeCoding(t *testing.T) {
	// 10 字节密钥 → 16 字符无填充 Base32
	secret := make([]byte, protocol.RecoverySecretLen)
	for i := range secret {
		secret[i] = byte(i*7 + 1)
	}
	code := EncodeRecoveryCode(secret)
	if len(code) != protocol.RecoveryCodeLen {
		t.Fatalf("码长应为 %d，实得 %d", protocol.RecoveryCodeLen, len(code))
	}
	got, err := DecodeRecoveryCode(code)
	if err != nil || string(got) != string(secret) {
		t.Fatalf("解码应还原密钥: %v", err)
	}
	if FormatRecoveryCode("ABCDEFGHIJKLMNOP") != "ABCD-EFGH-IJKL-MNOP" {
		t.Error("4 字符分组错误")
	}

	// 错误输入（Base32 字母表不含 0/1/8/9 与标点）
	for _, bad := range []string{"", "SHORT", "!!!!", "ABCDEFGHIJKLMNOPQ", "AAAA0AAAAAAAAAAA"} {
		if _, err := DecodeRecoveryCode(bad); err == nil {
			t.Errorf("非法码 %q 应拒绝", bad)
		}
	}
}

// TestProgressCallbackPanicSafety 进度回调 panic 不拖垮主流程。
func TestProgressCallbackPanicSafety(t *testing.T) {
	ctx := context.Background()
	m, _ := newLocalManager(t)
	bad := func(string) { panic("boom") }
	if _, err := m.Create(ctx, "pw", false, "", ""); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.GenerateRecoveryCode(ctx, "pw", "", bad)
	if err != nil {
		t.Fatalf("回调 panic 不应中断流程: %v", err)
	}
}
