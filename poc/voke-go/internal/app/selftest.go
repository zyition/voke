// selftest.go：启动自检（对照生产 voke-app/src/main.rs 的 `selftest_requested` +
// `awake_selftest`）。设 env `VOKE_SELFTEST=1` 时在 ready() 末尾跑一轮，把进程内
// 关键链路的失败项计数打进日志。
//
// 为什么要有它（不是纯开发玩具）：生产用它在真机上自动验证「合成键盘输入进不了输入框 /
// 保持唤醒不能真的等半小时」这类无法靠单测覆盖的链路——awake 自检靠回读**到期时间戳**
// （≈ now+请求分钟）证明 SetThreadExecutionState 线程真的起来并写了状态，无需等人肉
// 观察 30 分钟。这正是 BACKLOG 那条「awake 机器 30 分钟不睡人肉观察」能自动化的部分。
//
// 可测性：保持唤醒的激活状态（active/keepScreen/expiresAt）在 Go 侧独立跟踪、不依赖 OS
// （platform/awake.go 头注），故 awakeSelftest 在 Linux 也能自证状态机全链路；Windows 上
// 额外的收益是那段重申 goroutine 真的调用了 assertExecutionState（置位），env 开关在
// 设备侧触发同一段代码。
//
// 生产另有 clipboard_selftest（写→读回校验真实系统剪贴板）——mygo 的 Clipboard 确有
// 读原语（`Clipboard.ReadText`，modules.go:145），故本文件一并移植（见 clipboardSelftest）。
// routing_selftest 未另写——引擎路由已有完整单测覆盖，生产那段是为「合成输入进不了框」补的，
// Go 侧路由本就可 headless 测，设备侧再验意义有限。
package app

import (
	"log"
	"os"
	"time"

	"github.com/zyition/voke/poc/voke-go/internal/platform"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

// selftestEnv 是开启自检的环境变量（对齐生产用 env 而非命令行参数：Start-Process
// -WorkingDirectory 已在用 env 传其它开关，多一个同类项不加复杂度）。
const selftestEnv = "VOKE_SELFTEST"

// selftestRequested 报告是否要求跑启动自检（VOKE_SELFTEST=1，与生产同名）。
func selftestRequested() bool { return os.Getenv(selftestEnv) == "1" }

// runSelftest 汇总各自检的失败项并落总结日志。返回总失败数（0 = 全过）。
func runSelftest() int {
	failures := awakeSelftest()
	failures += clipboardSelftest()
	log.Printf("voke 启动自检结束，失败项 = %d", failures)
	return failures
}

// clipboardSelftest 复刻生产 clipboard_selftest（main.rs:787）：写一段哨兵文本再读回，
// 证明真实系统剪贴板链路通——这是 Copy 类 provider（base64/uuid/timestamp/…）在设备上
// 「点了真能复制」的唯一进程内证据。写→读回有极短竞态（OS 剪贴板提交异步），照生产做至多
// 10 次、每次间隔 150ms 的重试。返回失败项数（0 或 1）。只在主线程调（clipboard 读/写 are
// main-thread；onMain 在主线程内联执行、不自锁，见 loop.go:95）。
func clipboardSelftest() int {
	const expected = "voke-selftest-42"
	platform.WriteClipboard(expected)
	for attempt := 1; attempt <= 10; attempt++ {
		if got := platform.ReadClipboardText(); got == expected {
			log.Printf("clipboard 自检 ok：写入并读回 %q（第 %d 次尝试）", got, attempt)
			return 0
		}
		time.Sleep(150 * time.Millisecond)
	}
	log.Printf("clipboard 自检 FAIL：写 %q 后读回 %q，始终不符", expected, platform.ReadClipboardText())
	return 1
}

// awakeSelftest 复刻生产 awake_selftest 的状态链，返回失败项数：
//  1. CancelAwake 幂等清场 → AwakeState 未激活；
//  2. SetAwake(30 分钟，不连屏) → 激活、keepScreen=false、expiresAt ≈ now+1800s（偏差 ≤5s）；
//  3. awake provider 查询应见「取消保持唤醒」**可执行项**（判据取 ActionCancelAwake 而非
//     文案，避免文案迭代把自检带偏——同生产注释）；
//  4. CancelAwake → 回到未激活。
//
// 第 2 步失败即提前返回（后续断言在无状态下无意义），与生产一致。
func awakeSelftest() int {
	failures := 0

	platform.CancelAwake()
	if active, _, _ := platform.AwakeState(); active {
		log.Printf("awake 自检 FAIL：取消后状态仍为激活")
		failures++
	}

	m := 30
	platform.SetAwake(&m, false)
	now := time.Now().Unix()
	active, keepScreen, expiresAt := platform.AwakeState()
	if !active {
		log.Printf("awake 自检 FAIL：开启后状态仍为未激活（重申 goroutine 可能立刻退出）")
		return failures + 1
	}
	if keepScreen {
		log.Printf("awake 自检 FAIL：请求不保持屏幕，实际却连屏")
		failures++
	}
	const want = int64(30 * 60)
	if expiresAt == nil || absInt64(*expiresAt-(now+want)) > 5 {
		log.Printf("awake 自检 FAIL：到期时间偏差过大（expiresAt=%v，期望≈now+%d）", ptrI64(expiresAt), want)
		failures++
	} else {
		log.Printf("awake 自检 ok：已写状态（剩余≈%ds，keepScreen=%v）", *expiresAt-now, keepScreen)
	}

	// 用与 live 注册表同款状态源（读 platform.AwakeState）的 provider，验证插件侧真的
	// 呈现「可取消」——这是 awake 直达路径（托盘 openAwake / 前缀进入）能取消的前提。
	rows := awakeProvider().Query(providers.QueryCtx{Raw: "awake", Query: "awake"})
	if !hasAction(rows, providers.ActionCancelAwake) {
		log.Printf("awake 自检 FAIL：激活态下 provider 未给出「取消保持唤醒」可执行项")
		failures++
	}

	platform.CancelAwake()
	if active, _, _ := platform.AwakeState(); active {
		log.Printf("awake 自检 FAIL：二次取消后状态仍为激活")
		failures++
	}
	return failures
}

// hasAction 报告这批 Match 里是否有携带某 Action.Kind 的可执行项。
func hasAction(ms []providers.Match, kind providers.ActionKind) bool {
	for _, m := range ms {
		if m.Action.Kind == kind {
			return true
		}
	}
	return false
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ptrI64 仅用于日志（把 *int64 打成可读值，nil 打 "nil"）。
func ptrI64(p *int64) string {
	if p == nil {
		return "nil"
	}
	return time.Unix(*p, 0).UTC().Format(time.RFC3339)
}
