// IntentFor 的分流测试：每种 Interact/Action 组合应得到正确的意图。改坏任一分支会红。
package providers

import "testing"

func TestIntentFor(t *testing.T) {
	cases := []struct {
		name string
		m    Match
		want Intent
	}{
		{"Hint→Nothing", MatchHint("p", "说明", "sub"), Intent{Kind: IntentNothing}},
		{"EnterSubquery→EnterSubMode", MatchSubquery("p", "加密", "sub", "enc", 1.0),
			Intent{Kind: IntentEnterSubMode, Text: "enc"}},
		{"Execute+Copy→Copy", MatchExecute("p", "aGVsbG8=", "sub", Action{Kind: ActionCopy, Text: "aGVsbG8="}, 1.0),
			Intent{Kind: IntentCopy, Text: "aGVsbG8="}},
		{"Execute+ActivatePlugin→ActivatePlugin",
			MatchExecute("p", "t", "s", Action{Kind: ActionActivatePlugin, PluginID: "timestamp"}, 1.0),
			Intent{Kind: IntentActivatePlugin, Text: "timestamp"}},
		{"Execute+Launch→PlatformAction",
			MatchExecute("p", "微信", "s", Action{Kind: ActionLaunch, Path: `shell:AppsFolder\X`}, 1.0),
			Intent{Kind: IntentPlatformAction}},
		{"Execute+Refresh→Refresh",
			MatchExecute("p", "重新生成", "s", Action{Kind: ActionRefresh}, 1.0),
			Intent{Kind: IntentRefresh}},
		{"Execute+EndTask→EndTask",
			MatchExecute("p", "窗口", "s", Action{Kind: ActionEndTask, Hwnd: 104}, 1.0),
			Intent{Kind: IntentEndTask, Hwnd: 104}},
		{"Execute+CloseWindow→CloseWindow",
			MatchExecute("p", "窗口", "s", Action{Kind: ActionCloseWindow, Hwnd: 105}, 1.0),
			Intent{Kind: IntentCloseWindow, Hwnd: 105}},
	}
	for _, c := range cases {
		if got := IntentFor(c.m); got != c.want {
			t.Errorf("%s：得到 %+v，想要 %+v", c.name, got, c.want)
		}
	}
}
