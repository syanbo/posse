//go:build darwin && !headless

package spawn

// 桌面版 Posse.app 拥有独立签名与 TCC 身份，拉起常驻进程时剥离父进程归属。
const spawnDisclaim = true
