package storage

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// baiduEntryCache 是路径 → {fsid,size,isdir,dlink} 的组合缓存。
//
// 对照 Python 的设计缺口：Python 端 _entry_of 每次请求都付一次
// 「列父目录」API（download_range 还要再付一次 filemetas），且 dlink 缓存
// 的键是 fsid（_dlink_cache: dict[fsid, ...]，baidu_backend.py:189）——
// 路径查不到 fsid 时缓存形同虚设。Go 端以路径为键、两个保鲜期分层：
//
//   - entry（fsid/size/isdir）保鲜 5 分钟：远端目录树任何改动都会使
//     size/fsid 失效，所以保鲜期短；
//   - dlink 保鲜 6 小时（官方约 8 小时，保守取 6h，对齐 Python _DLINK_TTL）；
//
// 全部可变状态受 mu 保护；回源（miss 时的 API 调用）用自实现的
// 单飞（inflightCall）合并同键并发，避免列表与分片下载同时命中同路径时
// 重复列父目录。不引入 x/sync：go.mod 只有 x/sys 一个直接依赖。

// cachedEntry 是缓存里的一个条目（expire 由两个时间戳分别控制）。
type cachedEntry struct {
	fsid  int64
	size  int64
	isdir bool

	dlink      string
	dlinkValid bool

	entryAt time.Time // entry 字段的保鲜起点
	dlinkAt time.Time // dlink 字段的保鲜起点
}

// inflightCall 是一次进行中的同键回源；owner 执行后 close(done) 广播结果。
type inflightCall struct {
	done chan struct{}
	val  any
	err  error
}

type baiduEntryCache struct {
	mu    sync.Mutex
	items map[string]cachedEntry
	calls map[string]*inflightCall // 键 → 进行中的回源（remove 可摘除）
}

func newBaiduEntryCache() baiduEntryCache {
	return baiduEntryCache{
		items: map[string]cachedEntry{},
		calls: map[string]*inflightCall{},
	}
}

// freshEntry 判断条目字段是否仍在保鲜期内。
func freshEntry(at time.Time) bool {
	return !at.IsZero() && time.Since(at) < baiduEntryTTL
}

// freshDlink 判断 dlink 字段是否可复用：dlink 在保鲜期内还不够，
// 条目（fsid）也必须新鲜——同名文件被替换后旧 dlink 可能指向新内容，
// 复用会读到错位数据。
func freshDlink(e cachedEntry) bool {
	return e.dlinkValid && freshEntry(e.entryAt) &&
		!e.dlinkAt.IsZero() && time.Since(e.dlinkAt) < baiduDlinkTTL
}

// acquire 登记一个回源航班；返回 (航班, 本调用是否 owner)。
// owner 负责执行回源并 close(done)；其余调用共享同一航班等待结果。
func (c *baiduEntryCache) acquire(key string) (*inflightCall, bool) {
	if call, ok := c.calls[key]; ok {
		return call, false
	}
	call := &inflightCall{done: make(chan struct{})}
	c.calls[key] = call
	return call, true
}

// waitCall 等待回源结果；ctx 先取消时返回 ctx.Err()（owner 的执行不受影响，
// 它携带自己的 ctx 检查取消）。注意：owner 失败的错误会广播给所有等待者，
// 其中某个等待者的 ctx 取消也会收到同样的失败——传输队列按任务整体取消，
// 同路径并发 goroutine 同生共死，此竞态无实际危害。
func waitCall(ctx context.Context, call *inflightCall) error {
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// store 由 owner 调用：锁外执行回源 fn，锁内决定是否落缓存并广播结果。
//
// 落缓存的先决条件：回源期间该键未被 remove（改名/删除/上传都会摘除航班
// 并清 items）。若被 remove，说明路径已指向别的对象，写入会把过期内容
// 回填给新路径——放弃写入，但结果仍广播（本次调用方照常使用）。
//
// flightKey 是带前缀的航班键（e:/d: 区分两类回源，见 acquire 调用处）。
func (c *baiduEntryCache) store(flightKey string, call *inflightCall, fn func() (cachedEntry, error)) {
	e, err := fn()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		if _, alive := c.calls[flightKey]; alive {
			c.items[flightKey[2:]] = e // 去掉 e:/d: 前缀即路径键
		}
		call.val = &e
	} else {
		call.err = err
	}
	delete(c.calls, flightKey)
	close(call.done)
}

// getEntry 取条目（fsid/size/isdir）；路径不存在 → ErrNotFound。
//
// 航班键用 "e:" 前缀与 getDlink 的 "d:" 区分——两类回源产物类型不同
// （baiduEntry vs cachedEntry），共用航班会因类型断言而错乱；代价是
// 两类请求恰好同时 miss 时最多两次回源（Python 端每次请求恒付两次
// API，这里的并发窗口仍更优）。
func (c *baiduEntryCache) getEntry(ctx context.Context, b *Baidu, path string) (*baiduEntry, error) {
	key := baiduAbs(path)
	flightKey := "e:" + key

	c.mu.Lock()
	if e, ok := c.items[key]; ok && freshEntry(e.entryAt) {
		c.mu.Unlock()
		return &baiduEntry{Name: pathName(key), IsDir: e.isdir, Size: e.size, FsID: e.fsid}, nil
	}
	call, owner := c.acquire(flightKey)
	c.mu.Unlock()

	if !owner {
		if err := waitCall(ctx, call); err != nil {
			return nil, err
		}
		v := call.val.(*cachedEntry)
		return &baiduEntry{Name: pathName(key), IsDir: v.isdir, Size: v.size, FsID: v.fsid}, nil
	}

	// 回源：列父目录取条目；旧 dlink 若仍保鲜且 fsid 未变则原样保留
	// （dlink 6 小时不失效，不该被一次 5 分钟的条目刷新连带作废）
	c.store(flightKey, call, func() (cachedEntry, error) {
		e, err := b.entryOf(ctx, path)
		if err != nil {
			return cachedEntry{}, err
		}
		out := cachedEntry{
			fsid: e.FsID, size: e.Size, isdir: e.IsDir,
			entryAt: time.Now(),
		}
		c.mu.Lock()
		old, hasOld := c.items[key]
		c.mu.Unlock()
		if hasOld && freshDlink(old) && old.fsid == e.FsID {
			out.dlink = old.dlink
			out.dlinkValid = true
			out.dlinkAt = old.dlinkAt
		}
		return out, nil
	})
	if call.err != nil {
		return nil, call.err
	}
	v := call.val.(*cachedEntry)
	return &baiduEntry{Name: pathName(key), IsDir: v.isdir, Size: v.size, FsID: v.fsid}, nil
}

// getDlink 取 (fsid, dlink)；文件不存在或目标是目录 → ErrNotFound
// （目录没有 dlink；跨后端一致：下载目录即「不存在」）。
//
// 条目新鲜但 dlink 过期时只补一次 filemetas；全命中则 0 次 API 调用
// （Python 每次下载都要列父目录 + filemetas 两次）。
func (c *baiduEntryCache) getDlink(ctx context.Context, b *Baidu, path string) (int64, string, error) {
	key := baiduAbs(path)
	flightKey := "d:" + key

	c.mu.Lock()
	if e, ok := c.items[key]; ok && freshDlink(e) {
		c.mu.Unlock()
		return e.fsid, e.dlink, nil
	}
	call, owner := c.acquire(flightKey)
	c.mu.Unlock()

	if !owner {
		if err := waitCall(ctx, call); err != nil {
			return 0, "", err
		}
		if call.err != nil {
			return 0, "", call.err
		}
		v := call.val.(*cachedEntry)
		return v.fsid, v.dlink, nil
	}

	c.store(flightKey, call, func() (cachedEntry, error) {
		// 组装基础条目：新鲜则复用缓存里的 fsid；否则回源列父目录
		c.mu.Lock()
		cur, fresh := c.items[key]
		c.mu.Unlock()
		if !fresh || !freshEntry(cur.entryAt) {
			e, err := b.entryOf(ctx, path)
			if err != nil {
				return cachedEntry{}, err
			}
			cur = cachedEntry{fsid: e.FsID, size: e.Size, isdir: e.IsDir, entryAt: time.Now()}
		}
		if cur.isdir {
			return cachedEntry{}, fmt.Errorf("%w: 百度网盘目录没有 dlink：%s", ErrNotFound, path)
		}

		dlink, err := b.fetchDlink(ctx, cur.fsid)
		if err != nil {
			return cachedEntry{}, err
		}
		cur.dlink = dlink
		cur.dlinkValid = true
		cur.dlinkAt = time.Now()
		return cur, nil
	})
	if call.err != nil {
		return 0, "", call.err
	}
	v := call.val.(*cachedEntry)
	return v.fsid, v.dlink, nil
}

// fetchDlink 调 multimedia/filemetas 拿 fsid 的直链。
// 对照 baidu_backend.py:351-365（_get_dlink 的接口调用部分）。
func (b *Baidu) fetchDlink(ctx context.Context, fsid int64) (string, error) {
	q := url.Values{}
	q.Set("method", "filemetas")
	q.Set("fsids", fmt.Sprintf("[%d]", fsid)) // fsids 是 JSON 数组字符串
	q.Set("dlink", "1")
	out, err := b.api(ctx, apiParams{endpoint: "multimedia", query: q})
	if err != nil {
		return "", err
	}
	metas, _ := out["list"].([]any)
	if len(metas) == 0 {
		return "", fmt.Errorf("%w: 获取 dlink 失败：%s", ErrBackend, truncateJSON(out))
	}
	m, ok := metas[0].(map[string]any)
	if !ok {
		return "", fmt.Errorf("%w: 获取 dlink 失败：%s", ErrBackend, truncateJSON(out))
	}
	dlink := jsonStr(m, "dlink")
	if dlink == "" {
		return "", fmt.Errorf("%w: 获取 dlink 失败：%s", ErrBackend, truncateJSON(out))
	}
	return dlink, nil
}

// remove 使路径缓存失效（改名/删除/上传后调用）。进行中的两类回源航班
// （e:/d:）也被摘除：它们完成时 store 发现 calls 中已无本航班登记，
// 不会回填旧内容。
func (c *baiduEntryCache) remove(path string) {
	key := baiduAbs(path)
	c.mu.Lock()
	delete(c.items, key)
	delete(c.calls, "e:"+key)
	delete(c.calls, "d:"+key)
	c.mu.Unlock()
}

// pathName 取绝对路径的最后一段（缓存条目 Name 字段展示用）。
func pathName(abs string) string {
	return abs[strings.LastIndexByte(abs, '/')+1:]
}
