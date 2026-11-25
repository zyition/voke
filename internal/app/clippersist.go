// clippersist.go：剪贴板历史的 SQLite 持久化（默认开启，设置页可关）。
//
// 分工：cliphist 仍是唯一的内存真相；本文件只做两件事——
//   - 启动 / 运行时开启持久化时把库读回内存（cliphist.Restore 合并去重）；
//   - 每次历史变化后把增量（upsert）与「库里只保留这些 id」的对账操作投进
//     单线程队列，由后台 goroutine 写库。主线程不做磁盘 IO，图片条目也不会
//     卡住面板。
//
// 关闭持久化会删除 clipboard.db（含 journal 旁文件）：开关语义是「磁盘上不留
// 历史」，而不仅是停止写入。退出时只关闭连接、保留库。
package app

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"voke/internal/logic/appcfg"
	"voke/internal/logic/cliphist"
	"voke/internal/platform"
)

// clipboardPersistEnabled 读配置里的持久化开关：没写 [clipboard] persist 时
// **默认开启**（剪贴板管理器持久化是常态；关闭是显式选择）。
func clipboardPersistEnabled(loaded appcfg.Loaded) bool {
	if !loaded.HasClipboardPersist {
		return true
	}
	return loaded.ClipboardPersist
}

// clipOpKind 是持久化队列的操作类型。
type clipOpKind int

const (
	clipOpUpsert  clipOpKind = iota // 写 / 更新一条
	clipOpKeep                      // 删除不在 ids 里的行（对账）
	clipOpReplace                   // 整库替换（开启时合并后的快照）
)

type clipOp struct {
	kind  clipOpKind
	item  cliphist.Item
	ids   []string
	items []cliphist.Item
}

// clipPersist 是后台写库器。字段只在主线程或它自己的 goroutine 里访问；
// 唯一共享的是 ops 通道。
type clipPersist struct {
	db   *platform.ClipDB
	path string
	ops  chan clipOp
	done chan struct{}
	// failAt 节流错误日志：磁盘满 / 权限变化时别按每次复制刷屏。
	failAt time.Time
}

// clipDBPath 是库文件位置；测试可替换（生产固定为数据目录下 clipboard.db）。
var clipDBPath = func() string { return platform.ClipDBPath(platform.DataDir()) }

// initClipboardPersistence 在 ready 里按配置初始化：默认开启（缺键）时读库合并；
// 只有配置**显式**写了 persist = false 才清残留库。配置损坏回落默认时不删数据。
func (st *appState) initClipboardPersistence(loaded appcfg.Loaded) {
	if clipboardPersistEnabled(loaded) {
		st.startClipboardPersistence()
		return
	}
	if loaded.HasClipboardPersist && !loaded.ClipboardPersist {
		removeClipDBFiles(clipDBPath())
	}
}

// syncClipboardPersistence 在配置重载后对齐持久化状态。只在主线程调用。
func (st *appState) syncClipboardPersistence() {
	if st.hist == nil {
		return
	}
	if clipboardPersistEnabled(st.cfg) {
		st.startClipboardPersistence()
		return
	}
	st.stopClipboardPersistence(true)
}

// startClipboardPersistence 打开库、把旧条目合并进内存，再启动后台写库器。
// 已在运行时是 no-op。打不开库只记日志：内存历史继续工作。
func (st *appState) startClipboardPersistence() {
	if st.hist == nil || st.clipPersist != nil {
		return
	}
	path := clipDBPath()
	db, recs, err := openClipDB(path)
	if err != nil {
		log.Printf("voke 剪贴板持久化不可用：%v", err)
		return
	}
	items := make([]cliphist.Item, 0, len(recs))
	for _, r := range recs {
		items = append(items, cliphist.Item{
			Text: r.Text, HTML: r.HTML, RTF: r.RTF, PNG: r.PNG,
			ThumbPNG: r.ThumbPNG, Width: r.Width, Height: r.Height,
			Pinned: r.Pinned, CreatedAt: clipTime(r.CreatedAt), LastUsedAt: clipTime(r.LastUsedAt),
		})
	}
	// 合并当前会话历史与库里的旧条目（Restore 按内容去重、按时间重排）。
	st.hist.Restore(append(st.hist.Items(), items...))

	p := &clipPersist{
		db:   db,
		path: path,
		ops:  make(chan clipOp, 128),
		done: make(chan struct{}),
	}
	go p.run()
	st.clipPersist = p
	p.enqueue(clipOp{kind: clipOpReplace, items: st.hist.Items()})
}

// stopClipboardPersistence 刷完队列并关闭库；remove=true 时删除库文件。
// 只在主线程调用。
func (st *appState) stopClipboardPersistence(remove bool) {
	p := st.clipPersist
	if p == nil {
		// 库没打开成功（权限 / 损坏）时，用户显式关掉开关也要清掉残留文件。
		if remove && st.cfg.HasClipboardPersist && !st.cfg.ClipboardPersist {
			removeClipDBFiles(clipDBPath())
		}
		return
	}
	st.clipPersist = nil
	p.close()
	if remove {
		removeClipDBFiles(p.path)
	}
}

// persistClipUpsert 把一条（新增 / 置顶变化 / 最近使用时间变化）投进写队列，
// 并顺带对账删除内存里已淘汰的旧行。
func (st *appState) persistClipUpsert(it cliphist.Item) {
	if st.clipPersist == nil {
		return
	}
	st.clipPersist.enqueue(clipOp{kind: clipOpUpsert, item: it})
	st.persistClipReconcile()
}

// persistClipItem 按 id 读回最新状态再投递（Pin / Unpin 后调用）。
func (st *appState) persistClipItem(ref string) {
	if st.clipPersist == nil {
		return
	}
	if it, ok := st.hist.Find(ref); ok {
		st.persistClipUpsert(it)
		return
	}
	st.persistClipReconcile()
}

// persistClipReconcile 让库里的 id 集合与内存一致（删除 / 条数上限变化后调用）。
func (st *appState) persistClipReconcile() {
	if st.clipPersist == nil {
		return
	}
	items := st.hist.Items()
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	st.clipPersist.enqueue(clipOp{kind: clipOpKeep, ids: ids})
}

func (p *clipPersist) enqueue(op clipOp) {
	p.ops <- op // 有缓冲；阻塞只会发生在积压超过 128 条的异常场景
}

func (p *clipPersist) close() {
	close(p.ops)
	<-p.done
	if err := p.db.Close(); err != nil {
		log.Printf("voke 关闭剪贴板库失败：%v", err)
	}
}

func (p *clipPersist) run() {
	defer close(p.done)
	for op := range p.ops {
		var err error
		switch op.kind {
		case clipOpUpsert:
			err = p.db.Upsert(clipRecord(op.item))
		case clipOpKeep:
			err = p.db.KeepOnly(op.ids)
		case clipOpReplace:
			recs := make([]platform.ClipRecord, len(op.items))
			for i := range op.items {
				recs[i] = clipRecord(op.items[i])
			}
			err = p.db.ReplaceAll(recs)
		}
		if err == nil || time.Since(p.failAt) < 30*time.Second {
			continue
		}
		p.failAt = time.Now()
		log.Printf("voke 剪贴板持久化失败：%v", err)
	}
}

// openClipDB 打开库并读全部条目；只有「看起来是坏库」的读错误才备份重建，
// 锁 / 权限 / 磁盘这类问题原样返回（下次启动再试，不动用户数据）。
func openClipDB(path string) (*platform.ClipDB, []platform.ClipRecord, error) {
	db, err := platform.OpenClipDB(path)
	if err != nil {
		return nil, nil, err
	}
	recs, err := db.Load()
	if err == nil {
		return db, recs, nil
	}
	db.Close()
	if !clipDBLooksCorrupt(err) {
		return nil, nil, err
	}
	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
	if berr := backupClipDBFiles(path, backup); berr != nil {
		return nil, nil, fmt.Errorf("%v（备份坏库失败：%v）", err, berr)
	}
	log.Printf("voke 剪贴板库损坏，已备份到 %s：%v", backup, err)
	db, err = platform.OpenClipDB(path)
	if err != nil {
		return nil, nil, err
	}
	recs, err = db.Load()
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, recs, nil
}

// clipDBLooksCorrupt 区分「文件坏了」与「暂时打不开」：锁 / 忙的原样报错，
// 不要因为一次竞争就把历史改名。
func clipDBLooksCorrupt(err error) bool {
	msg := strings.ToLower(err.Error())
	return !strings.Contains(msg, "locked") && !strings.Contains(msg, "busy")
}

// backupClipDBFiles 把主库与可能的旁文件一起改名，避免旧 journal 作用到新库。
func backupClipDBFiles(path, backup string) error {
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		if err := os.Rename(path+suffix, backup+suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func clipRecord(it cliphist.Item) platform.ClipRecord {
	return platform.ClipRecord{
		ID: it.ID, Text: it.Text, HTML: it.HTML, RTF: it.RTF, PNG: it.PNG,
		ThumbPNG: it.ThumbPNG, Width: it.Width, Height: it.Height,
		Pinned: it.Pinned, CreatedAt: clipNano(it.CreatedAt), LastUsedAt: clipNano(it.LastUsedAt),
	}
}

func clipNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func clipTime(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// removeClipDBFiles 删除库与 SQLite 可能的旁文件；不存在不算错误。
func removeClipDBFiles(path string) {
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			log.Printf("voke 删除剪贴板库失败（%s%s）：%v", path, suffix, err)
		}
	}
}
