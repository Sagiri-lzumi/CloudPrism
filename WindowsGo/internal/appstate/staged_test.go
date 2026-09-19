package appstate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsStagedUploadPath 钉住「哪些本地路径属于浏览器上传暂存」的判据。
//
// 判据错一边都会出事：漏判 → 暂存明文永久留在 data/tmp；误判 → 删掉用户的
// 真实文件（下载落盘结果 / 本地上传的原件）。两条都要覆盖。
func TestIsStagedUploadPath(t *testing.T) {
	tmp := t.TempDir()

	stageRoot := filepath.Join(tmp, StagedUploadDirPrefix+"123456")
	cases := []struct {
		name  string
		local string
		want  bool
	}{
		{"暂存根下的一级文件", filepath.Join(stageRoot, "a.txt"), true},
		{"暂存根下的嵌套文件", filepath.Join(stageRoot, "photos", "2026", "a.jpg"), true},
		{"即便目录已不存在也认得出（按路径判据）", filepath.Join(stageRoot, "ghost.bin"), true},

		{"普通用户文件", filepath.Join(tmp, "photo.jpg"), false},
		{"下载落盘目录", filepath.Join(tmp, "downloads", "a.txt"), false},
		{"前缀相似但不是暂存根", filepath.Join(tmp, "cp-uploadX", "a.txt"), false},
		{"空路径", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isStagedUploadPath(c.local); got != c.want {
				t.Fatalf("isStagedUploadPath(%q) = %v, 期望 %v", c.local, got, c.want)
			}
		})
	}
}

// TestReapStagedUpload 钉住回收行为：删文件 + 逐级回收空目录到暂存根，
// 但同批还有别的文件在传时不得提前删掉它们的父目录。
func TestReapStagedUpload(t *testing.T) {
	t.Run("嵌套目录全量回收", func(t *testing.T) {
		tmp := t.TempDir()
		root := filepath.Join(tmp, StagedUploadDirPrefix+"aaa")
		target := filepath.Join(root, "photos", "2026", "a.jpg")
		mustWriteFile(t, target, "x")

		reapStagedUpload(target)

		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("暂存文件应被删除，实际 Stat err=%v", err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("暂存根应被回收，实际 Stat err=%v", err)
		}
	})

	t.Run("同批仍有文件时不删父目录", func(t *testing.T) {
		tmp := t.TempDir()
		root := filepath.Join(tmp, StagedUploadDirPrefix+"bbb")
		done := filepath.Join(root, "photos", "a.jpg")
		other := filepath.Join(root, "photos", "b.jpg")
		mustWriteFile(t, done, "x")
		mustWriteFile(t, other, "y")

		reapStagedUpload(done)

		if _, err := os.Stat(done); !os.IsNotExist(err) {
			t.Fatalf("已完成的暂存文件应被删除")
		}
		if _, err := os.Stat(other); err != nil {
			t.Fatalf("同批其它文件不得被删: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "photos")); err != nil {
			t.Fatalf("仍有文件占用时父目录不得被删: %v", err)
		}

		// 最后一个文件收尾后，目录链应被完整回收
		reapStagedUpload(other)
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("收尾后暂存根应被回收，实际 err=%v", err)
		}
	})

	t.Run("非暂存路径一律不动", func(t *testing.T) {
		tmp := t.TempDir()
		keep := filepath.Join(tmp, "user", "important.txt")
		mustWriteFile(t, keep, "keep me")

		reapStagedUpload(keep)

		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("用户文件被误删: %v", err)
		}
	})
}

// mustWriteFile 建父目录并写文件，测试用。
func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}
