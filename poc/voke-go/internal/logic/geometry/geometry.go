// Package geometry 是面板定位的**纯算术**（§7.2.1 三层铁律里的 logic 层，
// §7.2 重构步骤4 从 panel.go（package main）原样搬来）。
//
// 分层：本包**零 mygo、零 x/sys/windows**，只依赖标准库，故
// `go test ./internal/logic/...` 在 Linux 原生可跑。搬过来的只有「给定
// work area 尺寸 + 面板尺寸 + 锚点系数 → 返回面板左上角」这条与平台无关的
// 算术；**显示器从哪来**（mygo.Screen.PrimaryDisplay）、**窗口怎么建/怎么
// SetPosition** 都留在 platform/ui 侧（panel.go），本包一概不知。
//
// WorkArea 与 SetPosition 都是 int DIP（mygo window.go:56
// Rectangle{X,Y,Width,Height int}、:879 SetPosition(x, y int)），所以入参与
// 返回一律 int，全整数运算。
package geometry

const (
	// PanelWidth 是面板宽度（DIP）。
	PanelWidth = 640
	// PanelHeight 是面板高度（DIP）：mygo 的 chrome 由内容自然高度决定。
	// 下表的 y 是**实测**值（Tester 探针给输入行/分隔线铺底色后从渲染结果
	// 反读，见 PoC 文档 §11.11 / §7.6.2），不是估值；要可见 8 行需
	// 494 - 384 = 110 DIP 的 chrome：
	//
	//	输入行   y   0..55  = Row.Padding(16,18) + TextInput 高 24 = 56
	//	分隔线   y  56     （1 DIP）
	//	列表区   y  57..459 = 403（padding 12 + 8 行 × 48 + 余量）
	//	分隔线   y 460     （1 DIP）
	//	提示行   y 461..493 = 33
	//
	// 初版 420 只能看 ~6.5 行，与生产 voke 的 8 行不齐（生产 472 =
	// 88 chrome + 48×8 定死，search.rs:156/168）；420→494 见 §8.6。
	PanelHeight = 494
	// CenterPercent 是**面板中心**落在 work area 高度里的百分比，即
	//
	//	面板中心 y = WorkArea.Y + WorkArea.Height * pct / 100
	//	面板顶     = 面板中心 - 面板高/2
	//
	// 为什么锚「面板中心」而不是「输入框中心」（2026-10-16 真机反馈第二轮
	// 「还是高」后的定因，完整排查记在 PoC 文档 §7.6.2）：
	//
	//	沿用旧锚点时 1080p 的输入框中心落在 24.7%——**已经与 CmdPal 的
	//	25.0% 几乎重合**，可用户依然说「偏高」。说明当初对标的两个参照
	//	根本不是同一个口径：
	//
	//	  - CmdPal 的 25% 锚在**搜索框中心**，而它那块卡片 compact 模式下
	//	    总高只有约 48 DIP（只显示搜索行）。对它而言「搜索框中心」就等于
	//	    「整块卡片的中心」，25% 同时是卡片中心。
	//	  - 本面板总高 494 DIP、可见 8 行，实测输入框中心只距面板顶 28 DIP，
	//	    与面板几何中心相距 247 DIP。把输入框锚在 25%，等于把**整块面板
	//	    的视觉重心**（1080p 下 ≈46% 处）一起拽到上半屏。眼睛读的是整块
	//	    面板的体量，不是顶部那一条输入框——于是「输入框位置算对了」但
	//	    「整块面板仍然偏高」。这正是系数从 1/3 调到 42 仍不达标的根因：
	//	    修的是锚点错了，不是系数不够大。
	//
	// 换锚点后系数第一次直接表达用户那句话「**居中略偏上**」：
	// 48 = 面板中心在 work area 的 48%，即正中偏上 2%。
	//
	//	分辨率      旧 42%（顶口径）        新 48%（中心口径）
	//	1080p      面板中心 45.8%           面板中心 48.0%  输入框中心 27.9%
	//	1440p      面板中心 44.8%           面板中心 48.0%  输入框中心 33.0%
	//
	// 参照：生产 voke（Rust）是**纯正中**（面板中心 50%、输入框中心
	// 28.8%）；CmdPal 输入框中心 25.0%。新值落在两者之间、偏 Rust 一侧，
	// 与用户「CmdPal 与 Rust 版高度差不多」的观感一致。真机若仍想再低，
	// 把 48 改成 50 即完全对齐 Rust 的正中，语义不变、只挪一个系数。
	//
	// 写成 work area 的百分比（而非面板顶占「剩余空间」的百分比）有两个
	// 实测好处：① 系数与面板高度解耦，改 PanelHeight 不会悄悄挪位置；
	// ② 「略偏上」的比例跨分辨率保持——旧口径在 1440p 上同样的 42% 会
	//	退化成面板中心 44.8%，偏得比 1080p 更上面（面板占屏比越小，
	//	「剩余空间的百分比」越难线性映射到中心）。
	//
	// 用整数运算而非浮点：WorkArea 与 SetPosition 都是 int DIP
	// （mygo window.go:56 Rectangle{X,Y,Width,Height int}、
	// :879 SetPosition(x, y int)），写成浮点会被 Go 截断取整、引入
	// 无意义的舍入；整数乘除的结果完全确定。
	CenterPercent = 52
)

// PanelTopLeft 是定位算术的**唯一实现**：给定 work area 与面板尺寸，返回
// 面板左上角（int DIP）。panel.go 的 showPanel 调它，定位单测也调它——单测
// 里**不要**再抄一份，否则改了生产算术测试照样绿（第一版就是这么写的，变异
// 测试「把算术改回旧口径」时全绿，已修正）。
//
//	水平：work area 水平居中
//	垂直：面板中心落在 work area 高度的 CenterPercent 处，再减半个面板高
//
// 全整数运算：WorkArea 与 SetPosition 都是 int DIP（mygo window.go:56/:879），
// 用浮点会被 Go 截断取整、引入无意义的舍入。
func PanelTopLeft(waX, waY, waW, waH, panelW, panelH int) (x, y int) {
	x = waX + (waW-panelW)/2
	cy := waY + waH*CenterPercent/100
	y = cy - panelH/2
	return x, y
}
