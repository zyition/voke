// clipboard.go：把文本写进系统剪贴板——providers 结果行 IntentCopy 的落地端。
//
// §7.2.1：platform 是唯一允许 import mygo 的层；`intent → 副作用` 的这一环放这里，
// logic/ui 只发「复制这段文本」的意图，不碰 mygo 的剪贴板 API。
//
// 线程：mygo.Clipboard.WriteText 是**只许主线程**的调用（走 onMain，见 mygo
// modules 剪贴板模块；examples 里均在 UI 回调/主线程调），与本项目其它 mygo 平台件
// 同纪律。调用点应是主线程的 dispatch 执行器（intent 消费处）。
package platform

import (
	"errors"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

// rtfFormat 是 Windows 的 CF_RTF 注册格式名（Word / WordPad / 浏览器写富文本用的名字）。
// mygo 对未内建的格式按名字注册与读取，故读写两端都用这一个常量。
const rtfFormat transfer.Format = "Rich Text Format"

// clipExcludeFormat 是密码管理器等敏感来源设置的「不要进剪贴板历史」标记；
// 只要它出现（不论值）就跳过本次采集。
const clipExcludeFormat transfer.Format = "ExcludeClipboardContentFromMonitorProcessing"

// clipCanIncludeFormat 是 Windows 自己的历史开关：值为 DWORD 0 时同样跳过。
const clipCanIncludeFormat transfer.Format = "CanIncludeInClipboardHistory"

// ClipPayload 是一次剪贴板采集到的全部可用表示（对应剪贴板历史 Item 的原始内容）。
// 字段为空表示该格式不存在；Text 与 PNG 至少有一个非空才算有效内容。
type ClipPayload struct {
	Text string
	HTML []byte
	RTF  []byte
	PNG  []byte
}

// Empty 表示没有任何可保存的表示。
func (p ClipPayload) Empty() bool {
	return p.Text == "" && len(p.HTML) == 0 && len(p.RTF) == 0 && len(p.PNG) == 0
}

// ReadClipboardPayload 读取当前系统剪贴板的文本 / HTML / RTF / PNG 四种表示。
//
// 返回 ok=false 的情形（都属预期跳过，不记错误日志）：
//   - 剪贴板为空或只有不支持的格式；
//   - 带有「排除历史」标记（敏感内容）；
//   - 是文件列表（FileList / CF_HDROP，按需求不采集）；
//   - 读取过程被其它程序抢走所有权或数据超过 mygo 的 64MiB 硬上限。
//
// 线程：可后台调用——mygo.Clipboard.Read 内部经 onMainValue 回主线程执行（见
// modules.go 剪贴板模块的线程约定）；但在主线程直接调用同样安全。
func ReadClipboardPayload() (ClipPayload, bool) {
	formats := mygo.Clipboard.Formats()
	if len(formats) == 0 {
		return ClipPayload{}, false
	}
	if containsFormat(formats, transfer.FileList) || containsFormat(formats, clipExcludeFormat) {
		return ClipPayload{}, false
	}
	if containsFormat(formats, clipCanIncludeFormat) {
		// 该格式存在时读 4 字节 DWORD：0 = 源程序要求不要进入剪贴板历史。
		if b, err := mygo.Clipboard.ReadFormat(clipCanIncludeFormat); err == nil && len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 0 {
			return ClipPayload{}, false
		}
	}

	data, err := mygo.Clipboard.Read(transfer.Text, transfer.HTML, rtfFormat, transfer.PNG)
	if err != nil {
		return ClipPayload{}, false
	}
	var p ClipPayload
	if b, err := data.Read(transfer.Text); err == nil {
		p.Text = string(b)
	}
	if b, err := data.Read(transfer.HTML); err == nil {
		p.HTML = b
	}
	if b, err := data.Read(rtfFormat); err == nil {
		p.RTF = b
	}
	if b, err := data.Read(transfer.PNG); err == nil {
		p.PNG = b
	}
	if p.Empty() {
		return ClipPayload{}, false
	}
	return p, true
}

func containsFormat(formats []transfer.Format, want transfer.Format) bool {
	for _, f := range formats {
		if f == want {
			return true
		}
	}
	return false
}

// WriteClipboardPayload 把一条历史项按原格式写回系统剪贴板。RTF/HTML/Text/PNG 同时
// 提供，接收方按自己支持的格式取最合适的一份；纯图片项只写 PNG。
//
// 线程：只许主线程（mygo.Clipboard.Write 经 onMain，且在提供者回调里禁止重入）。
func WriteClipboardPayload(p ClipPayload) error {
	var reps []transfer.Representation
	if len(p.RTF) > 0 {
		reps = append(reps, transfer.Bytes(rtfFormat, p.RTF))
	}
	if len(p.HTML) > 0 {
		reps = append(reps, transfer.Bytes(transfer.HTML, p.HTML))
	}
	if p.Text != "" {
		reps = append(reps, transfer.Bytes(transfer.Text, []byte(p.Text)))
	}
	if len(p.PNG) > 0 {
		reps = append(reps, transfer.Bytes(transfer.PNG, p.PNG))
	}
	if len(reps) == 0 {
		return errors.New("剪贴板内容为空")
	}
	return mygo.Clipboard.Write(transfer.New(transfer.NewItem(reps...)))
}

// WriteClipboardPlain 把历史项降级为纯文本写回（Ctrl+Enter / 图片项没有此动作）。
func WriteClipboardPlain(p ClipPayload) error {
	if p.Text == "" {
		return errors.New("该条目没有文本表示")
	}
	return mygo.Clipboard.Write(transfer.New(transfer.NewItem(transfer.Bytes(transfer.Text, []byte(p.Text)))))
}

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
