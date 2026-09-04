// Package settings 提供应用设置的 JSON 持久化与旧版 INI 一次性导入。
//
// Python 端（settings_store.py）用 QSettings(IniFormat) 把设置写在程序目录
// 旁的 data/cloudprism.ini；Go 端没有 QSettings，设置以 JSON 键值文件
// （data/cloudprism_settings.json）持久化。**键名与 Python 侧完全一致**
// （"appearance/theme_index" 等），旧 INI 导入时逐键迁移、无需映射表。
//
// 值语义与 QSettings 对齐：全部字符串化存储（"0" / "512"），读取时按需
// 转换，缺失或损坏一律回退默认值——设置文件损坏绝不阻塞启动。
// 密码/token 等秘密一律不入本存储（Python 端同约束）。
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 设置键常量：与 Python 端 SettingsStore 的 QSettings key 逐字一致
// （对照 settings_store.py 各 getter/setter），值类型见各常量注释。
const (
	// 外观
	KeyThemeIndex = "appearance/theme_index" // 0=跟随系统 1=深色 2=浅色
	KeyFontSize   = "appearance/font_size"   // 默认 14

	// 缓存
	KeyCacheLimitMB = "cache/limit_mb" // 默认 512
	KeyCachePath    = "cache/path"     // 空 = 默认临时目录

	// 传输 / 性能 / 安全
	KeyChunkIndex       = "transfer/chunk_index"     // 0=256KB 1=512KB 2=1MB 3=4MB
	KeyConcurrent       = "transfer/concurrent"      // 并发任务数（1~4），默认 2
	KeyPendingTransfers = "transfer/pending"         // 未完成传输 JSON 字符串
	KeySyncLocalDir     = "sync/local_dir"           // 文件夹同步本地目录
	KeyMaxCores         = "perf/max_cores"           // 加密最大内核数，0=未设置
	KeyAutoLockIndex    = "security/auto_lock_index" // 0=从不 1/2/3=5/15/30 分钟
	KeyBackendType      = "conn/backend_type"        // local / webdav / baidu
	KeyLocalDir         = "conn/local_dir"
	KeyWebDAVURL        = "conn/webdav_url"
	KeyWebDAVUser       = "conn/webdav_user"

	// 最近连接的密库记录（仅连接参数，任何密码均不落盘）
	KeyRecentVaults = "vaults/recent"
)

// 记录上限（新记录置顶，超出时淘汰最旧），对照 settings_store.py:186。
const MaxRecentVaults = 8

// Store 是设置键值的 JSON 持久化存储，并发安全。
type Store struct {
	mu     sync.Mutex
	path   string
	values map[string]string
}

// Open 载入 JSON 设置文件。
//
// 文件不存在 → 空存储；文件损坏 / 读取失败 → 空存储（容错，对齐 QSettings
// 对坏 INI 的宽容度）。错误仅在 path 为空的参数错误时返回。
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("settings: 存储路径不能为空")
	}
	s := &Store{path: path, values: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return s, nil // 不存在 / 不可读都按空存储降级，不阻塞启动
	}
	if err := json.Unmarshal(data, &s.values); err != nil {
		s.values = map[string]string{} // 损坏容错：宁可丢设置也不丢启动
	}
	if s.values == nil {
		s.values = map[string]string{}
	}
	return s, nil
}

// Path 返回存储文件路径。
func (s *Store) Path() string { return s.path }

// Get 读取字符串值；缺失返回默认值。
func (s *Store) Get(key, def string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.values[key]; ok {
		return v
	}
	return def
}

// Set 写入字符串值（仅内存，Sync 落盘）。
func (s *Store) Set(key, val string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = val
}

// Int 读取整数；缺失或转换失败返回默认值（对照 Python _get cast 语义）。
func (s *Store) Int(key string, def int) int {
	v, err := strconv.Atoi(s.Get(key, ""))
	if err != nil {
		return def
	}
	return v
}

// SetInt 写入整数（字符串化存储，与 QSettings 一致）。
func (s *Store) SetInt(key string, v int) { s.Set(key, strconv.Itoa(v)) }

// JSONList 读取「JSON 字符串编码的对象列表」（pending 传输 / 最近密库），
// 缺失或损坏容错返回空列表（对照 Python pending_transfers / recent_vaults）。
func (s *Store) JSONList(key string) []map[string]any {
	raw := s.Get(key, "")
	if raw == "" {
		return nil
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	return items
}

// SetJSONList 把对象列表编码为 JSON 字符串写入；空列表即清空
// （对照 Python 端空列表写入空串的语义）。
func (s *Store) SetJSONList(key string, items []map[string]any) {
	if len(items) == 0 {
		s.Set(key, "")
		return
	}
	data, err := json.Marshal(items)
	if err != nil {
		s.Set(key, "")
		return
	}
	s.Set(key, string(data))
}

// Sync 原子写盘：先写同目录临时文件再 rename 覆盖，避免写一半崩溃
// 留下截断的 JSON（截断文件下次 Open 会被容错成空设置）。
func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]string, 0, len(s.values))
	for k := range s.values {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 稳定输出：map 无序，先排序再手写 JSON

	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(s.values[k])
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// 临时文件残留清理（rename 成功后此文件已不存在，Remove 幂等无害）
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// ---------------------------------------------------------------------------
// 最近密库记录（settings_store.py:181-241 的对等移植）
// ---------------------------------------------------------------------------

// recordString 取出 map 中键的字符串值；缺失/非字符串一律空串
// （对齐 Python 的 rec.get(key, "") 语义，nil 不产出 "<nil>"）。
func recordString(r map[string]any, key string) string {
	if r == nil {
		return ""
	}
	switch v := r[key].(type) {
	case string:
		return v
	default:
		return ""
	}
}

// recordKey 计算记录唯一键：后端类型 + 后端路径 + 密库位置（子目录）。
//
// 同一后端上根目录与子目录密库共存，必须把位置纳入键，否则两者互相
// 覆盖导致重连开错位置。对照 settings_store.py:203-216。
func recordKey(r map[string]any) string {
	return strings.Join([]string{
		recordString(r, "backend_type"),
		recordString(r, "path"),
		strings.Trim(recordString(r, "vault_path"), "/"),
	}, "|")
}

// copyRecord 浅拷贝记录 map（写操作不改调用方持有的 map）。
func copyRecord(r map[string]any) map[string]any {
	out := make(map[string]any, len(r)+2)
	for k, v := range r {
		out[k] = v
	}
	return out
}

// RememberVault 记录一次成功连接：按三元组去重置顶，刷新时间，截断上限。
//
// record 至少含 backend_type 与 path；可含 vault_path（子目录密库）。
// 旧版记录无 vault_path，按空位置参与归一，不会产生重复项。
// 对照 settings_store.py:218-236。
func (s *Store) RememberVault(record map[string]any) {
	rec := copyRecord(record)
	rec["vault_path"] = strings.Trim(recordString(record, "vault_path"), "/")
	if recordString(rec, "key") == "" {
		rec["key"] = recordKey(rec)
	}
	rec["last_used"] = nowStamp()

	key := recordKey(rec)
	var kept []map[string]any
	for _, r := range s.JSONList(KeyRecentVaults) {
		if recordKey(r) != key {
			kept = append(kept, r)
		}
	}
	kept = append([]map[string]any{rec}, kept...)
	if len(kept) > MaxRecentVaults {
		kept = kept[:MaxRecentVaults]
	}
	s.SetJSONList(KeyRecentVaults, kept)
}

// ForgetVault 删除指定 key 的记录。对照 settings_store.py:238-241。
func (s *Store) ForgetVault(key string) {
	var kept []map[string]any
	for _, r := range s.JSONList(KeyRecentVaults) {
		if recordString(r, "key") != key {
			kept = append(kept, r)
		}
	}
	s.SetJSONList(KeyRecentVaults, kept)
}

// nowStamp 生成 last_used 时间戳（分钟精度，与 Python strftime 一致）。
func nowStamp() string {
	return clockNow().Format("2006-01-02 15:04")
}

// clockNow 返回当前时间；包级变量便于测试注入固定时钟。
var clockNow = time.Now
