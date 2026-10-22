// Package theme 是面板**字号**与**列表行视觉**常量的单一事实来源
// （PoC 文档 §7.4「字号体系」的执行口径 + §7.2.2 目录提案里的 `ui/theme.go`；
// §7.2 重构步骤 7 的安全子件：本轮只把**值**从 view.go 搬进来，
// `view()` / `row()` / `pickerRow()` 等渲染函数本体仍在 view.go，一行未动）。
//
// ## 分层（§7.2.1 三层铁律）
//
// 本包**零 mygo**：搬进来的全是无类型整数常量（DIP / 磅），不需要
// `ui.Color` / `ui.Theme` 才能表达，故 logic 层与 ui 层都能引它而不会把
// mygo 带进 logic，`go test ./internal/ui/...` 在 Linux 原生可跑。
//
// **颜色不在这里**：行的三态底色（静止 `ui.Transparent`、hover
// `t.SurfaceHover`、选中 `t.Selection.Over(t.Background)`）与文字色
// （`t.Text` / `t.TextMuted`）的类型是 mygo 的 `ui.Color`，且**必须**跟着
// 主题走（§7.5.1「为什么 hover 用主题的 SurfaceHover 而不是自造色」：
// 切浅/深色自动跟随，不必维护第二套配色；§7.4 执行口径 3 也明确
// 「字号不是主题项」——只有字号是固定值）。把它们硬抽进零依赖包就会把
// mygo 拖进来，违反本包的存在理由，所以留在 view.go 现取。
//
// ## 红线：数值一个都不许改
//
// 这些值是 `row_visual_test.go` / `panel_behavior_test.go` 的**像素断言**
// 基准：PoC 文档 §10.1.1 的变异测试已证明「`rowGap` 4→0」「`rowInsetX` 6→0」
// 「`rowSlotH` 48→56」都会立刻让对应像素用例变红。本包的
// `TestThemeConstantsLocked` 再钉一层「搬动没改值」。
package theme

const (
	// RowHeight 是结果行高（DIP），与生产 voke search.rs:144 ROW_HEIGHT=48 对齐。
	RowHeight = 48
	// IconSize 是行内图标渲染尺寸（DIP），与 internal/platform 的 iconDIPSize 同值
	// （取图像素 = 该值 × 最大显示器缩放，见 platform/shell.go 的 iconPixelSize）。
	// 2026-10-19 随字号整体下调 32→24，与生产 voke 的 ROW_ICON_SIZE=24
	// （search.rs:150）一致、也与更小的行标题更协调。
	IconSize = 24
	// ActionIconSize / ActionSlot 是结果行尾操作图标的尺寸与每个图标的
	// 固定槽宽。图标比内容图标小一档，避免和标题/缩略图抢视觉；固定槽宽
	// 保证选中/悬浮时才显示 action 时不引发文字重排。
	// 2026-10-22 按真机反馈 18→22（含 4 DIP 内边距 → 30 槽宽）。
	ActionIconSize = 22
	ActionSlot     = ActionIconSize + 8

	// 字号体系（PoC 文档 §7.4 定案，2026-10-16）。四个值必须成体系看，
	// 不要再各自独立硬编码：
	//   - 行标题 TitleSize = 14，正好等于 mygo 主题默认字号
	//     （ui/theme.go:55 Theme.FontSize 字段、:98 defaultFontSize()，
	//     Windows 返回 14），所以切主题时行标题跟着主题走、层级不漂。
	//   - 输入框 InputSize = TitleSize + 2（16 DIP，1.14×）。此前是 22，
	//     逐字抄自官方示例 examples/launcher/main.go:87，但那份示例的行标题
	//     用的是主题默认 14、副行更小，22 与 14 的失衡在示例里本就不明显；
	//     搬进 PoC（行标题同样 14）后输入框比行标题大 57%，观感失衡。
	//     §7.4 执行口径 2 要求写成 `TitleSize + 2` 的表达式而非裸数字。
	//   - 副行 DetailSize = 12 而非 11：12 与 14 的对比度层级比 11 与 14
	//     更清楚，且 11 在 125% DPI 下会糊。
	//   - 提示行 HintSize = 11，比副行更小一档（弱信息，不与副行争层级）。
	//     注意 DetailSize 不能也取 11，否则副行与提示行同号、层级消失。
	TitleSize  = 13
	InputSize  = TitleSize + 2
	DetailSize = 11
	HintSize   = 10
	// FooterSize 是启动器**底部提示行**（快捷键 / 操作提示）字号：比 HintSize 大一档
	//（用户反馈 10 号看不清）。只用于底部提示，设置页的弱文案仍用 HintSize。
	FooterSize = DetailSize + 1

	// 行视觉常量（2026-10-16 真机反馈「每行都有边框圆角、框还连成一片」，
	// 详见 PoC 文档 §7.5.1）。三者的含义与取值的理由都写在 view.go 的
	// row() 的注释里，此处只列数值，改动前先读那段注释。
	//
	// RowSlotH 是**行距**（相邻两行的行中心间距），维持 48 DIP 与生产 voke
	// 的 ROW_HEIGHT 对齐（search.rs:144）——**没有**因为加间隙而缩小它。
	// §10.1 第 5 条：真机若觉得行间留白偏空/偏挤，只调 RowGap 与 RowInsetX，
	// **不要**回去改 RowSlotH（8 行可见性由 TestPanelStillShowsEightRows 守着）。
	RowSlotH = RowHeight
	// RowGap 是相邻两行留白的高度：行槽 48 里药丸只占 40，上下各留 4。
	// 这一条是用户点名的硬要求（「框又连着太难看了」），且是「去框」之外的
	// 第二道保险——即使将来误加回描边，间隙也不会让两个框贴在一起。
	RowGap = 4
	// RowInsetX 是药丸相对行槽左右各缩进的量：让高亮块不贴着列表边缘，
	// 与 CmdPal / PowerToys Run 的条目留白观感一致。
	RowInsetX = 6
)
