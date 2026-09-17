// 本文件负责分块缓存的落盘元信息（.meta/<hash>.json）与文件命名规则。
//
// 元信息是缓存布局的唯一真相来源：块文件是否完整、未完成块覆盖了哪些
// 区间、条目原名与后端路径的对应关系，全部记录于此。块文件本身只承载
// 字节，不做任何自描述（保持「子文件夹 = 原名、块 = 原名-N」的纯命名）。
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const (
	// metaDirName 是作用域目录下存放元信息的隐藏子目录。
	//
	// 独立成子目录而非与块文件混放，是为了让缓存根目录保持干净 ——
	// 用户打开缓存目录时只看到「原名文件 / 原名文件夹」，符合需求
	// 「避免根目录文件过多、发生混乱」。
	metaDirName = ".meta"
	// partSuffix 是未完成块/未完成单文件的临时后缀。
	//
	// 写入中的文件带该后缀，覆盖区间补齐后去掉后缀改名 —— 用户在
	// 资源管理器里能一眼看出「哪些块还在下载中」。
	partSuffix = ".part"
	// metaVersion 是元信息 schema 版本；结构不兼容变更时递增，
	// 旧版本元信息按「不认识」处理并重建条目。
	metaVersion = 1
	// maxNameRunes 是条目名（原名清洗后）的最大字符数。
	//
	// 预留给「-N」块号后缀和「 (2)」消歧后缀，保证最终路径不逼近
	// Windows 的 255 字符上限。
	maxNameRunes = 120
)

// interval 是半开区间 [Start, End)，用于记录块内已缓存的密文范围。
type interval struct {
	Start int64 `json:"s"`
	End   int64 `json:"e"`
}

// metaChunk 是单个分块的落盘状态。
type metaChunk struct {
	// File 是块文件名（原名 或 原名-N），不含目录与前缀路径。
	File string `json:"file"`
	// Len 是该块应覆盖的密文字节数（末块可能小于分块大小）。
	Len int64 `json:"len"`
	// Done 表示该块已完整（文件已去掉 .part 后缀），此时 Cov 无意义。
	Done bool `json:"done,omitempty"`
	// Cov 是未完成块已覆盖的区间（已合并、按起点升序）。
	Cov []interval `json:"cov,omitempty"`
}

// metaJSON 是一个缓存条目的完整元信息。
type metaJSON struct {
	V       int    `json:"v"`
	Remote  string `json:"remote"`  // 后端密文路径（相对密库根）
	Display string `json:"display"` // 明文展示名（用户看到的原名）
	Entry   string `json:"entry"`   // 实际落盘名（可能与 Display 不同，见消歧）
	// Chunked 为 false 时整个文件是「一个块」，即需求里的「不分块」形态。
	Chunked    bool  `json:"chunked"`
	ChunkSize  int64 `json:"chunkSize"`
	CipherSize int64 `json:"cipherSize"`
	LastAccess int64 `json:"lastAccess"` // Unix 秒，LRU 淘汰依据
	// Chunks 以块索引（十进制字符串）为键；JSON 的 map 键必须是字符串。
	Chunks map[string]*metaChunk `json:"chunks,omitempty"`
}

// metaPath 计算某后端路径的元信息文件路径：.meta/<sha256 前 16 位>.json。
//
// 用哈希而非原名做文件名，规避「原名含非法字符 / 超长 / 同名」三类问题；
// 原名本身记录在元信息的 Entry 字段里，不影响可读性。
func (s *Store) metaPath(remote string) string {
	sum := sha256.Sum256([]byte(remote))
	return filepath.Join(s.dir, metaDirName, hex.EncodeToString(sum[:8])+".json")
}

// loadMeta 读取元信息；不存在 / 损坏 / 版本不符一律返回 false（调用方
// 按「无缓存」处理并重建条目，绝不因为元信息坏了就报错）。
func loadMeta(path string) (*metaJSON, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var m metaJSON
	if err := json.Unmarshal(data, &m); err != nil || m.V != metaVersion {
		return nil, false
	}
	if m.Remote == "" || m.Entry == "" || m.ChunkSize <= 0 || m.CipherSize < 0 {
		return nil, false
	}
	if m.Chunks == nil {
		m.Chunks = map[string]*metaChunk{}
	}
	return &m, true
}

// saveMeta 原子写元信息：先写同目录临时文件再 rename 覆盖。
//
// 元信息在每次填充后被频繁改写，直接就地写会留下截断的 JSON；截断
// 元信息虽然只会被 loadMeta 判为「无缓存」而不致错，但会白白丢掉
// 已下载的块覆盖记录，故仍走原子写。
func saveMeta(path string, m *metaJSON) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	// Windows 上 rename 到已存在的目标会失败，先删目标
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// listMetas 读取作用域目录下全部元信息，返回 remote → 元信息 的映射。
//
// 用于两处：Open 时重建内存索引；新建条目时判断某个落盘名是否已被
// 其它后端路径占用（防同名串扰）。
func (s *Store) listMetas() map[string]*metaJSON {
	out := map[string]*metaJSON{}
	dir := filepath.Join(s.dir, metaDirName)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if m, ok := loadMeta(filepath.Join(dir, e.Name())); ok {
			out[m.Remote] = m
		}
	}
	return out
}

// sanitizeName 把展示名清洗成可用于 Windows 文件名的字符串。
//
// 处理四类问题：非法字符（<>:"/\|?* 与控制字符）→ 下划线；结尾的
// 空格与点（Windows 会静默丢弃）；保留设备名（CON/PRN/AUX/NUL/COM1-9/
// LPT1-9，作为文件名会被系统拒绝）；超长（按字符数截断，避免截坏 UTF-8）。
// 清洗结果为空时返回 "unnamed"，保证任何展示名都能落到一个合法路径。
func sanitizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f:
			return '_'
		case strings.ContainsRune(`<>:"/\|?*`, r):
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(name, " .")

	if len([]rune(name)) > maxNameRunes {
		name = string([]rune(name)[:maxNameRunes])
		name = strings.TrimRight(name, " .")
	}
	if name == "" {
		return "unnamed"
	}
	// 保留设备名（以扩展名前的部分判定，Windows 对 CON.txt 同样拒绝）
	base := name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if isReservedDeviceName(base) {
		return "_" + name
	}
	return name
}

// isReservedDeviceName 报告是否为 Windows 保留设备名（大小写不敏感）。
func isReservedDeviceName(base string) bool {
	up := strings.ToUpper(strings.TrimRight(base, " "))
	switch up {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(up) == 4 {
		switch {
		case strings.HasPrefix(up, "COM"), strings.HasPrefix(up, "LPT"):
			// COM1-9 / LPT1-9
			if up[3] >= '1' && up[3] <= '9' {
				return true
			}
		}
	}
	return false
}
