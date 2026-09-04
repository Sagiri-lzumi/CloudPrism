package settings

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// 旧版设置（QSettings IniFormat 的 data/cloudprism.ini）的一次性导入。
//
// Python 端 SettingsStore 用 QSettings 写 INI，其格式与通用 INI 有出入，
// 此处按实测样本（PySide6 QSettings 实际产出的字节）实现最小解析器：
//
//   - 行结构：`[组名]` 与 `键=值`，CRLF 行尾、组间空行；
//   - 键路径 `a/b` 表现为组 `[a]` + 键 `b`（不再嵌套）；
//   - **组名/键名**中的非 ASCII 与特殊字符按 `%UXXXX` 编码
//     （实测 "中文键" → `%U4E2D%U6587%U952E`），值**不**做该编码、
//     中文等非 ASCII 直接以 UTF-8 原样写入；
//   - 字符串值在**需要转义时**才加双引号并转义内部 `\`/`"`/`\n` 等
//     （实测含引号花括号的 JSON 值带引号，普通 URL 不带）；
//   - 行首 `;`/`#` 为注释，空行忽略。
//
// 真实键集（appearance/cache/transfer/sync/perf/security/conn/vaults 共
// 15 个键）全部是 ASCII 组键名且值为普通文本或带引号 JSON，因此上述规则
// 覆盖 Python 端会写出的全部形态；`%UXXXX` 解码与转义处理是防御性的，
// 覆盖用户手改文件的常见情况。超纲形态（UTF-16 代理对等）按边界外处理。

// ImportLegacy 把旧版 QSettings INI 一次性导入到 JSON 设置存储。
//
// 幂等语义（新格式优先）：
//  1. jsonPath 已存在 → 不导入（新设置已在写，旧 INI 视为已处理）；
//  2. iniPath 不存在 → 无事可做；
//  3. 解析 INI → 写入 JSON → 把 INI 改名追加 ".imported" 作已处理标记
//     （改名失败无害：下次因 jsonPath 已存在仍会跳过）。
//
// 返回导入的键数；导入/写盘失败返回 error（含解析到无法容忍的内容）。
func ImportLegacy(iniPath, jsonPath string) (int, error) {
	if _, err := os.Stat(jsonPath); err == nil {
		return 0, nil // 新格式已存在：跳过导入
	}
	if _, err := os.Stat(iniPath); err != nil {
		return 0, nil // 无遗留 INI（或不可读，按无事可做处理）
	}

	f, err := os.Open(iniPath)
	if err != nil {
		return 0, fmt.Errorf("settings: 打开旧版 INI 失败: %w", err)
	}

	kv, err := parseQSettingsINI(f)
	// 解析完成后立刻关句柄再改名：Windows 上对仍打开的文件做 rename 会
	// 被拒绝（共享冲突），导致“一次性导入”标记写不出去、下次重复导入。
	closeErr := f.Close()
	if err != nil {
		return 0, fmt.Errorf("settings: 解析旧版 INI 失败: %w", err)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("settings: 关闭旧版 INI 失败: %w", closeErr)
	}

	st := &Store{path: jsonPath, values: kv}
	if err := st.Sync(); err != nil {
		return 0, fmt.Errorf("settings: 写入 JSON 设置失败: %w", err)
	}
	_ = os.Rename(iniPath, iniPath+".imported")
	return len(kv), nil
}

// parseQSettingsINI 解析 QSettings(IniFormat) 内容为 组/键 → 值 映射。
// 解析宽松：结构异常的行跳过（导入不该因单行坏数据全盘失败）。
func parseQSettingsINI(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	section := "" // 无 [组] 前缀行落在根键（真实文件不出现，防御处理）

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				continue // 结构异常组行：跳过
			}
			section = decodeQSettingsKey(line[1 : len(line)-1])
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue // 无等号的噪声行
		}
		key := decodeQSettingsKey(strings.TrimSpace(line[:eq]))
		val := decodeQSettingsValue(strings.TrimSpace(line[eq+1:]))
		if section != "" {
			key = section + "/" + key
		}
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// decodeQSettingsKey 还原组名/键名中的 %UXXXX 编码（仅 BMP 码点）。
// 实测样本："中文键" → "%U4E2D%U6587%U952E"。非 BMP（代理对）场景在
// 真实键集中不存在，遇到无法解析的形态按字面保留，不阻断整体解析。
func decodeQSettingsKey(s string) string {
	if !strings.Contains(s, "%U") {
		return s // 快速路径：绝大多数真实键名无编码
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		// %UXXXX：恰好还有 5 个字符（U + 4 hex）时才尝试解析
		if i+6 <= len(s) && s[i] == '%' && s[i+1] == 'U' {
			if v, err := strconv.ParseUint(s[i+2:i+6], 16, 16); err == nil {
				b.WriteRune(rune(v))
				i += 6
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// decodeQSettingsValue 还原 QSettings 值：
//
//   - 首尾双引号包裹 → 字符串值：剥引号后反转义 `\\` `\"` `\n` `\r` `\t`
//     （实测 JSON 字符串值形如 "[{\"local\": \"a\\\\b\"...]"）；
//   - 无引号 → 标量值（int/bool/普通文本），QSettings 只在需要转义时才
//     加引号，故无引号值不需要反转义。
func decodeQSettingsValue(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	} else {
		return v
	}
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '\\' || i+1 >= len(v) {
			b.WriteByte(c)
			continue
		}
		i++
		switch v[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		default:
			// 未知转义按字面保留（QSettings 只转义上述五种）
			b.WriteByte('\\')
			b.WriteByte(v[i])
		}
	}
	return b.String()
}
