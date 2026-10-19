// 图标资产：托盘图标与生产 voke 同源（仓库根 assets/voke-tray.png 的副本）。
package ui

import _ "embed"

//go:embed assets/voke-tray.png
var trayIconPNG []byte

// TrayIcon 返回托盘的 PNG 字节，逐字取自生产资产 assets/voke-tray.png（128×128 RGBA）。
// mygo 的 Tray 接受 PNG 并按需缩放；与 Rust 版用同一张图，避免托盘图标风格不一致。
// 同步来源：仓库根 `assets/voke-tray.png`（升级资产时同步复制到本目录）。
func TrayIcon() []byte { return trayIconPNG }
