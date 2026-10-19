// 表驱动单测：期望值全部写**绝对值**（不用被测常量/被测函数反推阈值，避开 §10.1.1 的同义反复坑）。

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testTable 是一张覆盖 Direct / Indirect / picker / 无前缀 的最小前缀表。
func testTable() Table {
	return Table{
		{Prefix: "=", PluginID: "calculator", Activation: Direct},
		{Prefix: "{", PluginID: "workspaces", Activation: Direct},
		{Prefix: "#", PluginID: PickerPluginID, Activation: Direct},
		{Prefix: "ba", PluginID: "base64", Activation: Indirect},
		{Prefix: "ts", PluginID: "timestamp", Activation: Indirect},
		{Prefix: "", PluginID: "settings", Activation: Direct}, // 无前缀：不参与扫描
	}
}

// mk 构造一个「标准进入路径」的栈状态（测试用）：
//
//	Global      → [Global{query}]
//	PluginPicker→ [Global, Picker{query}]                     （# 进入）
//	Plugin      → [Global, Plugin{query}]                     （前缀直达）
//	SubMode     → [Global, Plugin, SubMode{query}]
func mk(kind Mode, pluginID, subMode, query string) State {
	var s State
	switch kind {
	case Global:
		return s.withQuery(query)
	case PluginPicker:
		return s.withQuery("").push(Scope{Kind: PluginPicker, Query: query})
	case Plugin:
		return s.withQuery("").push(Scope{Kind: Plugin, PluginID: pluginID, Query: query})
	case SubMode:
		p := s.withQuery("").push(Scope{Kind: Plugin, PluginID: pluginID})
		return p.push(Scope{Kind: SubMode, PluginID: pluginID, SubMode: subMode, Query: query})
	default:
		return s
	}
}

// mkViaPicker 构造「经插件列表选中进入」的栈：[Global, Picker, Plugin{query}]。
// 与 mk(Plugin,…) 的区别正在栈里多一层 Picker——退格出栈据此回到列表。
func mkViaPicker(pluginID, query string) State {
	return State{}.withQuery("").push(Scope{Kind: PluginPicker}).
		push(Scope{Kind: Plugin, PluginID: pluginID, Query: query})
}

// sameState 语义相等：四元组（栈顶口径）相等，且**非 Global 帧的种类/归属序列**相等
// （据此区分「前缀直达」与「经 picker」两种进入路径）。底部 Global 帧物化与否不影响判等。
func sameState(a, b State) bool {
	if a.Mode() != b.Mode() || a.PluginID() != b.PluginID() || a.SubMode() != b.SubMode() || a.Query() != b.Query() {
		return false
	}
	return pathSig(a) == pathSig(b)
}

func pathSig(s State) string {
	var b strings.Builder
	for _, sc := range s.Scopes() {
		if sc.Kind == Global {
			continue
		}
		b.WriteString(sc.Kind.String())
		b.WriteByte(':')
		b.WriteString(sc.PluginID)
		b.WriteByte(':')
		b.WriteString(sc.SubMode)
		b.WriteByte(';')
	}
	return b.String()
}

func TestScanFromGlobal(t *testing.T) {
	tests := []struct {
		name  string
		cur   State
		input string
		want  State
	}{
		{"Direct 全等命中 calculator", State{}, "=", mk(Plugin, "calculator", "", "")},
		{"Direct 命中前有前导空白：裁前导后仍命中", State{}.withQuery("x"), "  {", mk(Plugin, "workspaces", "", "")},
		{"Indirect 前缀+空格命中 base64", State{}, "ba ", mk(Plugin, "base64", "", "")},
		{"Indirect 裸前缀不切", State{}, "ba", mk(Global, "", "", "ba")},
		{"Direct 多打一字符不切（全等，不是 StartsWith）", State{}, "=1+1", mk(Global, "", "", "=1+1")},
		{"Direct 前缀后带空格不切", State{}, "= ", mk(Global, "", "", "= ")},
		{"Indirect 多打字符不切", State{}, "bas", mk(Global, "", "", "bas")},
		{"picker 条目走同一条扫描路径，命中压 PluginPicker", State{}, "#", mk(PluginPicker, "", "", "")},
		{"#ba 不兜底（决策⑥）", State{}, "#ba", mk(Global, "", "", "#ba")},
		{"无前缀条目不得劫持空输入", State{}.withQuery("old"), "", mk(Global, "", "", "")},
		{"纯空白输入不得命中任何条目", State{}, "   ", mk(Global, "", "", "   ")},
		{"无命中：裸查询词保留原文（含首尾空格）", State{}, " chrome ", mk(Global, "", "", " chrome ")},
	}

	table := testTable()
	table = append(table, PrefixEntry{Prefix: "=", PluginID: "dupshadow", Activation: Direct})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scan(table, tt.cur, tt.input)
			if !sameState(got, tt.want) {
				t.Errorf("Scan(%q) = %+v，想要 %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestScanStopsJudgingPrefixOnceActivated(t *testing.T) {
	tests := []struct {
		name  string
		cur   State
		input string
		want  State
	}{
		{"已在 calculator，再打别的插件前缀也不切", mk(Plugin, "calculator", "", ""), "{",
			mk(Plugin, "calculator", "", "{")},
		{"已在 calculator，打 picker 前缀也不切", mk(Plugin, "calculator", "", ""), "#",
			mk(Plugin, "calculator", "", "#")},
		{"已在 base64 子模式，再打前缀只当查询词", mk(SubMode, "base64", "enc", "abc"), "ba ",
			mk(SubMode, "base64", "enc", "ba ")},
		{"子模式里查询词原文透传（不裁空格）", mk(SubMode, "base64", "dec", ""), " aB9 ",
			mk(SubMode, "base64", "dec", " aB9 ")},
		{"PluginPicker 里继续打字只更新过滤词", mk(PluginPicker, "", "", ""), "ba",
			mk(PluginPicker, "", "", "ba")},
		{"经 picker 进入的插件里打字也只更新该层词", mkViaPicker("base64", ""), "x",
			mkViaPicker("base64", "x")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scan(testTable(), tt.cur, tt.input)
			if !sameState(got, tt.want) {
				t.Errorf("Scan(%q) = %+v，想要 %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestEnterSubModePushesAndClearsQuery(t *testing.T) {
	cur := mk(Plugin, "base64", "", "残留查询词")
	got := EnterSubMode(cur, "base64", "enc")
	if !sameState(got, mk(SubMode, "base64", "enc", "")) {
		t.Errorf("EnterSubMode 应压入子模式层并清空查询词：得到 %+v", got)
	}
	// 换子模式：再压一层新的子模式（父插件层仍在栈里）。
	got = EnterSubMode(got, "base64", "dec")
	if !sameState(got, func() State {
		return mk(SubMode, "base64", "enc", "").push(Scope{Kind: SubMode, PluginID: "base64", SubMode: "dec"})
	}()) {
		t.Errorf("切换子模式 = %+v", got)
	}
}

func TestSelectPluginKeepsEntryPath(t *testing.T) {
	// 前缀直达后 SelectPlugin（理论上不常见）：压在当前之上。
	got := SelectPlugin(State{}, "base64")
	if !sameState(got, mk(Plugin, "base64", "", "")) {
		t.Errorf("SelectPlugin from Global = %+v，想要 [Global, Plugin]", got)
	}
	// 经 picker 选中：Picker 帧留在栈里（出栈回列表）。
	got = SelectPlugin(mk(PluginPicker, "", "", ""), "base64")
	if !sameState(got, mkViaPicker("base64", "")) {
		t.Errorf("SelectPlugin from Picker = %+v，想要 [Global, Picker, Plugin]", got)
	}
}

func TestBackspaceBack(t *testing.T) {
	t.Run("经 picker 进入的子模式：逐级出栈 Sub→Plugin→Picker→Global，全程不关", func(t *testing.T) {
		cur := State{}.withQuery("").push(Scope{Kind: PluginPicker}).
			push(Scope{Kind: Plugin, PluginID: "base64"}).
			push(Scope{Kind: SubMode, PluginID: "base64", SubMode: "enc", Query: "abc"})
		want := []struct {
			mode   Mode
			plugin string
			sub    string
			query  string
		}{
			{SubMode, "base64", "enc", ""}, // 1：清词
			{Plugin, "base64", "", ""},     // 2：弹 SubMode
			{PluginPicker, "", "", ""},     // 3：弹 Plugin → 回 picker
			{Global, "", "", ""},           // 4：弹 Picker → Global
			{Global, "", "", ""},           // 5：栈底原地
		}
		for i, w := range want {
			next, close := BackspaceBack(cur)
			if close {
				t.Fatalf("第 %d 下不该关面板", i+1)
			}
			if next.Mode() != w.mode || next.PluginID() != w.plugin || next.SubMode() != w.sub || next.Query() != w.query {
				t.Errorf("第 %d 下：得到 %+v，想要 {mode=%s plugin=%s sub=%s query=%q}", i+1, next, w.mode, w.plugin, w.sub, w.query)
			}
			cur = next
		}
	})

	t.Run("前缀直达的插件：出栈回 Global（不经过 picker）", func(t *testing.T) {
		cur := mk(Plugin, "calculator", "", "")
		next, close := BackspaceBack(cur)
		if close || next.Mode() != Global {
			t.Errorf("前缀直达插件出栈应回 Global：得到 %+v close=%v", next, close)
		}
	})

	t.Run("出栈恢复上一层自己的过滤词", func(t *testing.T) {
		// picker 带着过滤词 "uu"，选中 uuid 后出栈应回到带 "uu" 的 picker。
		cur := State{}.withQuery("").push(Scope{Kind: PluginPicker, Query: "uu"}).
			push(Scope{Kind: Plugin, PluginID: "uuid"})
		next, _ := BackspaceBack(cur)
		if next.Mode() != PluginPicker || next.Query() != "uu" {
			t.Errorf("出栈应恢复 picker 过滤词 uu：得到 %+v", next)
		}
	})

	tests := []struct {
		name      string
		cur       State
		want      State
		wantClose bool
	}{
		{"有查询词：只清词，不动栈", mk(SubMode, "base64", "enc", "abc"),
			mk(SubMode, "base64", "enc", ""), false},
		{"插件第一级有查询词：只清词", mk(Plugin, "calculator", "", "="),
			mk(Plugin, "calculator", "", ""), false},
		{"空 + 子模式（前缀直达的父插件）：弹出到插件", mk(SubMode, "base64", "dec", ""),
			mk(Plugin, "base64", "", ""), false},
		{"空 + picker：弹回 Global", mk(PluginPicker, "", "", ""), mk(Global, "", "", ""), false},
		{"空 + Global：原地不动", mk(Global, "", "", ""), mk(Global, "", "", ""), false},
		{"零值 State 当 Global：原地不动", State{}, mk(Global, "", "", ""), false},
		{"Global 带查询词：只清词", mk(Global, "", "", "vim"), mk(Global, "", "", ""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, close := BackspaceBack(tt.cur)
			if !sameState(next, tt.want) || close != tt.wantClose {
				t.Errorf("BackspaceBack(%+v) = %+v, %v，想要 %+v, %v", tt.cur, next, close, tt.want, tt.wantClose)
			}
		})
	}
}

// fakeProvider 只是把收到的查询词原样回显，用来证明引擎不认得任何插件 id。
type fakeProvider struct{ gotQuery string }

func (p *fakeProvider) Search(q string) int { p.gotQuery = q; return 0 }

type stepKind uint8

const (
	kindType      stepKind = iota // 输入框内容变化 → Scan
	kindBackspace                 // 退格 → BackspaceBack
	kindSelect                    // picker 里 Enter 选定插件 → SelectPlugin
	kindSubMode                   // 插件第一级 Enter 子模式 → EnterSubMode
)

type step struct {
	label string
	kind  stepKind
	input string
	want  State
}

// TestScriptedTrace 用三段真实打字序列守住不变式：Query 永不含前缀、插件字段成对、
// 且**退格按进入路径出栈**（前缀直达→Global；经 picker→回 picker）。
func TestScriptedTrace(t *testing.T) {
	table := testTable()
	provider := &fakeProvider{}

	scripts := []struct {
		name  string
		start State
		steps []step
	}{
		{
			name:  "calculator：前缀直达 → 打字过滤 → 退格清词再出栈回 Global（不经过 picker）",
			start: State{},
			steps: []step{
				{"输入空", kindType, "", mk(Global, "", "", "")},
				{"输入 = 命中", kindType, "=", mk(Plugin, "calculator", "", "")},
				{"输入 1+1", kindType, "1+1", mk(Plugin, "calculator", "", "1+1")},
				{"退格清词", kindBackspace, "", mk(Plugin, "calculator", "", "")},
				{"退格出栈回 Global", kindBackspace, "", mk(Global, "", "", "")},
				{"Global 退格原地", kindBackspace, "", mk(Global, "", "", "")},
			},
		},
		{
			name:  "base64：Indirect + 子模式，出栈链 Sub→Plugin→Global",
			start: State{},
			steps: []step{
				{"裸前缀 ba 不切", kindType, "ba", mk(Global, "", "", "ba")},
				{"ba 空格命中", kindType, "ba ", mk(Plugin, "base64", "", "")},
				{"Enter 子模式 enc", kindSubMode, "enc", mk(SubMode, "base64", "enc", "")},
				{"子模式打字", kindType, "aB9", mk(SubMode, "base64", "enc", "aB9")},
				{"退格清词", kindBackspace, "", mk(SubMode, "base64", "enc", "")},
				{"退格出子模式", kindBackspace, "", mk(Plugin, "base64", "", "")},
				{"退格出插件回 Global", kindBackspace, "", mk(Global, "", "", "")},
			},
		},
		{
			name:  "picker：# → 过滤 → 选中 → 退格回 picker（严格 LIFO）",
			start: State{},
			steps: []step{
				{"# 进 picker", kindType, "#", mk(PluginPicker, "", "", "")},
				{"过滤 ba", kindType, "ba", mk(PluginPicker, "", "", "ba")},
				{"选中 base64（picker 留在栈下）", kindSelect, "base64", mkViaPicker("base64", "")},
				{"插件内打字", kindType, "aB9", mkViaPicker("base64", "aB9")},
				{"退格清词", kindBackspace, "", mkViaPicker("base64", "")},
				{"退格回 picker（恢复其过滤词 ba）", kindBackspace, "", mk(PluginPicker, "", "", "ba")},
				{"退格先清 picker 过滤词", kindBackspace, "", mk(PluginPicker, "", "", "")},
				{"退格出 picker 回 Global", kindBackspace, "", mk(Global, "", "", "")},
			},
		},
	}

	for _, sc := range scripts {
		t.Run(sc.name, func(t *testing.T) {
			cur := sc.start
			for i, s := range sc.steps {
				var close bool
				switch s.kind {
				case kindType:
					cur = Scan(table, cur, s.input)
				case kindBackspace:
					cur, close = BackspaceBack(cur)
				case kindSelect:
					cur = SelectPlugin(cur, s.input)
				case kindSubMode:
					cur = EnterSubMode(cur, cur.PluginID(), s.input)
				}
				if close || !sameState(cur, s.want) {
					t.Fatalf("第 %d 步 %s：得到 %+v close=%v，想要 %+v", i, s.label, cur, close, s.want)
				}
				// 不变式：激活态下 Query 不得含任何前缀字符。
				if cur.Mode().Activated() && strings.ContainsAny(cur.Query(), "={#") {
					t.Errorf("第 %d 步 %s：Query %q 含前缀字符", i, s.label, cur.Query())
				}
				// D1：Plugin/SubMode 的归属字段必须成对。
				if cur.Mode() == Plugin && cur.PluginID() == "" {
					t.Errorf("第 %d 步 %s：Plugin 但 PluginID 为空", i, s.label)
				}
				if cur.Mode() == SubMode && (cur.PluginID() == "" || cur.SubMode() == "") {
					t.Errorf("第 %d 步 %s：SubMode 但字段不全 %+v", i, s.label, cur)
				}
			}
		})
	}
	_ = provider
}

// TestNoSecondPrefixJudgement 守铁律②：本包的非测试源码里（注释除外）不得出现第二个
// 前缀判定写法，也不得硬编码插件列表入口字符。
func TestNoSecondPrefixJudgement(t *testing.T) {
	forbidden := []string{"strings.HasPrefix", "HasPrefix(", "TrimPrefix", "strings.Cut", `"#"`}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取包目录失败：%v", err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		checked++
		for lineNo, line := range strings.Split(string(raw), "\n") {
			if code := stripComment(line); code == "" {
				continue
			}
			for _, f := range forbidden {
				if strings.Contains(stripComment(line), f) {
					t.Errorf("%s:%d 出现被禁止的前缀判定写法 %q（判定逻辑只允许一处）", name, lineNo+1, f)
				}
			}
		}
	}
	if checked < 2 {
		t.Errorf("只检查了 %d 个源文件，扫描可能失效", checked)
	}
}

func stripComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

func TestTableLookupByID(t *testing.T) {
	table := Table{
		{Prefix: "{", PluginID: "workspaces", Activation: Direct},
		{Prefix: "=", PluginID: "calculator", Activation: Direct},
		{Prefix: "=", PluginID: "calculator", Activation: Indirect},
	}
	if e, ok := table.lookup("calculator"); !ok || e.Prefix != "=" {
		t.Errorf("lookup calculator = %+v, %v", e, ok)
	}
	// 空表：任何输入都只能是全局裸查询词。
	if got := Scan(Table{}, State{}, "="); !sameState(got, mk(Global, "", "", "=")) {
		t.Errorf("空表 Scan = %+v", got)
	}
}
