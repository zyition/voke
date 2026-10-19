// clipboard.go：把文本写进系统剪贴板——providers 结果行 IntentCopy 的落地端。
//
// §7.2.1：platform 是唯一允许 import mygo 的层；`intent → 副作用` 的这一环放这里，
// logic/ui 只发「复制这段文本」的意图，不碰 mygo 的剪贴板 API。
//
// 线程：mygo.Clipboard.WriteText 是**只许主线程**的调用（走 onMain，见 mygo
// modules 剪贴板模块；examples 里均在 UI 回调/主线程调），与本项目其它 mygo 平台件
// 同纪律。调用点应是主线程的 dispatch 执行器（intent 消费处）。
package platform

import "github.com/egoist/mygo"

// WriteClipboard 把 text 写入系统剪贴板（对照生产 Copy 动作 / Rust `Action::Copy`）。
// 无返回值（mygo.Clipboard.WriteText 即 void）；失败静默——真机复制极少失败，
// 且这是便利操作而非关键路径，不值得为它中断面板流程。
func WriteClipboard(text string) {
	mygo.Clipboard.WriteText(text)
}

// ReadClipboardText 读回系统剪贴板的文本（对照生产 clipboard_selftest 的
// `cx.read_from_clipboard().text()`）。仅供启动自检「写→读回」验证真实剪贴板链路是否
// 通，日常复制路径不用它（IntentCopy 只写不读回）。
//
// 线程同 WriteClipboard：只许主线程（mygo.Clipboard.ReadText 走 onMainValue，见
// modules.go:145；在主循环回调内 onMain 直接内联执行、不自锁，见 loop.go:95-98）。
func ReadClipboardText() string {
	return mygo.Clipboard.ReadText()
}
