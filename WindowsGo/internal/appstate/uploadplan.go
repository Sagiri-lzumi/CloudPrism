// uploadplan.go —— 「直读本机路径」上传的展开逻辑与上传前清单。
//
// 背景（用户 2026-09-27 提出）：浏览器上传必须把文件内容先经浏览器读到暂存
// 目录（用户机器上就是 C 盘），导出一个文件夹等于把整个文件夹在 C 盘再抄一份。
// 但本程序**本来就跑在同一台机器上**，后端完全可以自己打开这些文件。
//
// 于是分出两条通路：
//   - **拖放**：浏览器只能给 File 对象（拿不到绝对路径），照旧 multipart +
//     暂存目录 —— 这条通路无法避免，只能保持原样；
//   - **按钮（上传文件 / 上传文件夹）**：走网页版路径选择器拿到**绝对路径**，
//     直接交给这里的展开逻辑，全程不经过浏览器、不占暂存空间。
//
// 本文件只做「展开 + 统计」，是纯本地操作（不碰密库、不碰网络）。
package appstate

import (
	"io/fs"
	"os"
	"path/filepath"
)

// localFile 是一个待上传的本地文件：local 为绝对路径，rel 为相对本次上传
// 基准目录的逻辑路径（POSIX 分隔符，末段即远端展示名）。
type localFile struct{ local, rel string }

const (
	// scanMaxFiles 是「待上传清单」单次扫描的文件数上限。
	//
	// 限制的是扫描耗时而非内存：误选整个盘符时 walk 上百万文件要几十秒，
	// 而这份清单只是给用户看一眼的预览。超限即停并置 Truncated，由前端
	// 如实提示「只统计了前 N 项」—— 不谎报一个看起来完整的数字。
	scanMaxFiles = 200000
	// scanMaxDetail 是清单里回传**明细**的条数上限。总数与总大小仍是全量统计，
	// 只有逐条列表会被截断（几百行足够用户确认「我选对了没有」）。
	scanMaxDetail = 500
)

// expandLocalPaths 把本地路径清单展开成（文件, 逻辑相对路径）与目录清单。
//
// UploadPaths（真正入队）与 ScanUploadPaths（上传前预览）**共用这一个函数**：
// 两边只要有一处 rel 规则不一致，用户看到的清单就会与实际传的东西不符 ——
// 那比根本不给清单更糟。maxFiles <= 0 表示不设上限。
//
// 语义（对照已移除的 Python 端 _expand_dir_tasks）：
//   - 单文件 → rel 取**文件名**（平铺进目标远端目录，不带它所在的本地目录）；
//   - 目录   → 以该目录为根递归，rel 相对该根；根目录自身不进 dirs（它就是
//     基准），其余子目录全部保留，从而复现用户看到的目录结构。
func expandLocalPaths(localPaths []string, maxFiles int) (files []localFile, dirs []string, truncated bool, err error) {
	for _, p := range localPaths {
		st, sErr := os.Stat(p)
		if sErr != nil {
			return nil, nil, false, sErr
		}
		if !st.IsDir() {
			files = append(files, localFile{p, filepath.Base(p)})
			continue
		}
		root := filepath.Clean(p)
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, wErr error) error {
			if wErr != nil {
				return wErr
			}
			rel, rErr := filepath.Rel(root, path)
			if rErr != nil {
				return rErr
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if path != root {
					dirs = append(dirs, rel)
				}
				return nil
			}
			if maxFiles > 0 && len(files) >= maxFiles {
				truncated = true
				return fs.SkipAll // 停止本次 walk；其余 root 仍需各自判定
			}
			files = append(files, localFile{path, rel})
			return nil
		})
		if walkErr != nil {
			return nil, nil, false, walkErr
		}
	}
	return files, dirs, truncated, nil
}

// UploadPlanItem 是清单里的一条文件明细。
type UploadPlanItem struct {
	Rel  string `json:"rel"`  // 相对目标目录的逻辑路径（含目录段）
	Size int64  `json:"size"` // 字节；取不到时为 0
}

// UploadPlan 是「待上传清单」：直读本机路径前交用户确认。
type UploadPlan struct {
	// Items 是明细，最多 scanMaxDetail 条。
	Items []UploadPlanItem `json:"items"`
	// TotalFiles 是文件总数；Truncated 时是**已扫描到的**数量。
	TotalFiles int   `json:"totalFiles"`
	TotalDirs  int   `json:"totalDirs"`
	TotalBytes int64 `json:"totalBytes"`
	// Truncated 表示已达 scanMaxFiles，统计不完整（前端必须如实说明）。
	Truncated bool `json:"truncated"`
}

// ScanUploadPaths 扫描本地路径、生成待上传清单（**不发起任何上传**）。
//
// 纯本地操作，因此是包级函数而非 State 方法 —— 未连接密库时也能预览。
// 取不到大小的条目按 0 计：文件在扫描与上传之间被删掉是正常竞态，不该
// 让整份清单失败。
func ScanUploadPaths(localPaths []string) (UploadPlan, error) {
	files, dirs, truncated, err := expandLocalPaths(localPaths, scanMaxFiles)
	if err != nil {
		return UploadPlan{}, err
	}
	plan := UploadPlan{
		Items:      make([]UploadPlanItem, 0, min(len(files), scanMaxDetail)),
		TotalFiles: len(files),
		TotalDirs:  len(dirs),
		Truncated:  truncated,
	}
	for i, f := range files {
		var size int64
		if st, sErr := os.Stat(f.local); sErr == nil {
			size = st.Size()
		}
		plan.TotalBytes += size
		if i < scanMaxDetail {
			plan.Items = append(plan.Items, UploadPlanItem{Rel: f.rel, Size: size})
		}
	}
	return plan, nil
}
