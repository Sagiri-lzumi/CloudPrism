package storage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// newTestLocal 建一个临时根目录并返回本地后端。
//
// 注意断言绝对路径时必须用 b.Root() 而不是 t.TempDir() 的返回值：NewLocal
// 会对根做 EvalSymlinks，两者在有符号链接的机器上（如 macOS 的 /var →
// /private/var）并不相等，直接用 tmpdir 拼出来的期望路径会假失败。
func newTestLocal(t *testing.T) (*Local, string) {
	t.Helper()
	root := t.TempDir()
	b, err := NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal(%q) 失败: %v", root, err)
	}
	return b, b.Root()
}

// writeRoot 在根目录下写入一个文件，自动创建父目录。
func writeRoot(t *testing.T, root, rel string, data []byte) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("创建父目录失败: %v", err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", rel, err)
	}
	return full
}

// ---------------------------------------------------------------------------
// 构造
// ---------------------------------------------------------------------------

// TestNewLocalRejectsBadRoot 覆盖三种非法根，并确认**绝不自动创建**。
//
// 「不自动创建」是刻意保留的 Python 语义（local_backend.py:28-31）：根目录是
// 用户在向导里明确选定的，静默 mkdir 会把「盘符选错 / 路径打错」这类失误掩盖成
// 「连接成功但密库是空的」，用户接着上传一批文件，等发现时数据已经散落在一个
// 自己都不知道存在的目录里。
func TestNewLocalRejectsBadRoot(t *testing.T) {
	base := t.TempDir()

	t.Run("根不存在", func(t *testing.T) {
		missing := filepath.Join(base, "no-such-dir")
		_, err := NewLocal(missing)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("期望 ErrNotFound，实得 %v", err)
		}
		// 关键：不能顺手把目录建出来
		if _, statErr := os.Lstat(missing); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("NewLocal 失败后仍创建了目录 %s", missing)
		}
	})

	t.Run("根是文件", func(t *testing.T) {
		f := writeRoot(t, base, "afile.bin", []byte("x"))
		_, err := NewLocal(f)
		if !errors.Is(err, ErrNotDir) {
			t.Fatalf("期望 ErrNotDir，实得 %v", err)
		}
	})

	// 空根在 Python 端会被 Path("") 当作当前目录（等价于「静默把密库建在
	// 进程 cwd」），Go 端显式拒绝。设置项未填写时就该报「未配置」，
	// 而不是悄悄落到一个随启动方式变化的目录里。
	t.Run("根为空串", func(t *testing.T) {
		for _, empty := range []string{"", "   "} {
			if _, err := NewLocal(empty); !errors.Is(err, ErrBackend) {
				t.Errorf("NewLocal(%q) 期望 ErrBackend，实得 %v", empty, err)
			}
		}
	})

	t.Run("合法根被规范化为绝对路径", func(t *testing.T) {
		b, err := NewLocal(base)
		if err != nil {
			t.Fatalf("NewLocal 失败: %v", err)
		}
		if !filepath.IsAbs(b.Root()) {
			t.Errorf("Root() = %q，应为绝对路径", b.Root())
		}
	})
}

// ---------------------------------------------------------------------------
// 路径逃逸 —— 本文件最有价值的一组断言
// ---------------------------------------------------------------------------

// TestLocalPathEscape 确认越界路径一律被拒。
//
// 为什么这条在 Go 端比 Python 端重要：两端的路径拼接语义不同。
//
//	pathlib：Path("C:/root") / "D:/evil" → D:/evil（右侧绝对路径覆盖左侧），
//	         随后 relative_to(root) 失败 → 抛 ValueError，**天然免疫**；
//	Go：     filepath.Join("C:\\root", "D:\\evil") → C:\root\D:\evil，
//	         随后 filepath.Rel 成功 → **不报错，静默逃逸**。
//
// 也就是说照抄 Python 的「Join 后再 Rel 校验」在 Go 里是漏的，必须显式拒绝
// 绝对路径。威胁模型是真实的：目录条目名来自云端目录树，攻击者只要能在
// WebDAV/百度侧塞进一个名为 "..\\..\\Windows" 的条目，同步下来后就可能让
// Delete/Rename 落到用户任意文件上。
func TestLocalPathEscape(t *testing.T) {
	b, root := newTestLocal(t)

	escapes := []struct {
		name string
		path string
	}{
		{"上溯一级", ".."},
		{"上溯带斜杠", "../"},
		{"上溯到系统目录", "../../etc/passwd"},
		{"中间上溯越界", "a/../../b"},
		{"深层中间上溯", "a/b/../../../x"},
		{"UNC 共享", `\\server\share\x`},
	}
	// 盘符绝对路径仅在 Windows 上是逃逸；其它平台 "D:" 只是普通目录名，
	// Join 之后确实还在 root 内，拒绝它反而会误伤合法路径。
	if os.PathSeparator == '\\' {
		escapes = append(escapes,
			struct{ name, path string }{"同盘绝对路径", root},
			struct{ name, path string }{"跨盘绝对路径", "D:/evil/payload"},
			struct{ name, path string }{"反斜杠绝对路径", `C:\Windows\System32`},
		)
	}

	for _, tc := range escapes {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := b.resolve(tc.path); !errors.Is(err, ErrPathEscape) {
				t.Errorf("resolve(%q) 期望 ErrPathEscape，实得 %v", tc.path, err)
			}
			// 每个公开方法都必须经过同一道校验，抽查两个破坏力最大的
			if err := b.Delete(context.Background(), tc.path); !errors.Is(err, ErrPathEscape) {
				t.Errorf("Delete(%q) 期望 ErrPathEscape，实得 %v", tc.path, err)
			}
			if _, err := b.ListDir(context.Background(), tc.path); !errors.Is(err, ErrPathEscape) {
				t.Errorf("ListDir(%q) 期望 ErrPathEscape，实得 %v", tc.path, err)
			}
		})
	}

	// 反向：这些路径**必须**被接受，否则防护就成了拒绝服务。
	// 首尾斜杠按约定可省略（backend.go 的路径约定），"/etc/passwd" 去掉
	// 前导斜杠后是 root 内的合法相对路径 —— 与 Python 的 path.strip("/") 一致。
	t.Run("合法相对路径不被误杀", func(t *testing.T) {
		for _, ok := range []string{
			"", "/", "//", "a", "a/b", "/a/b/", "./a", "a/./b", "a/b/../c",
			"..data", "a..b", "....//x",
		} {
			p, err := b.resolve(ok)
			if err != nil {
				t.Errorf("resolve(%q) 不该被拒，实得 %v", ok, err)
				continue
			}
			// 解析结果必须仍在 root 之内（".." 开头才算逃逸）
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
				t.Errorf("resolve(%q) = %q 逃出了根目录", ok, p)
			}
		}
	})

	// "..data" / "a..b" 这类名字里含 ".." 但不构成上溯，是密库里完全可能出现的
	// 合法文件名（Base32 密文不含点，但展示名与用户自建的目录名会含）。
	// 前缀判断写成 HasPrefix(p, ".."+sep) 而不是 Contains(p, "..") 就是为了它们。
	t.Run("含点号的名字不被误判", func(t *testing.T) {
		p, err := b.resolve("..data/x")
		if err != nil {
			t.Fatalf(`resolve("..data/x") 不该被拒，实得 %v`, err)
		}
		if filepath.Base(filepath.Dir(p)) != "..data" {
			t.Errorf("解析结果 %q 的父目录名不是 ..data", p)
		}
	})
}

// ---------------------------------------------------------------------------
// 目录列举
// ---------------------------------------------------------------------------

func TestLocalListDir(t *testing.T) {
	b, root := newTestLocal(t)
	ctx := context.Background()

	t.Run("空目录", func(t *testing.T) {
		entries, err := b.ListDir(ctx, "/")
		if err != nil {
			t.Fatalf("ListDir 失败: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("空目录应返回 0 条，实得 %d", len(entries))
		}
	})

	writeRoot(t, root, "a.cpenc", []byte("hello"))
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("建子目录失败: %v", err)
	}

	t.Run("文件与子目录", func(t *testing.T) {
		entries, err := b.ListDir(ctx, "/")
		if err != nil {
			t.Fatalf("ListDir 失败: %v", err)
		}
		byName := map[string]Entry{}
		for _, e := range entries {
			byName[e.Name] = e
		}
		if len(byName) != 2 {
			t.Fatalf("期望 2 条，实得 %d：%v", len(byName), entries)
		}

		a := byName["a.cpenc"]
		if a.IsDir {
			t.Error("a.cpenc 不该被标为目录")
		}
		// size 必须是真实字节数：上层用它算明文总量（cipherSize - headerLen），
		// 报 0 会让流式代理直接返回空体，症状是「播放器显示时长 0:00」
		if a.Size != 5 {
			t.Errorf("a.cpenc 的 size = %d，应为 5", a.Size)
		}
		if !byName["sub"].IsDir {
			t.Error("sub 应被标为目录")
		}
	})

	t.Run("条目名是叶子名而非全路径", func(t *testing.T) {
		writeRoot(t, root, "sub/deep.bin", []byte("xyz"))
		entries, err := b.ListDir(ctx, "sub")
		if err != nil {
			t.Fatalf("ListDir(sub) 失败: %v", err)
		}
		if len(entries) != 1 || entries[0].Name != "deep.bin" {
			t.Errorf("期望单条 deep.bin，实得 %v", entries)
		}
	})

	t.Run("路径不存在", func(t *testing.T) {
		_, err := b.ListDir(ctx, "nope")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("期望 ErrNotFound，实得 %v", err)
		}
	})

	t.Run("路径是文件", func(t *testing.T) {
		_, err := b.ListDir(ctx, "a.cpenc")
		if !errors.Is(err, ErrNotDir) {
			t.Errorf("期望 ErrNotDir，实得 %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// 大小与存在性
// ---------------------------------------------------------------------------

func TestLocalGetSizeAndExists(t *testing.T) {
	b, root := newTestLocal(t)
	ctx := context.Background()

	writeRoot(t, root, "f.bin", bytes.Repeat([]byte{0}, 123))
	if err := os.Mkdir(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}

	if n, err := b.GetSize(ctx, "f.bin"); err != nil || n != 123 {
		t.Errorf("GetSize = (%d, %v)，应为 (123, nil)", n, err)
	}
	// Head 与 GetSize 同义，是断点续传的基准；两者不一致会让续传从错误偏移
	// 开始写，产物是「文件大小对但中间一段是旧数据」，解密后表现为随机乱码
	if n, err := b.Head(ctx, "f.bin"); err != nil || n != 123 {
		t.Errorf("Head = (%d, %v)，应为 (123, nil)", n, err)
	}

	if _, err := b.GetSize(ctx, "nope.bin"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSize 不存在 期望 ErrNotFound，实得 %v", err)
	}
	// GetSize 对目录报 ErrIsDir（对照 local_backend.py:79 的 IsADirectoryError）
	if _, err := b.GetSize(ctx, "d"); !errors.Is(err, ErrIsDir) {
		t.Errorf("GetSize 目录 期望 ErrIsDir，实得 %v", err)
	}

	for _, tc := range []struct {
		path string
		want bool
	}{{"f.bin", true}, {"d", true}, {"nope", false}, {"d/f.bin", false}} {
		got, err := b.Exists(ctx, tc.path)
		if err != nil {
			t.Errorf("Exists(%q) 报错: %v", tc.path, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Exists(%q) = %v，应为 %v", tc.path, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 字节范围读取
// ---------------------------------------------------------------------------

func TestLocalDownloadRange(t *testing.T) {
	b, root := newTestLocal(t)
	ctx := context.Background()

	full := make([]byte, 256)
	for i := range full {
		full[i] = byte(i)
	}
	writeRoot(t, root, "f.bin", full)
	writeRoot(t, root, "abcde.bin", []byte("ABCDE"))
	if err := os.Mkdir(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}

	tests := []struct {
		name       string
		path       string
		start, end int64
		want       []byte
	}{
		{"整段", "f.bin", 0, 255, full},
		{"部分", "f.bin", 10, 20, full[10:21]},
		{"单字节", "abcde.bin", 2, 2, []byte("C")},
		// end 越界必须收敛到 size-1 而不是报错：调用方（流式代理）按 2MiB
		// 步长推进，最后一片的 end 天然会超出文件末尾
		{"end 超出末尾", "abcde.bin", 3, 100, []byte("DE")},
		// start == size 返回空切片且**不报错**：上层据此判定「这一段没有数据」，
		// 若改成 ErrRange，代理会把正常的 EOF 探测回成 416
		{"start 等于大小", "abcde.bin", 5, 10, []byte{}},
		{"start 远超大小", "abcde.bin", 1000, 2000, []byte{}},
		{"空文件的 [0,0]", "empty.bin", 0, 0, []byte{}},
	}
	writeRoot(t, root, "empty.bin", nil)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := b.DownloadRange(ctx, tc.path, tc.start, tc.end)
			if err != nil {
				t.Fatalf("DownloadRange(%q, %d, %d) 失败: %v", tc.path, tc.start, tc.end, err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("DownloadRange(%q, %d, %d) = %v，应为 %v", tc.path, tc.start, tc.end, got, tc.want)
			}
		})
	}

	t.Run("非法范围", func(t *testing.T) {
		for _, r := range [][2]int64{{-1, 3}, {5, 3}, {-1, -1}} {
			if _, err := b.DownloadRange(ctx, "f.bin", r[0], r[1]); !errors.Is(err, ErrRange) {
				t.Errorf("DownloadRange(%d, %d) 期望 ErrRange，实得 %v", r[0], r[1], err)
			}
		}
	})

	t.Run("文件不存在", func(t *testing.T) {
		if _, err := b.DownloadRange(ctx, "nope.bin", 0, 10); !errors.Is(err, ErrNotFound) {
			t.Errorf("期望 ErrNotFound，实得 %v", err)
		}
	})

	// Python 的 download_range 只判 is_file()，因此对目录抛的是
	// FileNotFoundError 而不是 IsADirectoryError（与 get_size 不同）。
	// 这里逐字保留该差异：两端在「对目录取范围」时的错误分类必须一致，
	// 否则同一份密库在两个客户端上会一个报 404、一个报 500。
	t.Run("目录按不存在处理", func(t *testing.T) {
		_, err := b.DownloadRange(ctx, "d", 0, 10)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("期望 ErrNotFound，实得 %v", err)
		}
		if errors.Is(err, ErrIsDir) {
			t.Error("不该报 ErrIsDir，需与 Python 的 FileNotFoundError 对齐")
		}
	})

	t.Run("取消的 ctx 立即返回", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := b.DownloadRange(cctx, "f.bin", 0, 10); !errors.Is(err, context.Canceled) {
			t.Errorf("期望 context.Canceled，实得 %v", err)
		}
	})
}

// TestLocalDownloadRangeInto 验证零拷贝路径与 DownloadRange 结果一致。
//
// 这条路径是流式代理的热路径（每 256KiB 一次），若与 DownloadRange 语义分叉，
// 症状会是「本地后端播放正常、切到 WebDAV 就花屏」这类极难归因的差异。
func TestLocalDownloadRangeInto(t *testing.T) {
	b, root := newTestLocal(t)
	ctx := context.Background()

	full := make([]byte, 4096)
	for i := range full {
		full[i] = byte(i * 7)
	}
	writeRoot(t, root, "f.bin", full)

	t.Run("与 DownloadRange 逐字节一致", func(t *testing.T) {
		for _, r := range [][2]int64{{0, 4095}, {0, 0}, {100, 199}, {1000, 99999}, {4096, 5000}} {
			want, err := b.DownloadRange(ctx, "f.bin", r[0], r[1])
			if err != nil {
				t.Fatalf("DownloadRange(%d,%d) 失败: %v", r[0], r[1], err)
			}
			// 哨兵填充：若实现少写了字节，残留的 0xEE 会立刻暴露出来
			dst := bytes.Repeat([]byte{0xEE}, 4096)
			n, err := b.DownloadRangeInto(ctx, "f.bin", r[0], r[1], dst)
			if err != nil {
				t.Fatalf("DownloadRangeInto(%d,%d) 失败: %v", r[0], r[1], err)
			}
			if n != len(want) {
				t.Errorf("[%d,%d] n = %d，应为 %d", r[0], r[1], n, len(want))
			}
			if !bytes.Equal(dst[:n], want) {
				t.Errorf("[%d,%d] 内容与 DownloadRange 不一致", r[0], r[1])
			}
		}
	})

	t.Run("dst 容量不足时截断而不报错", func(t *testing.T) {
		dst := make([]byte, 10)
		n, err := b.DownloadRangeInto(ctx, "f.bin", 0, 100, dst)
		if err != nil {
			t.Fatalf("失败: %v", err)
		}
		if n != 10 {
			t.Errorf("n = %d，应为 dst 容量 10", n)
		}
		if !bytes.Equal(dst, full[:10]) {
			t.Error("截断读取的内容不正确")
		}
	})

	t.Run("非法范围与不存在", func(t *testing.T) {
		dst := make([]byte, 16)
		if _, err := b.DownloadRangeInto(ctx, "f.bin", -1, 3, dst); !errors.Is(err, ErrRange) {
			t.Errorf("期望 ErrRange，实得 %v", err)
		}
		if _, err := b.DownloadRangeInto(ctx, "nope", 0, 3, dst); !errors.Is(err, ErrNotFound) {
			t.Errorf("期望 ErrNotFound，实得 %v", err)
		}
	})

	// ReadAt 不读写文件偏移量，因此并发分片读是安全的。当前实现每次调用各自
	// open 一个句柄，本测试看似多余；但它是一道**重构护栏**：若将来为了省
	// syscall 而改成缓存句柄 + Seek，竞态会在这里以数据错乱的形式暴露。
	// 用内容比对而非 -race，因为竞态检测器在 Windows 上需要 cgo，而本项目
	// 发布形态是 CGO_ENABLED=0、开发机也没装 gcc。
	t.Run("并发分片读不串数据", func(t *testing.T) {
		const (
			workers = 8
			rounds  = 50
			seg     = 512
		)
		var wg sync.WaitGroup
		for w := range workers {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				start := int64(id * seg)
				want := full[start : start+seg]
				for range rounds {
					dst := make([]byte, seg)
					n, err := b.DownloadRangeInto(ctx, "f.bin", start, start+seg-1, dst)
					if err != nil || n != seg {
						t.Errorf("worker %d: n=%d err=%v", id, n, err)
						return
					}
					if !bytes.Equal(dst, want) {
						t.Errorf("worker %d 读到了别的分片的数据", id)
						return
					}
				}
			}(w)
		}
		wg.Wait()
	})
}

// TestReadRangeIntoFallsBack 确认统一入口对未实现优化接口的后端能正确回退。
//
// 回退逻辑收在 ReadRangeInto 里而不是散落到调用点，是为了保证「优化接口缺席」
// 永远不会变成调用方需要处理的分叉。百度后端目前不实现 RangeReaderInto，
// 全靠这条路径。
func TestReadRangeIntoFallsBack(t *testing.T) {
	b, root := newTestLocal(t)
	ctx := context.Background()
	data := []byte("0123456789")
	writeRoot(t, root, "f.bin", data)

	// noInto 只暴露 Backend，刻意隐藏 RangeReaderInto
	wrapped := struct{ Backend }{b}
	if _, ok := any(wrapped).(RangeReaderInto); ok {
		t.Fatal("测试前提不成立：包装类型不该实现 RangeReaderInto")
	}

	dst := make([]byte, 4)
	n, err := ReadRangeInto(ctx, wrapped, "f.bin", 2, 9, dst)
	if err != nil {
		t.Fatalf("回退路径失败: %v", err)
	}
	if n != 4 || !bytes.Equal(dst, []byte("2345")) {
		t.Errorf("回退读取 = (%d, %q)，应为 (4, \"2345\")", n, dst)
	}

	// 直接传 *Local 时走零拷贝路径，结果必须相同
	dst2 := make([]byte, 4)
	n2, err := ReadRangeInto(ctx, b, "f.bin", 2, 9, dst2)
	if err != nil || n2 != n || !bytes.Equal(dst2, dst) {
		t.Errorf("两条路径结果不一致：(%d,%v) vs (%d,%v)", n2, dst2, n, dst)
	}
}

// ---------------------------------------------------------------------------
// 上传
// ---------------------------------------------------------------------------

// collectProgress 跑一次上传并回收进度序列。
func collectProgress(t *testing.T, b *Local, src *os.File, remote string, chunk int) []float64 {
	t.Helper()
	var seq []float64
	err := b.UploadChunked(context.Background(), src.Name(), remote, chunk, func(p float64) {
		seq = append(seq, p)
	})
	if err != nil {
		t.Fatalf("UploadChunked 失败: %v", err)
	}
	return seq
}

func TestLocalUploadChunked(t *testing.T) {
	b, root := newTestLocal(t)
	tmp := t.TempDir()

	t.Run("完整上传", func(t *testing.T) {
		src := writeRoot(t, tmp, "src1.bin", bytes.Repeat([]byte{0xAB}, 1024))
		var seq []float64
		err := b.UploadChunked(context.Background(), src, "dst1.bin", 256, func(p float64) {
			seq = append(seq, p)
		})
		if err != nil {
			t.Fatalf("上传失败: %v", err)
		}

		got, _ := os.ReadFile(filepath.Join(root, "dst1.bin"))
		if len(got) != 1024 || !bytes.Equal(got, bytes.Repeat([]byte{0xAB}, 1024)) {
			t.Errorf("落盘内容不正确（len=%d）", len(got))
		}
		// 进度必须单调非减且末值为 1.0：传输队列按它算聚合进度，
		// 一次回退会让总进度条来回跳
		if len(seq) == 0 || seq[len(seq)-1] != 1.0 {
			t.Errorf("进度末值应为 1.0，实得 %v", seq)
		}
		for i := 1; i < len(seq); i++ {
			if seq[i] < seq[i-1] {
				t.Errorf("进度回退：%v", seq)
				break
			}
		}
		for _, p := range seq {
			if p < 0 || p > 1 {
				t.Errorf("进度越界 %f：%v", p, seq)
				break
			}
		}
		// 1024 字节 / 256 分块 = 恰好 4 次回调
		if len(seq) != 4 {
			t.Errorf("期望 4 次进度回调，实得 %d 次：%v", len(seq), seq)
		}
	})

	t.Run("空文件也要回调 1.0", func(t *testing.T) {
		src := writeRoot(t, tmp, "empty.bin", nil)
		var seq []float64
		if err := b.UploadChunked(context.Background(), src, "empty.bin", 0, func(p float64) {
			seq = append(seq, p)
		}); err != nil {
			t.Fatalf("上传失败: %v", err)
		}
		if len(seq) != 1 || seq[0] != 1.0 {
			t.Errorf("空文件应回调一次 1.0，实得 %v", seq)
		}
		if fi, err := os.Stat(filepath.Join(root, "empty.bin")); err != nil || fi.Size() != 0 {
			t.Errorf("空文件未正确落盘: %v", err)
		}
	})

	// chunk<=0 取 DefaultChunk。若这条默认值失效（例如变成 0），
	// make([]byte, 0) 会让循环一次都读不到数据，产物是 0 字节文件
	// 而进度却报 1.0 —— 静默的数据丢失。
	t.Run("chunk 非正数取默认值", func(t *testing.T) {
		src := writeRoot(t, tmp, "src2.bin", bytes.Repeat([]byte{1}, 3000))
		for _, chunk := range []int{0, -1} {
			remote := "dst2.bin"
			os.Remove(filepath.Join(root, remote))
			if err := b.UploadChunked(context.Background(), src, remote, chunk, nil); err != nil {
				t.Fatalf("chunk=%d 上传失败: %v", chunk, err)
			}
			if fi, err := os.Stat(filepath.Join(root, remote)); err != nil || fi.Size() != 3000 {
				t.Errorf("chunk=%d 落盘大小 = %v, %v，应为 3000", chunk, fi, err)
			}
		}
	})

	t.Run("onProgress 为 nil 不 panic", func(t *testing.T) {
		src := writeRoot(t, tmp, "src3.bin", []byte("data"))
		if err := b.UploadChunked(context.Background(), src, "dst3.bin", 2, nil); err != nil {
			t.Fatalf("上传失败: %v", err)
		}
	})

	t.Run("自动创建多级父目录", func(t *testing.T) {
		src := writeRoot(t, tmp, "src4.bin", []byte("nested"))
		if err := b.UploadChunked(context.Background(), src, "a/b/c/deep.bin", 4, nil); err != nil {
			t.Fatalf("上传失败: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(root, "a", "b", "c", "deep.bin"))
		if err != nil || string(got) != "nested" {
			t.Errorf("嵌套路径写入失败: %q, %v", got, err)
		}
	})

	t.Run("源文件不存在", func(t *testing.T) {
		err := b.UploadChunked(context.Background(), filepath.Join(tmp, "nope.bin"), "x.bin", 64, nil)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("期望 ErrNotFound，实得 %v", err)
		}
	})

	t.Run("源是目录", func(t *testing.T) {
		err := b.UploadChunked(context.Background(), tmp, "x.bin", 64, nil)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("期望 ErrNotFound，实得 %v", err)
		}
	})
}

// TestLocalUploadResume 覆盖断点续传的三种情形。
func TestLocalUploadResume(t *testing.T) {
	b, root := newTestLocal(t)
	tmp := t.TempDir()

	full := make([]byte, 256)
	for i := range full {
		full[i] = byte(i)
	}

	t.Run("从已有偏移续传", func(t *testing.T) {
		src := writeRoot(t, tmp, "src.bin", full)
		writeRoot(t, root, "dst.bin", full[:100]) // 预置前 100 字节

		var seq []float64
		if err := b.UploadChunked(context.Background(), src, "dst.bin", 64, func(p float64) {
			seq = append(seq, p)
		}); err != nil {
			t.Fatalf("续传失败: %v", err)
		}

		got, _ := os.ReadFile(filepath.Join(root, "dst.bin"))
		if !bytes.Equal(got, full) {
			t.Errorf("续传结果与原文不一致（len=%d）", len(got))
		}
		// 首次回调就应带上续传命中的 100 字节，与 Python 的
		// (offset+written)/total 一致；若从 0 开始报，进度条会先倒退再前进
		if len(seq) == 0 || seq[0] <= 0 {
			t.Errorf("首次进度应 > 0（含续传命中部分），实得 %v", seq)
		}
		if seq[len(seq)-1] != 1.0 {
			t.Errorf("末值应为 1.0，实得 %v", seq)
		}
	})

	// 这是对 Python 缺陷的**修正点**：Python 在「目标已与源等长」时循环体一次
	// 都不进，一个进度都不 yield，上层进度条永远停在 99%、任务永远不结束。
	// Go 端保证末值恒为 1.0。
	t.Run("目标已完整时仍回调 1.0", func(t *testing.T) {
		src := writeRoot(t, tmp, "src2.bin", full)
		writeRoot(t, root, "dst2.bin", full) // 已完整

		var seq []float64
		if err := b.UploadChunked(context.Background(), src, "dst2.bin", 64, func(p float64) {
			seq = append(seq, p)
		}); err != nil {
			t.Fatalf("失败: %v", err)
		}
		if len(seq) != 1 || seq[0] != 1.0 {
			t.Errorf("应恰好回调一次 1.0，实得 %v", seq)
		}
		got, _ := os.ReadFile(filepath.Join(root, "dst2.bin"))
		if !bytes.Equal(got, full) {
			t.Error("已完整的目标被改动了")
		}
	})

	// 目标比源大属异常状态（上次传了别的文件、或云端被手工改过），
	// 必须从头重传而不是接着写 —— 接着写会产出一个前缀是旧数据的坏文件。
	t.Run("目标比源大则重传", func(t *testing.T) {
		short := []byte("short")
		src := writeRoot(t, tmp, "src3.bin", short)
		writeRoot(t, root, "dst3.bin", bytes.Repeat([]byte{0xFF}, 500))

		if err := b.UploadChunked(context.Background(), src, "dst3.bin", 16, nil); err != nil {
			t.Fatalf("失败: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(root, "dst3.bin"))
		if !bytes.Equal(got, short) {
			t.Errorf("重传后应为 %q，实得 %d 字节（残留旧数据）", short, len(got))
		}
	})
}

// TestLocalUploadCancelledBeforeStart 确认「启动前已取消」不会破坏既有数据。
//
// 少了入口处的 ctx 检查，offset==0 分支会以 O_TRUNC 打开目标：用户点
// 「取消全部」时，那些还没开始跑的任务反而会把上次留下的半成品清零。
func TestLocalUploadCancelledBeforeStart(t *testing.T) {
	b, root := newTestLocal(t)
	tmp := t.TempDir()

	src := writeRoot(t, tmp, "src.bin", bytes.Repeat([]byte{7}, 100))
	cctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("目标未被创建", func(t *testing.T) {
		err := b.UploadChunked(cctx, src, "fresh.bin", 16, nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("期望 context.Canceled，实得 %v", err)
		}
		if _, statErr := os.Lstat(filepath.Join(root, "fresh.bin")); !errors.Is(statErr, os.ErrNotExist) {
			t.Error("已取消的任务不该创建目标文件")
		}
	})

	// 目标比源大 → offset 归零 → 会走 O_TRUNC 分支。这正是最危险的路径。
	t.Run("既有目标未被清零", func(t *testing.T) {
		stale := bytes.Repeat([]byte{0xFF}, 500)
		writeRoot(t, root, "stale.bin", stale)

		err := b.UploadChunked(cctx, src, "stale.bin", 16, nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("期望 context.Canceled，实得 %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(root, "stale.bin"))
		if !bytes.Equal(got, stale) {
			t.Errorf("已取消的任务改动了目标（len=%d，应为 %d）", len(got), len(stale))
		}
	})

	t.Run("取消后不回调进度", func(t *testing.T) {
		called := false
		_ = b.UploadChunked(cctx, src, "x.bin", 16, func(float64) { called = true })
		if called {
			t.Error("已取消的任务不该回调进度")
		}
	})
}

// ---------------------------------------------------------------------------
// 目录操作
// ---------------------------------------------------------------------------

func TestLocalMkdirRenameDelete(t *testing.T) {
	b, root := newTestLocal(t)
	ctx := context.Background()

	t.Run("建多级目录且幂等", func(t *testing.T) {
		if err := b.Mkdir(ctx, "a/b/c"); err != nil {
			t.Fatalf("Mkdir 失败: %v", err)
		}
		if fi, err := os.Stat(filepath.Join(root, "a", "b", "c")); err != nil || !fi.IsDir() {
			t.Errorf("目录未创建: %v", err)
		}
		// 幂等：密库同步会重复调用 Mkdir，报「已存在」会让整批任务失败
		if err := b.Mkdir(ctx, "a/b/c"); err != nil {
			t.Errorf("重复 Mkdir 应成功，实得 %v", err)
		}
	})

	t.Run("重命名文件", func(t *testing.T) {
		writeRoot(t, root, "old.bin", []byte("data"))
		if err := b.Rename(ctx, "old.bin", "new.bin"); err != nil {
			t.Fatalf("Rename 失败: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(root, "old.bin")); !errors.Is(err, os.ErrNotExist) {
			t.Error("源文件仍存在")
		}
		got, err := os.ReadFile(filepath.Join(root, "new.bin"))
		if err != nil || string(got) != "data" {
			t.Errorf("目标内容 = %q, %v", got, err)
		}
	})

	t.Run("重命名到不存在的子目录", func(t *testing.T) {
		writeRoot(t, root, "m.bin", []byte("move"))
		if err := b.Rename(ctx, "m.bin", "x/y/z/m.bin"); err != nil {
			t.Fatalf("Rename 失败: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(root, "x", "y", "z", "m.bin"))
		if err != nil || string(got) != "move" {
			t.Errorf("跨目录移动失败: %q, %v", got, err)
		}
	})

	t.Run("重命名目录", func(t *testing.T) {
		if err := b.Mkdir(ctx, "dir1/sub"); err != nil {
			t.Fatalf("Mkdir 失败: %v", err)
		}
		writeRoot(t, root, "dir1/sub/inner.bin", []byte("i"))
		if err := b.Rename(ctx, "dir1", "dir2"); err != nil {
			t.Fatalf("Rename 目录失败: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(root, "dir2", "sub", "inner.bin"))
		if err != nil || string(got) != "i" {
			t.Errorf("目录树未整体移动: %q, %v", got, err)
		}
	})

	t.Run("重命名不存在的源", func(t *testing.T) {
		if err := b.Rename(ctx, "ghost", "x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("期望 ErrNotFound，实得 %v", err)
		}
	})

	t.Run("重命名两端都校验越界", func(t *testing.T) {
		writeRoot(t, root, "safe.bin", []byte("s"))
		if err := b.Rename(ctx, "safe.bin", "../escaped.bin"); !errors.Is(err, ErrPathEscape) {
			t.Errorf("目标越界期望 ErrPathEscape，实得 %v", err)
		}
		if err := b.Rename(ctx, "../outside", "in.bin"); !errors.Is(err, ErrPathEscape) {
			t.Errorf("源越界期望 ErrPathEscape，实得 %v", err)
		}
		// 源必须完好无损
		if got, _ := os.ReadFile(filepath.Join(root, "safe.bin")); string(got) != "s" {
			t.Error("越界的 Rename 改动了源文件")
		}
	})

	t.Run("删除文件与目录树", func(t *testing.T) {
		writeRoot(t, root, "gone.bin", []byte("x"))
		writeRoot(t, root, "tree/sub/deep.bin", []byte("y"))

		if err := b.Delete(ctx, "gone.bin"); err != nil {
			t.Fatalf("Delete 文件失败: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(root, "gone.bin")); !errors.Is(err, os.ErrNotExist) {
			t.Error("文件未删除")
		}

		// 递归删除是刻意保留的 Python 语义（shutil.rmtree）：接口文档写的是
		// 「删除文件或空目录」，但上层删除文件夹时依赖递归行为。改成「仅空目录」
		// 会让「删除文件夹」在非空时报错，用户只能手工逐个删。
		if err := b.Delete(ctx, "tree"); err != nil {
			t.Fatalf("Delete 目录失败: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(root, "tree")); !errors.Is(err, os.ErrNotExist) {
			t.Error("目录树未递归删除")
		}
	})

	t.Run("删除不存在的路径", func(t *testing.T) {
		if err := b.Delete(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
			t.Errorf("期望 ErrNotFound，实得 %v", err)
		}
	})

	// 空路径与 "/" 按约定解析为根。Delete/Rename 拿到根就是灾难：
	// Delete("") 会 RemoveAll 掉用户选定的整个文件夹，Rename("", x) 会把
	// 后端根本身搬走，之后所有操作全部报 ErrNotFound。
	//
	// Python 端没有这道防护（delete("") 真的会 rmtree 根目录），只是上层从不
	// 传空路径所以未暴露。Go 端显式拒绝，且必须归为 ErrPathEscape ——
	// 它是确定性失败，若误归 ErrBackend 会被传输队列当「网络抖动」无限重试。
	t.Run("禁止删除或移动根本身", func(t *testing.T) {
		for _, rootPath := range []string{"", "/", "//"} {
			if err := b.Delete(ctx, rootPath); !errors.Is(err, ErrPathEscape) {
				t.Errorf("Delete(%q) 期望 ErrPathEscape，实得 %v", rootPath, err)
			}
			if err := b.Rename(ctx, rootPath, "moved"); !errors.Is(err, ErrPathEscape) {
				t.Errorf("Rename(%q, …) 期望 ErrPathEscape，实得 %v", rootPath, err)
			}
			if err := b.Rename(ctx, "tree", rootPath); !errors.Is(err, ErrPathEscape) {
				t.Errorf("Rename(…, %q) 期望 ErrPathEscape，实得 %v", rootPath, err)
			}
		}
		// 根本身必须完好无损
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			t.Errorf("后端根目录已损坏: %v", err)
		}
		if entries, err := b.ListDir(ctx, "/"); err != nil || len(entries) == 0 {
			t.Errorf("根目录内容丢失: %v, %d 条", err, len(entries))
		}
	})

	// 非破坏性操作仍需接受空路径：ListDir("") 是刷新目录树的常规调用，
	// Exists("/") 是连接时的健康检查。把它们一并拒了就是拒绝服务。
	t.Run("只读操作仍可用空路径访问根", func(t *testing.T) {
		if _, err := b.ListDir(ctx, ""); err != nil {
			t.Errorf(`ListDir("") 失败: %v`, err)
		}
		if ok, err := b.Exists(ctx, "/"); err != nil || !ok {
			t.Errorf(`Exists("/") = (%v, %v)，应为 (true, nil)`, ok, err)
		}
		if err := b.Mkdir(ctx, ""); err != nil {
			t.Errorf(`Mkdir("") 应幂等成功，实得 %v`, err)
		}
	})
}
