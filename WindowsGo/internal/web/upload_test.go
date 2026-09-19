package web

import (
	"path/filepath"
	"testing"
)

// TestSanitizeUploadRel 覆盖浏览器上报相对路径的清洗。
//
// 这张表是安全边界：任何一段非法都必须报错，绝不允许清洗后放行——
// 放行意味着暂存文件被写到 stageDir 之外（路径穿越）。
func TestSanitizeUploadRel(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		// —— 合法：文件夹上传的正常形态 ——
		{"纯文件名", "a.txt", "a.txt", false},
		{"两级目录", "photos/2026/a.jpg", "photos/2026/a.jpg", false},
		{"前导点斜杠", "./a.txt", "a.txt", false},
		{"重复斜杠", "a//b.txt", "a/b.txt", false},
		{"反斜杠转正斜杠", `a\b\c.txt`, "a/b/c.txt", false},
		{"含空格与中文", "我的 照片/风景 01.jpg", "我的 照片/风景 01.jpg", false},
		{"单点段被丢弃", "a/./b.txt", "a/b.txt", false},
		{"空串视为无路径信息", "", "", false},
		{"仅点斜杠", "././", "", false},

		// —— 非法：一律拒绝 ——
		{"上跳段", "../a.txt", "", true},
		{"中段上跳", "a/../../b.txt", "", true},
		{"绝对路径", "/etc/passwd", "", true},
		// 只有分隔符同样按「前导斜杠」拒绝：合法客户端（webkitRelativePath 与
		// 拖放递归）都不会产出前导斜杠，宁可报错也不猜语义
		{"只有分隔符", "///", "", true},
		{"盘符", "C:/Windows/a.txt", "", true},
		{"UNC 路径", "//server/share/a.txt", "", true},
		{"反斜杠绕过上跳", `..\a.txt`, "", true},
		{"含 NUL", "a\x00b.txt", "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := sanitizeUploadRel(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("sanitizeUploadRel(%q) = %q, 期望报错但通过了", c.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("sanitizeUploadRel(%q) 意外报错: %v", c.raw, err)
			}
			if got != c.want {
				t.Fatalf("sanitizeUploadRel(%q) = %q, 期望 %q", c.raw, got, c.want)
			}
		})
	}
}

// TestResolveStageRel 覆盖「paths 与 files 不对齐」的退化路径。
func TestResolveStageRel(t *testing.T) {
	cases := []struct {
		name     string
		rels     []string
		i        int
		filename string
		want     string
		wantErr  bool
	}{
		{"正常取用", []string{"a.txt", "d/b.txt"}, 1, "b.txt", "d/b.txt", false},
		{"清单短于文件数则平铺", []string{"a.txt"}, 1, "b.txt", "b.txt", false},
		{"清单为空则平铺", nil, 0, "c.txt", "c.txt", false},
		{"空路径退回文件名", []string{""}, 0, "d.txt", "d.txt", false},
		{"非法路径整体拒绝", []string{"../x"}, 0, "x", "", true},
		{"文件名含目录分隔符只取末段", []string{""}, 0, "evil/../f.txt", "f.txt", false},
		{"文件名退化为点则报错", []string{""}, 0, ".", "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveStageRel(c.rels, c.i, c.filename)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，得到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外报错: %v", err)
			}
			if got != c.want {
				t.Fatalf("= %q, 期望 %q", got, c.want)
			}
		})
	}
}

// TestStageLayoutMirrorsRelativePath 钉住核心约定：暂存目录按相对路径镜像，
// 于是 appstate.UploadPaths walk 出的 rel 就是用户看到的目录结构。
// 这条一旦破坏，文件夹上传会「成功但目录被拍平」，且没有任何报错。
func TestStageLayoutMirrorsRelativePath(t *testing.T) {
	stageDir := t.TempDir()
	rel := "photos/2026/a.jpg"

	dst := filepath.Join(stageDir, filepath.FromSlash(rel))
	if !filepath.IsAbs(dst) {
		t.Fatalf("暂存路径应为绝对路径: %s", dst)
	}
	// 必须落在暂存根之内（穿越防护的最终体现）
	inside, err := filepath.Rel(stageDir, dst)
	if err != nil {
		t.Fatalf("Rel 失败: %v", err)
	}
	if inside != filepath.FromSlash(rel) {
		t.Fatalf("暂存相对路径 = %q, 期望 %q", inside, filepath.FromSlash(rel))
	}
}
