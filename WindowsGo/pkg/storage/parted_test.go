package storage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 分卷（Parted）测试。
//
// 三件事必须被钉死，否则用户看到的是「视频少了后半段」这类静默数据错误：
//  1. **写**：超过分卷尺寸的文件被切成若干个 <名>.part-N 对象，每个都是原始
//     密文的连续片段，拼起来与整份一致；
//  2. **读**：任意逻辑区间（跨卷、卷首卷尾、越界）都能取回正确字节，
//     且上层完全看不到分卷的存在（GetSize/ListDir 给的是逻辑视图）；
//  3. **兼容**：历史单对象照旧可读；容量变化（大→小、小→大）不会留下
//     与新内容并存的旧对象。
//
// 契约类用例（TestPartedContract）在**三类后端**上跑同一组断言：本地文件夹、
// WebDAV（mock 服务器）、百度网盘（mock API）—— 用户明确要求分卷对三种后端
// 一致生效，而不是只对网盘生效。

const testPartSize = 4096

// partedContent 造一份可校验的测试内容：长度 size，字节 = i*31+7（mod 256）。
func partedContent(size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte(i*31 + 7)
	}
	return out
}

// writeTempSource 把内容写到临时文件，返回路径。
func writeTempSource(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("写源文件失败: %v", err)
	}
	return p
}

// TestPartedContract 在三类后端上验证分卷的写/读/列/删/改名语义一致。
func TestPartedContract(t *testing.T) {
	// 3.5 个分卷：末卷短读是分卷最容易出错的边界
	data := partedContent(testPartSize*3 + testPartSize/2)
	src := writeTempSource(t, data)

	for _, fx := range newContractFixtures(t) {
		t.Run(fx.kind, func(t *testing.T) {
			ctx := context.Background()
			p := NewParted(fx.backend, testPartSize)
			if err := p.Mkdir(ctx, "videos"); err != nil {
				t.Fatalf("建目录失败: %v", err)
			}
			const remote = "videos/movie.mp4.cpenc"
			// ListDir 回的是**目录内**的名字（不带 "videos/" 前缀），
			// 断言分卷名时要拿基名拼，否则查表恒 miss（拿不到大小）。
			_, remoteBase := splitParent(remote)

			if err := p.UploadChunked(ctx, src, remote, 0, nil); err != nil {
				t.Fatalf("分卷上传失败: %v", err)
			}

			// —— 写：远端确实被拆成 4 个对象，且没有整份对象 ——
			raw, err := fx.backend.ListDir(ctx, "videos")
			if err != nil {
				t.Fatalf("列原始目录失败: %v", err)
			}
			parts := map[string]int64{}
			var whole bool
			for _, e := range raw {
				if e.Name == "movie.mp4.cpenc" {
					whole = true
				}
				if strings.HasPrefix(e.Name, "movie.mp4.cpenc"+PartSuffix) {
					parts[e.Name] = e.Size
				}
			}
			if whole {
				t.Error("分卷上传后不应残留整份对象")
			}
			if len(parts) != 4 {
				t.Fatalf("远端应有 4 个分卷，实得 %d 个: %v", len(parts), parts)
			}
			for i := 1; i <= 3; i++ {
				if got := parts[partName(remoteBase, i)]; got != testPartSize {
					t.Errorf("第 %d 卷大小 = %d，应为 %d", i, got, testPartSize)
				}
			}
			if got := parts[partName(remoteBase, 4)]; got != testPartSize/2 {
				t.Errorf("末卷大小 = %d，应为 %d", got, testPartSize/2)
			}

			// —— 上层视图：一个文件、总大小、卷名不外露 ——
			entries, err := p.ListDir(ctx, "videos")
			if err != nil {
				t.Fatalf("列目录失败: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("上层应只看到 1 个条目，实得 %d 个: %+v", len(entries), entries)
			}
			if entries[0].Name != "movie.mp4.cpenc" {
				t.Errorf("条目名 = %q，应为逻辑名", entries[0].Name)
			}
			if entries[0].Size != int64(len(data)) {
				t.Errorf("条目大小 = %d，应为 %d（各卷之和）", entries[0].Size, len(data))
			}
			if size, err := p.GetSize(ctx, remote); err != nil || size != int64(len(data)) {
				t.Errorf("GetSize = (%d, %v)，应为 (%d, nil)", size, err, len(data))
			}
			if size, err := p.Head(ctx, remote); err != nil || size != int64(len(data)) {
				t.Errorf("Head = (%d, %v)，应为 (%d, nil)", size, err, len(data))
			}
			if ok, err := p.Exists(ctx, remote); err != nil || !ok {
				t.Errorf("Exists = (%v, %v)，应为 true", ok, err)
			}

			// —— 读：跨卷、卷首卷尾、越界 ——
			ranges := []struct {
				name       string
				start, end int64
			}{
				{"卷内", 100, 200},
				{"跨 1→2 卷", testPartSize - 10, testPartSize + 10},
				{"跨 2→3 卷", testPartSize*2 - 1, testPartSize*2 + 1},
				{"末卷尾部", int64(len(data)) - 100, int64(len(data)) - 1},
				{"整份", 0, int64(len(data)) - 1},
				{"end 越过 EOF", int64(len(data)) - 50, int64(len(data)) + 500},
			}
			for _, rg := range ranges {
				want := data[rg.start:min64(rg.end+1, int64(len(data)))]
				got, err := p.DownloadRange(ctx, remote, rg.start, rg.end)
				if err != nil {
					t.Fatalf("%s: DownloadRange 失败: %v", rg.name, err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s: 取回 %d 字节，应为 %d 字节（内容不符）", rg.name, len(got), len(want))
				}

				// 零拷贝路径必须与 DownloadRange 逐字节一致
				buf := make([]byte, len(want))
				n, err := p.DownloadRangeInto(ctx, remote, rg.start, rg.end, buf)
				if err != nil {
					t.Fatalf("%s: DownloadRangeInto 失败: %v", rg.name, err)
				}
				if !bytes.Equal(buf[:n], want) {
					t.Errorf("%s: DownloadRangeInto 取回 %d 字节，内容不符", rg.name, n)
				}
			}

			// 起点越过 EOF：空切片而非错误（与三后端既有语义一致）
			got, err := p.DownloadRange(ctx, remote, int64(len(data))+10, int64(len(data))+20)
			if err != nil {
				t.Fatalf("越界读应返回空切片而非错误: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("越界读应返回空切片，实得 %d 字节", len(got))
			}

			// —— 改名：整组卷跟着走，内容不变 ——
			const renamed = "videos/renamed.mp4.cpenc"
			if err := p.Rename(ctx, remote, renamed); err != nil {
				t.Fatalf("重命名失败: %v", err)
			}
			if left, _ := fx.backend.ListDir(ctx, "videos"); len(left) != 4 {
				t.Errorf("改名后远端应仍是 4 个对象，实得 %d", len(left))
			}
			if got, err := p.DownloadRange(ctx, renamed, 0, 99); err != nil ||
				!bytes.Equal(got, data[:100]) {
				t.Errorf("改名后读取失败或内容不符: %v", err)
			}
			if ok, _ := p.Exists(ctx, remote); ok {
				t.Error("改名后旧名不应还存在")
			}

			// —— 删除：整组卷一起删，不留孤儿 ——
			if err := p.Delete(ctx, renamed); err != nil {
				t.Fatalf("删除失败: %v", err)
			}
			if rest, err := fx.backend.ListDir(ctx, "videos"); err != nil || len(rest) != 0 {
				t.Errorf("删除后远端目录应为空，实得 %+v（err=%v）", rest, err)
			}
			if ok, _ := p.Exists(ctx, renamed); ok {
				t.Error("删除后 Exists 应为假")
			}
		})
	}
}

// TestPartedSmallFileStaysSingle 未达分卷尺寸的文件仍是一个对象
// —— 绝大多数文件（照片、文档、小视频）不该被无谓地切开。
func TestPartedSmallFileStaysSingle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inner, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	p := NewParted(inner, testPartSize)

	data := partedContent(testPartSize) // 恰好等于阈值 → 整份
	src := writeTempSource(t, data)
	const remote = "small.cpenc"
	if err := p.UploadChunked(ctx, src, remote, 0, nil); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "small.cpenc")); err != nil {
		t.Errorf("应落成单个对象: %v", err)
	}
	entries, _ := inner.ListDir(ctx, "")
	for _, e := range entries {
		if strings.Contains(e.Name, PartSuffix) {
			t.Errorf("不应出现分卷对象: %s", e.Name)
		}
	}
	if size, _ := p.GetSize(ctx, remote); size != int64(len(data)) {
		t.Errorf("GetSize = %d，应为 %d", size, len(data))
	}
	got, err := p.DownloadRange(ctx, remote, 0, int64(len(data))-1)
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("单对象读取失败或内容不符: %v", err)
	}
}

// TestPartedLayoutChangesLeaveNoStaleObjects 容量双向变化时不留并存对象。
//
// 两个方向都必须清干净：大→小要删掉多余分卷（否则读取路径会优先命中
// 残留的整份对象），小→大要删掉整份对象（否则读到的是旧的小文件）。
// 这条是「上传了新版本，打开的却还是旧内容」这类静默错误的守卫。
func TestPartedLayoutChangesLeaveNoStaleObjects(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inner, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	p := NewParted(inner, testPartSize)
	const remote = "doc.bin.cpenc"

	big := partedContent(testPartSize * 3)
	if err := p.UploadChunked(ctx, writeTempSource(t, big), remote, 0, nil); err != nil {
		t.Fatalf("上传大文件失败: %v", err)
	}
	if entries, _ := inner.ListDir(ctx, ""); len(entries) != 3 {
		t.Fatalf("大文件应落成 3 个分卷，实得 %d", len(entries))
	}

	// 大 → 小：分卷必须被清掉，且读到的是新内容
	small := partedContent(1000)
	if err := p.UploadChunked(ctx, writeTempSource(t, small), remote, 0, nil); err != nil {
		t.Fatalf("上传小文件失败: %v", err)
	}
	entries, _ := inner.ListDir(ctx, "")
	if len(entries) != 1 || entries[0].Name != remote {
		t.Fatalf("大→小后远端应只剩 1 个整份对象，实得 %+v", entries)
	}
	if got, err := p.DownloadRange(ctx, remote, 0, 999); err != nil || !bytes.Equal(got, small) {
		t.Errorf("大→小后内容不符: %v", err)
	}

	// 小 → 大：整份对象必须被清掉
	if err := p.UploadChunked(ctx, writeTempSource(t, big), remote, 0, nil); err != nil {
		t.Fatalf("再次上传大文件失败: %v", err)
	}
	entries, _ = inner.ListDir(ctx, "")
	if len(entries) != 3 {
		t.Fatalf("小→大后远端应只剩 3 个分卷，实得 %+v", entries)
	}
	for _, e := range entries {
		if e.Name == remote {
			t.Error("小→大后整份对象应被删除（否则读取会命中旧内容）")
		}
	}
	if got, err := p.DownloadRange(ctx, remote, 0, int64(len(big))-1); err != nil || !bytes.Equal(got, big) {
		t.Errorf("小→大后内容不符: %v", err)
	}
}

// TestPartedReadsLegacySingleObject 历史单对象（v1.01 及更早、或未达阈值的文件）
// 必须照旧读得出来 —— 分卷不能把老数据变成不可读。
func TestPartedReadsLegacySingleObject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inner, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	// 直接往远端放一个「整份」对象（绕过 Parted，模拟历史数据）
	data := partedContent(testPartSize * 2)
	if err := inner.UploadChunked(ctx, writeTempSource(t, data), "legacy.cpenc", 0, nil); err != nil {
		t.Fatal(err)
	}

	p := NewParted(inner, testPartSize)
	if size, err := p.GetSize(ctx, "legacy.cpenc"); err != nil || size != int64(len(data)) {
		t.Errorf("GetSize = (%d, %v)，应为 (%d, nil)", size, err, len(data))
	}
	got, err := p.DownloadRange(ctx, "legacy.cpenc", testPartSize-5, testPartSize+5)
	if err != nil || !bytes.Equal(got, data[testPartSize-5:testPartSize+6]) {
		t.Errorf("历史单对象跨阈值区间读取失败: %v", err)
	}
	entries, _ := p.ListDir(ctx, "")
	if len(entries) != 1 || entries[0].Name != "legacy.cpenc" {
		t.Errorf("历史单对象应正常出现在列表里: %+v", entries)
	}
}

// TestPartedIgnoresLookalikeNames 用户文件自己不参与分卷识别。
//
// `foo.part-1.cpenc` 是合法的密文容器名（含 .part- 但结尾是 .cpenc），
// 绝不能被当成某个文件的分卷藏起来或连坐删除。
func TestPartedIgnoresLookalikeNames(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inner, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	// 直接把两个「名字里带 .part-」的对象放到远端
	for _, name := range []string{"foo.part-1.cpenc", "bar.cpenc.part-x"} {
		if err := inner.UploadChunked(ctx, writeTempSource(t, []byte("x")), name, 0, nil); err != nil {
			t.Fatal(err)
		}
	}

	p := NewParted(inner, testPartSize)
	entries, err := p.ListDir(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("两个普通对象都应各自成条目，实得 %+v", entries)
	}
	// 删除 bar.cpenc.part-x：这是它自己的名字，不该牵连别的对象
	if err := p.Delete(ctx, "bar.cpenc.part-x"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if ok, _ := p.Exists(ctx, "foo.part-1.cpenc"); !ok {
		t.Error("删除 bar.cpenc.part-x 牵连了无关对象")
	}
}

// TestPartedUploadFallbackWithoutRangeUploader 后端没实现 RangeUploader 时
// 走「切片落临时文件再整份上传」的回退路径，结果必须一致。
//
// 百度就是这类后端（XPAN 分片上传以整个文件为单位），所以这条回退路径
// 不是理论分支，而是百度用户的日常路径。
func TestPartedUploadFallbackWithoutRangeUploader(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inner, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	p := NewParted(backendWithoutRange{inner}, testPartSize)

	data := partedContent(testPartSize*2 + 17)
	src := writeTempSource(t, data)
	const remote = "fallback.cpenc"
	if err := p.UploadChunked(ctx, src, remote, 0, nil); err != nil {
		t.Fatalf("回退路径上传失败: %v", err)
	}
	entries, _ := inner.ListDir(ctx, "")
	if len(entries) != 3 {
		t.Fatalf("应落成 3 个分卷，实得 %+v", entries)
	}
	got, err := p.DownloadRange(ctx, remote, 0, int64(len(data))-1)
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("回退路径内容不符: %v", err)
	}
}

// backendWithoutRange 故意只实现 Backend（不实现 RangeUploader），
// 用来覆盖 Parted 的回退路径。
type backendWithoutRange struct{ Backend }

// TestPartedProgressIsMonotonic 分卷上传的进度必须单调递增并收到 1.0。
//
// 进度按「累计完成量 / 总大小」折算，切卷时若直接转发单卷进度，
// 用户会看到进度条每传完一卷就跳回 0。
func TestPartedProgressIsMonotonic(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inner, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	p := NewParted(inner, testPartSize)

	data := partedContent(testPartSize * 3)
	var got []float64
	err = p.UploadChunked(ctx, writeTempSource(t, data), "prog.cpenc", 0, func(v float64) {
		got = append(got, v)
	})
	if err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("没有任何进度回调")
	}
	last := 0.0
	for i, v := range got {
		if v < last {
			t.Fatalf("进度回退：第 %d 次 %f < 前一次 %f（全部：%v）", i, v, last, got)
		}
		if v < 0 || v > 1 {
			t.Fatalf("进度越界：%f", v)
		}
		last = v
	}
	if last != 1.0 {
		t.Errorf("末值进度 = %f，应为 1.0", last)
	}
}

// TestPartedRejectsBadRange 非法区间与后端同语义（ErrRange）。
func TestPartedRejectsBadRange(t *testing.T) {
	ctx := context.Background()
	inner, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := NewParted(inner, testPartSize)
	if _, err := p.DownloadRange(ctx, "x.cpenc", 10, 5); !errors.Is(err, ErrRange) {
		t.Errorf("end < start 应返回 ErrRange，实得 %v", err)
	}
	if _, err := p.DownloadRange(ctx, "x.cpenc", -1, 5); !errors.Is(err, ErrRange) {
		t.Errorf("负起点应返回 ErrRange，实得 %v", err)
	}
}
