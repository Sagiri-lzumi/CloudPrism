package storage

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// 百度凭证的加密落盘存储。对照 baidu_backend.py:110-158（BaiduCredentialStore）
// 与 gui/baidu_auth.py:180-188（落盘 JSON 的键拼写）。
//
// 磁盘格式与 Python 完全一致：
//
//	"DPAPI:" + base64(protect(utf8(json)))   —— DPAPI 加密（Windows）
//	"PLAIN:" + base64(utf8(json))            —— 明文兜底（非 Windows/测试）
//
// JSON 键：app_id/app_key/secret_key/sign_key/access_token/refresh_token/
// expires_at —— Go 侧通过 BaiduCredData 的 json tag 双向兼容 Python 端
// json.dumps/loads（Go 紧凑 JSON 可被 json.loads 读取，阶段 2 S3 已验证）。

// BaiduCredData 是百度后端的完整凭证与 token 快照。
// sign_key 不参与 API 调用，仅授权流程透传，保留它以便旧文件读回再写不丢字段。
type BaiduCredData struct {
	AppID        string  `json:"app_id"`
	AppKey       string  `json:"app_key"`
	SecretKey    string  `json:"secret_key"`
	SignKey      string  `json:"sign_key"`
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresAt    float64 `json:"expires_at"`
}

// BaiduProtector 抽象凭证加密实现：DPAPI 由 internal/platform/win 提供，
// 装配层（internal/appstate）注入；其余环境用 PLAIN 兜底。
type BaiduProtector interface {
	// Protect 加密；Unprotect 解密；Scheme 返回落盘前缀（"DPAPI"/"PLAIN"）。
	Protect(data []byte) ([]byte, error)
	Unprotect(data []byte) ([]byte, error)
	Scheme() string
}

// plainProtector 是 Base64 明文兜底（对齐 Python 非 Windows 的 PLAIN 分支）。
type plainProtector struct{}

func (plainProtector) Protect(d []byte) ([]byte, error)   { return d, nil }
func (plainProtector) Unprotect(d []byte) ([]byte, error) { return d, nil }
func (plainProtector) Scheme() string                     { return "PLAIN" }

// BaiduCredStore 负责凭证文件的读写（Save/Load/Clear 三方法对应 Python）。
type BaiduCredStore struct {
	path string
	prot BaiduProtector
}

// NewBaiduCredStore 构造凭证存储；prot 为 nil 时退化为 PLAIN 明文兜底。
func NewBaiduCredStore(path string, prot BaiduProtector) *BaiduCredStore {
	if prot == nil {
		prot = plainProtector{}
	}
	return &BaiduCredStore{path: path, prot: prot}
}

// Save 序列化并加密写入。Protect 失败（如 DPAPI 在非 Windows 不可用）时
// 自动降级 PLAIN 明文（对齐 Python save 的 except (OSError, ...) 链）。
func (s *BaiduCredStore) Save(d BaiduCredData) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	// 优先走注入的加密器；失败时明文兜底，保证凭证仍可读写
	payload, err := s.prot.Protect(raw)
	scheme := s.prot.Scheme()
	if err != nil {
		payload = raw
		scheme = "PLAIN"
	}
	blob := scheme + ":" + base64.StdEncoding.EncodeToString(payload)

	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(s.path, []byte(blob), 0o600)
}

// Load 读取并解密凭证；文件不存在或任何环节损坏都返回 nil（不报错），
// 对齐 Python load 的 except Exception: return None —— 上层把 nil 视为
// 「未授权」，走授权向导而不是把坏文件当故障弹给用户。
func (s *BaiduCredStore) Load() *BaiduCredData {
	payload, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var raw []byte
	// 按前缀分支：DPAPI: 只能由注入的 DPAPI protector 解开；PLAIN: 直接 b64。
	switch {
	case len(payload) > 6 && string(payload[:6]) == "DPAPI:":
		ct, err := base64.StdEncoding.DecodeString(string(payload[6:]))
		if err != nil {
			return nil
		}
		if s.prot.Scheme() != "DPAPI" {
			return nil // 本进程没有 DPAPI 能力，解不开加密凭证
		}
		raw, err = s.prot.Unprotect(ct)
		if err != nil {
			return nil
		}
	case len(payload) > 6 && string(payload[:6]) == "PLAIN:":
		var err error
		raw, err = base64.StdEncoding.DecodeString(string(payload[6:]))
		if err != nil {
			return nil
		}
	default:
		return nil // 未知前缀：不是本程序写的文件
	}
	var d BaiduCredData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	return &d
}

// Clear 删除凭证文件；文件不存在时容忍（对齐 Python clear 的 except OSError）。
func (s *BaiduCredStore) Clear() {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Python 静默吞一切 OSError；这里同样不向调用方报错
		_ = err
	}
}
