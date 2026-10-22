// cliphist：内存剪贴板历史的有界存储。
//
// 只做纯数据与预算：文本 / HTML / RTF / PNG、去重、置顶、FIFO 淘汰。
// 采集（平台格式、敏感标记）与粘贴（前台窗口、SendInput）都不在这里。
// 所有方法只在主线程调用；不持久化，退出即清。
package cliphist

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"time"

	xdraw "golang.org/x/image/draw"
)

const (
	DefaultMaxEntries = 100
	MinMaxEntries     = 1
	MaxMaxEntries     = 1000
	MaxPinned         = 10

	// MaxTotalBytes 是历史数据的总预算（含置顶项），只算内容字节，
	// 不含 Go 结构体/切片头。图片与富文本都从这里扣。
	MaxTotalBytes = 64 << 20
	// MaxImageBytes 是单张图片 PNG 的上限；超过则丢弃图片表示，保留文本。
	MaxImageBytes = 16 << 20
	// MaxRichBytes 是单条 Text+HTML+RTF 合计上限；超过则只保留 Text。
	MaxRichBytes = 1 << 20
	// ThumbSize 是缩略图最长边（像素）；UI 以 24 DIP 显示，2x 屏够用。
	ThumbSize = 48
)

var (
	ErrEmpty    = errors.New("cliphist: 空内容")
	ErrTooLarge = errors.New("cliphist: 内容超过上限")
	ErrCapacity = errors.New("cliphist: 历史总预算已满")
)

// Payload 是采集层交来的原始表示（尚未做上限/缩略图处理）。
type Payload struct {
	Text string
	HTML string
	RTF  []byte
	PNG  []byte
}

// Item 是一条不可变内容 + 可变使用状态。切片/字符串在读取方只读，
// Store 不会原地修改。
type Item struct {
	ID         string
	Text       string
	HTML       string
	RTF        []byte
	PNG        []byte
	ThumbPNG   []byte
	Width      int
	Height     int
	Pinned     bool
	CreatedAt  time.Time
	LastUsedAt time.Time
	Bytes      int

	hash [32]byte
}

func (it Item) HasText() bool  { return it.Text != "" }
func (it Item) HasRich() bool  { return it.HTML != "" || len(it.RTF) > 0 }
func (it Item) HasImage() bool { return len(it.PNG) > 0 }

// HasPlainAction 报告是否值得显示「纯文本粘贴」：只有富文本/图片
// 且同时有文本表示时才有意义；纯文本条目再给一个纯文本按钮是噪音。
func (it Item) HasPlainAction() bool { return it.HasText() && (it.HasRich() || it.HasImage()) }

// Store 是主线程专用的内存历史。pinned 与 unpinned 都按最近使用从新到旧；
// pinned 不计入 maxEntries，但与普通项共享字节预算。
type Store struct {
	maxEntries int
	pinned     []*Item
	unpinned   []*Item
	byID       map[string]*Item
	byHash     map[[32]byte]*Item
	bytes      int
}

// New 创建历史上限；非法值夹到 [MinMaxEntries, MaxMaxEntries]。
func New(maxEntries int) *Store {
	return &Store{
		maxEntries: clampEntries(maxEntries),
		byID:       map[string]*Item{},
		byHash:     map[[32]byte]*Item{},
	}
}

func clampEntries(n int) int {
	if n < MinMaxEntries {
		return MinMaxEntries
	}
	if n > MaxMaxEntries {
		return MaxMaxEntries
	}
	return n
}

// MaxEntries 返回当前普通历史条数上限。
func (s *Store) MaxEntries() int { return s.maxEntries }

// SetMaxEntries 更新上限并立刻淘汰最旧普通项；返回淘汰数量。
func (s *Store) SetMaxEntries(n int) int {
	s.maxEntries = clampEntries(n)
	return s.trimCount()
}

// Len 返回含置顶的全部条数。
func (s *Store) Len() int { return len(s.pinned) + len(s.unpinned) }

// UnpinnedLen 返回计入 maxEntries 的普通条数。
func (s *Store) UnpinnedLen() int { return len(s.unpinned) }

// PinnedLen 返回置顶条数。
func (s *Store) PinnedLen() int { return len(s.pinned) }

// Bytes 返回当前内容字节总量（含置顶）。
func (s *Store) Bytes() int { return s.bytes }

// CanPin 报告是否还能新增置顶。
func (s *Store) CanPin() bool { return len(s.pinned) < MaxPinned }

// Add 归一化后写入历史：同内容只更新时间/位置，不新增条目。
func (s *Store) Add(p Payload, now time.Time) (Item, error) {
	p, err := normalize(p)
	if err != nil {
		return Item{}, err
	}
	hash := hashPayload(p)
	if it := s.byHash[hash]; it != nil {
		it.LastUsedAt = now
		if !it.Pinned {
			s.moveUnpinnedFront(it)
		}
		return *it, nil
	}

	thumb, w, h, ok := thumbnail(p.PNG)
	if !ok {
		if len(p.PNG) > 0 {
			p.PNG = nil
		}
		if p.Text == "" && p.HTML == "" && len(p.RTF) == 0 {
			return Item{}, ErrEmpty
		}
	}
	it := &Item{
		ID: hex.EncodeToString(hash[:16]), Text: p.Text, HTML: p.HTML, RTF: p.RTF,
		PNG: p.PNG, ThumbPNG: thumb, Width: w, Height: h,
		CreatedAt: now, LastUsedAt: now, hash: hash,
	}
	it.Bytes = itemBytes(it)

	// 先为普通项腾预算；置顶项不自动淘汰。腾完仍不够则拒绝新条目。
	for s.bytes+it.Bytes > MaxTotalBytes && len(s.unpinned) > 0 {
		s.removeUnpinnedAt(len(s.unpinned) - 1)
	}
	if s.bytes+it.Bytes > MaxTotalBytes {
		return Item{}, ErrCapacity
	}

	s.unpinned = append([]*Item{it}, s.unpinned...)
	s.byID[it.ID] = it
	s.byHash[it.hash] = it
	s.bytes += it.Bytes
	s.trimCount()
	return *it, nil
}

// Find 按 id 取条目副本。
func (s *Store) Find(id string) (Item, bool) {
	if it := s.byID[id]; it != nil {
		return *it, true
	}
	return Item{}, false
}

// Pin 置顶；已置顶返回 true，达到 10 条上限返回 false。
func (s *Store) Pin(id string) bool {
	it := s.byID[id]
	if it == nil {
		return false
	}
	if it.Pinned {
		return true
	}
	if len(s.pinned) >= MaxPinned {
		return false
	}
	s.detachUnpinned(it)
	it.Pinned = true
	s.pinned = append([]*Item{it}, s.pinned...)
	return true
}

// Unpin 取消置顶并放回普通历史最前；随后按条数/字节重新淘汰。
func (s *Store) Unpin(id string) bool {
	it := s.byID[id]
	if it == nil || !it.Pinned {
		return false
	}
	s.detachPinned(it)
	it.Pinned = false
	s.unpinned = append([]*Item{it}, s.unpinned...)
	s.trimCount()
	return true
}

// Delete 删除一条；返回是否存在。
func (s *Store) Delete(id string) bool {
	it := s.byID[id]
	if it == nil {
		return false
	}
	if it.Pinned {
		s.detachPinned(it)
	} else {
		s.detachUnpinned(it)
	}
	s.forget(it)
	return true
}

// Items 返回置顶在前、普通在后的副本（切片与字符串仍只读共享）。
func (s *Store) Items() []Item {
	out := make([]Item, 0, len(s.pinned)+len(s.unpinned))
	for _, it := range s.pinned {
		out = append(out, *it)
	}
	for _, it := range s.unpinned {
		out = append(out, *it)
	}
	return out
}

func (s *Store) trimCount() int {
	removed := 0
	for len(s.unpinned) > s.maxEntries {
		s.removeUnpinnedAt(len(s.unpinned) - 1)
		removed++
	}
	return removed
}

func (s *Store) moveUnpinnedFront(it *Item) {
	s.detachUnpinned(it)
	s.unpinned = append([]*Item{it}, s.unpinned...)
}

// detachUnpinned / detachPinned 只移出链表，不动 byID/byHash/bytes；
// 真正删除时由调用方再 forget。
func (s *Store) detachUnpinned(it *Item) bool {
	for i, cur := range s.unpinned {
		if cur == it {
			s.unpinned = append(s.unpinned[:i], s.unpinned[i+1:]...)
			return true
		}
	}
	return false
}

func (s *Store) detachPinned(it *Item) bool {
	for i, cur := range s.pinned {
		if cur == it {
			s.pinned = append(s.pinned[:i], s.pinned[i+1:]...)
			return true
		}
	}
	return false
}

func (s *Store) removeUnpinnedAt(i int) {
	if i < 0 || i >= len(s.unpinned) {
		return
	}
	it := s.unpinned[i]
	s.detachUnpinned(it)
	s.forget(it)
}

func (s *Store) forget(it *Item) {
	delete(s.byID, it.ID)
	delete(s.byHash, it.hash)
	s.bytes -= it.Bytes
}

func itemBytes(it *Item) int {
	return len(it.Text) + len(it.HTML) + len(it.RTF) + len(it.PNG) + len(it.ThumbPNG)
}

func normalize(p Payload) (Payload, error) {
	if len(p.RTF) > 0 {
		p.RTF = bytes.Clone(p.RTF)
	}
	if len(p.PNG) > 0 {
		p.PNG = bytes.Clone(p.PNG)
	}
	if rich := len(p.Text) + len(p.HTML) + len(p.RTF); rich > MaxRichBytes {
		p.HTML, p.RTF = "", nil
		if len(p.Text) > MaxRichBytes {
			return Payload{}, ErrTooLarge
		}
	}
	imageTooLarge := false
	if len(p.PNG) > 0 {
		if len(p.PNG) > MaxImageBytes {
			p.PNG = nil
			imageTooLarge = true
		}
	}
	if p.Text == "" && p.HTML == "" && len(p.RTF) == 0 && len(p.PNG) == 0 {
		if imageTooLarge {
			return Payload{}, ErrTooLarge
		}
		return Payload{}, ErrEmpty
	}
	return p, nil
}

func hashPayload(p Payload) [32]byte {
	h := sha256.New()
	writePart := func(b []byte) {
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	writePart([]byte(p.Text))
	writePart([]byte(p.HTML))
	writePart(p.RTF)
	writePart(p.PNG)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// thumbnail 校验 PNG 并生成 ThumbSize 内的缩略图；返回原图尺寸。
func thumbnail(data []byte) ([]byte, int, int, bool) {
	if len(data) == 0 {
		return nil, 0, 0, false
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, 0, 0, false
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, false
	}
	w, h := scaledSize(cfg.Width, cfg.Height, ThumbSize)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Over, nil)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, 0, 0, false
	}
	return buf.Bytes(), cfg.Width, cfg.Height, true
}

func scaledSize(w, h, maxSide int) (int, int) {
	if w <= maxSide && h <= maxSide {
		return w, h
	}
	if w >= h {
		return maxSide, max(1, h*maxSide/w)
	}
	return max(1, w*maxSide/h), maxSide
}
