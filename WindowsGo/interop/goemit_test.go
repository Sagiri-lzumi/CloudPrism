package interop

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
)

// goDir 是反向夹具（Go 产出、Python 消费）在 testdata 下的子目录。
const goDir = "go"

// buildGoCpenc 按 go_emit 规格在 Go 侧构造一个 .cpenc 容器。
//
// 规格里的 salt/iv 全是定值，故产物逐字节可复现 —— 这正是它能当漂移守卫
// 的前提。Python 侧的 Encryptor 无法注入 IV，反向夹具只能由 Go 自己定值构造。
func buildGoCpenc(t *testing.T, password string, spec goEmitCpenc) []byte {
	t.Helper()

	plain := recipePlain(t, spec.Recipe, spec.Size)
	return buildCpenc(t, password, plain,
		mustHex(t, spec.Salt, spec.Slug+" 的 salt"),
		mustHex(t, spec.IV, spec.Slug+" 的 iv"),
		spec.Flags, spec.Version)
}

// buildGoVault 按 go_emit 规格在 Go 侧构造一个 Vault Marker。
//
// 第二个返回值表示产物是否可复现：带恢复块时 rsalt 由 BuildRecoveryBlob
// 内部随机生成，每次调用都不同，因此不能参与字节级漂移比对（只能验证
// 「Python 解得开」）。规格未给恢复密钥时走 nil 分支，产物完全确定。
func buildGoVault(t *testing.T, spec goEmitVault) ([]byte, bool) {
	t.Helper()

	meta := cryptox.VaultMetadata{
		Version:         spec.Version,
		VaultID:         mustHex(t, spec.VaultID, spec.Slug+" 的 vault_id"),
		Salt:            mustHex(t, spec.Salt, spec.Slug+" 的 salt"),
		IV:              mustHex(t, spec.IV, spec.Slug+" 的 iv"),
		FilenameEnc:     spec.FilenameEnc,
		ProtocolVersion: spec.ProtocolVersion,
		Name:            spec.Name,
	}

	var blob []byte
	reproducible := true
	if spec.RecoverySecret != nil {
		secret := mustHex(t, *spec.RecoverySecret, spec.Slug+" 的恢复密钥")
		var err error
		if blob, err = cryptox.BuildRecoveryBlob(secret, spec.Password); err != nil {
			t.Fatalf("%s: 构造恢复块失败: %v", spec.Slug, err)
		}
		reproducible = false // rsalt 随机，产物每轮不同
	}

	data, err := cryptox.CreateMarker(meta, spec.Password, blob)
	if err != nil {
		t.Fatalf("%s: CreateMarker 失败: %v", spec.Slug, err)
	}
	return data, reproducible
}

// buildGoFilename 加密一个展示名，产物含随机 nonce，故不可复现。
func buildGoFilename(t *testing.T, key []byte, plain string) string {
	t.Helper()

	enc, err := cryptox.EncryptFilename(plain, key)
	if err != nil {
		t.Fatalf("加密文件名 %q 失败: %v", plain, err)
	}
	return enc
}

// goArtifactPath 把 go_emit 里的相对路径映射到磁盘绝对路径。
func goArtifactPath(rel string) string {
	return filepath.Join(testdataDir, filepath.FromSlash(rel))
}

// TestGoArtifactsAreUpToDate 是反向夹具的漂移守卫。
//
// testdata/go/ 里的文件是提交入库的「Go 侧产出快照」，Python 的反向测试
// 直接读它。若 Go 的实现变了而快照没重新生成，Python 那边会一直绿着
// 验证一份**过期**的产物 —— 互操作契约名存实亡。这条测试在常规 go test
// 里就地重新推导快照并与已提交内容比对，把这种漂移变成即时失败。
//
// 只对可复现的产物做字节比对；含随机 rsalt / nonce 的产物退化为
// 「Go 自己能解开且字段正确」的自洽校验。
func TestGoArtifactsAreUpToDate(t *testing.T) {
	v := loadVectors(t)

	t.Run("cpenc", func(t *testing.T) {
		for _, spec := range v.GoEmit.Cpenc {
			t.Run(spec.Slug, func(t *testing.T) {
				want := buildGoCpenc(t, v.Meta.MasterPassword, spec)
				got, err := os.ReadFile(goArtifactPath(spec.Path))
				if err != nil {
					t.Fatalf("反向夹具缺失，请运行 %s=1 go test ./interop/ -run Emit 后提交: %v", emitEnvVar, err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("已提交的 %s 与 Go 当前产物不一致（实现改了但快照没重生成）\n  %s",
						spec.Path, firstDiff(got, want))
				}
			})
		}
	})

	t.Run("vault", func(t *testing.T) {
		for _, spec := range v.GoEmit.Vault {
			t.Run(spec.Slug, func(t *testing.T) {
				committed, err := os.ReadFile(goArtifactPath(spec.Path))
				if err != nil {
					t.Fatalf("反向夹具缺失，请运行 %s=1 go test ./interop/ -run Emit 后提交: %v", emitEnvVar, err)
				}

				rebuilt, reproducible := buildGoVault(t, spec)
				if reproducible && !bytes.Equal(committed, rebuilt) {
					t.Fatalf("已提交的 %s 与 Go 当前产物不一致（实现改了但快照没重生成）\n  %s",
						spec.Path, firstDiff(committed, rebuilt))
				}

				// 无论是否可复现，都必须能被 Go 自己正确解开
				meta, err := cryptox.VerifyMarker(committed, spec.Password)
				if err != nil {
					t.Fatalf("校验已提交的 %s 失败: %v", spec.Path, err)
				}
				if meta.Name != spec.ExpectName {
					t.Errorf("name = %q，应为 %q", meta.Name, spec.ExpectName)
				}
				if meta.HasRecovery != spec.ExpectHasRecovery {
					t.Errorf("has_recovery = %v，应为 %v", meta.HasRecovery, spec.ExpectHasRecovery)
				}
				if meta.Version != spec.Version || meta.FilenameEnc != spec.FilenameEnc {
					t.Errorf("version/filename_enc = %d/%v，应为 %d/%v",
						meta.Version, meta.FilenameEnc, spec.Version, spec.FilenameEnc)
				}

				// 带恢复块时，尾部必须能被恢复密钥解开成主密码
				if spec.RecoverySecret != nil {
					_, tail := cryptox.SplitRecoveryTail(committed)
					if len(tail) < 2 {
						t.Fatalf("恢复块尾部只有 %d 字节，装不下长度字段", len(tail))
					}
					got, err := cryptox.DecryptRecoveryBlob(tail[2:], mustHex(t, *spec.RecoverySecret, "恢复密钥"))
					if err != nil {
						t.Fatalf("解密自己写入的恢复块失败: %v", err)
					}
					if got != spec.Password {
						t.Errorf("恢复块解出 %q，应为 %q", got, spec.Password)
					}
				}
			})
		}
	})

	t.Run("filename", func(t *testing.T) {
		key := mustHex(t, v.GoEmit.Filename.Key, "反向夹具文件名密钥")
		for _, item := range v.GoEmit.Filename.Items {
			t.Run(item.Plain, func(t *testing.T) {
				raw, err := os.ReadFile(goArtifactPath(item.Path))
				if err != nil {
					t.Fatalf("反向夹具缺失，请运行 %s=1 go test ./interop/ -run Emit 后提交: %v", emitEnvVar, err)
				}
				// nonce 随机，无法比对字节；只能验证「解得开且等于期望明文」
				got, err := cryptox.DecryptFilename(string(bytes.TrimRight(raw, "\r\n")), key)
				if err != nil {
					t.Fatalf("解密已提交的 %s 失败: %v", item.Path, err)
				}
				if got != item.Plain {
					t.Errorf("%s 解出 %q，应为 %q", item.Path, got, item.Plain)
				}
			})
		}
	})
}

// TestEmitGoArtifacts 写出反向夹具（testdata/go/），供 Python 侧验证。
//
// 默认跳过：常规 go test 必须只读不写。文件名 nonce 与恢复块 rsalt 都是
// 随机的，若每轮都重写，工作树会恒脏、提交历史里全是无意义的二进制抖动。
//
// 重新生成的时机：协议改动、或 TestGoArtifactsAreUpToDate 报漂移时。
//
//	$env:CLOUDPRISM_INTEROP_EMIT = "1"
//	go test ./interop/ -run Emit -v
//
// 生成后必须把 testdata/go/ 一起提交，否则 Python 侧的反向测试会 skip。
func TestEmitGoArtifacts(t *testing.T) {
	if os.Getenv(emitEnvVar) != "1" {
		t.Skipf("未设置 %s=1，跳过写出反向夹具（常规运行只读不写）", emitEnvVar)
	}

	v := loadVectors(t)
	written := 0

	write := func(rel string, data []byte) {
		t.Helper()
		p := goArtifactPath(rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("创建目录 %s 失败: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatalf("写入 %s 失败: %v", rel, err)
		}
		written++
		t.Logf("已写出 %s（%d 字节）", rel, len(data))
	}

	for _, spec := range v.GoEmit.Cpenc {
		write(spec.Path, buildGoCpenc(t, v.Meta.MasterPassword, spec))
	}
	for _, spec := range v.GoEmit.Vault {
		data, _ := buildGoVault(t, spec)
		write(spec.Path, data)
	}

	key := mustHex(t, v.GoEmit.Filename.Key, "反向夹具文件名密钥")
	for _, item := range v.GoEmit.Filename.Items {
		// 末尾加换行便于人眼查看；Python 侧读取时会 strip
		write(item.Path, []byte(buildGoFilename(t, key, item.Plain)+"\n"))
	}

	t.Logf("反向夹具共写出 %d 个文件，请一并提交 testdata/%s/", written, goDir)
}
