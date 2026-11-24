//go:build android

package tslink

import (
	// 启用 Tailscale 的 Android 无 netlink 网卡枚举与 DNS 兜底，
	// 避免 Android 11+ 沙盒下 netmon 调 netlinkrib 报 EPERM 崩溃。
	_ "tailscale.com/feature/androidbin"
	"tailscale.com/net/netns"
)

// SetAndroidBindToNetwork 注册 Android JNI 的底层网络绑定钩子（Network.bindSocket），
// 让 tsnet 的 socket 可绑定到物理 Wi-Fi/蜂窝网络。
func SetAndroidBindToNetwork(fn func(fd int) error) {
	netns.SetAndroidBindToNetworkFunc(fn)
}
