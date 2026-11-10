// iconstore.go：图标 PNG 的**磁盘**缓存（%APPDATA%\voke\icons\）。
//
// 为什么需要：mygo 的 Shell.FileIcon 在 Windows 上走
// IShellItemImageFactory::GetImage（internal/windows/icon.go），本机实测单次
// 10~70ms（144 次取图：平均 15.2ms、最大 72ms），且按 mygo 契约只能在主线程调
// （modules.go:73 → loop.go:96 的 onMainValue）。进程内的两层缓存只能保证
// 「同一会话里第二次不再付费」，重启后要全部重来；落盘后冷启动直接读盘
// （48px PNG 约 1~6KB，亚毫秒）。
//
// Rust 版本来就落盘——数据目录下 icons\ 里那 300+ 个 PNG 就是它的产物；
// Go 版迁移时只保留了进程内缓存，这一步是补回同等能力（不是新机制）。
//
// 失效策略：按来源字符串（exe 路径 / shell:AppsFolder\<AUMID>）取哈希，命中即用，
// 不做 mtime 校验。图标随程序升级变化的情况极罕见，真变了删掉 icons\ 即可
// （与 Rust 版同口径，不对每次唤起都做一次 stat）。
//
// 线程：只在主线程调用（图标补齐泵 internal/app/iconpump.go）。
package platform

import (
	"crypto/sha1"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// iconStoreDir 是图标磁盘缓存目录：数据目录下的 icons\（与配置、日志同源）。
func iconStoreDir() string { return filepath.Join(DataDir(), "iconcache") }

// iconStoreFile 把图标来源映射成缓存文件名。Windows 路径大小写不敏感，
// 统一按小写取哈希，避免同一条目两种写法各存一份。
func iconStoreFile(src string) string {
	sum := sha1.Sum([]byte(strings.ToLower(src)))
	return hex.EncodeToString(sum[:8]) + ".png"
}

// LoadIconPNG 读磁盘缓存。ok=true 表示**缓存命中**：png 为空 = 上次已确认这条
// 来源没有图标（负缓存），不必再去问 Shell；ok=false 才是从未取过。
func LoadIconPNG(src string) (png []byte, ok bool) {
	png, err := os.ReadFile(filepath.Join(iconStoreDir(), iconStoreFile(src)))
	if err != nil {
		return nil, false
	}
	return png, true
}

// SaveIconPNG 写磁盘缓存。png 为空表示「这条来源没有图标」，落一个 0 字节文件
// 当负缓存标记（否则每次启动都会对着同一个无图标来源重付一次 Shell 调用）。
// 失败只记日志：磁盘缓存是加速手段，不能因为它写不进去就影响取图。
func SaveIconPNG(src string, png []byte) {
	dir := iconStoreDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("创建图标缓存目录 %s 失败：%v", dir, err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, iconStoreFile(src)), png, 0o644); err != nil {
		log.Printf("写图标缓存失败（%s）：%v", src, err)
	}
}
