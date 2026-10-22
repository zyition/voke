package appcfg

import (
	"strings"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/config"
)

// 插件热键按 [providers.<id>] hotkey 落盘并原样读回；未设置时不写该键。
func TestProviderHotkeyRoundtrip(t *testing.T) {
	hk := "Alt+`"
	data, err := Encode(Top{Hotkey: "alt+space"}, []config.UserOverride{{ID: "clipboard", Hotkey: &hk}})
	if err != nil {
		t.Fatalf("Encode 失败：%v", err)
	}
	if !strings.Contains(string(data), `hotkey = "Alt+`+"`"+`"`) {
		t.Errorf("落盘缺插件热键：\n%s", data)
	}
	back, err := FromTOML(data)
	if err != nil {
		t.Fatalf("回读失败：%v\n%s", err, data)
	}
	if len(back.Overrides) != 1 || back.Overrides[0].Hotkey == nil || *back.Overrides[0].Hotkey != hk {
		t.Fatalf("插件热键往返错：%+v", back.Overrides)
	}

	plain, err := Encode(Top{Hotkey: "alt+space"}, []config.UserOverride{{ID: "clipboard"}})
	if err != nil {
		t.Fatal(err)
	}
	// 顶层本身有 hotkey = "alt+space"；插件段未设置时不应再出现第二个。
	if got := strings.Count(string(plain), "hotkey"); got != 1 {
		t.Errorf("未设置插件热键不应落盘（hotkey 出现 %d 次）：\n%s", got, plain)
	}
}
