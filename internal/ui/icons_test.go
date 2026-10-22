// 托盘图标资产回归：与生产 voke 同源的 assets/voke-tray.png，断言可解码/尺寸/非全透明。
// 资产被换坏（缺失、非 PNG、尺寸漂移、整图透明）都会红。
package ui

import (
	"bytes"
	"image/png"
	"testing"
)

func TestTrayIconEmbeddedAsset(t *testing.T) {
	b := TrayIcon()
	if len(b) == 0 {
		t.Fatal("TrayIcon 为空（embed 资产缺失？）")
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("TrayIcon 不是合法 PNG：%v", err)
	}
	if got := img.Bounds(); got.Dx() != 128 || got.Dy() != 128 {
		t.Errorf("托盘图标应为 128×128，实际 %d×%d", got.Dx(), got.Dy())
	}
	opaque := 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y && opaque == 0; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0 {
				opaque++
				break
			}
		}
	}
	if opaque == 0 {
		t.Error("托盘图标全透明（资产异常）")
	}
}
