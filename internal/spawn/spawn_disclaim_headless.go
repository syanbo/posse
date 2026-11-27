//go:build darwin && headless

package spawn

// headless 构建仅用于无头服务端，不调用 responsibility_spawnattrs_setdisclaim，
// 让经 SSH 拉起的无头 daemon 保留父会话（sshd）的责任链（已实测可访问开启远程完全磁盘访问权限的 Desktop 目录）。
const spawnDisclaim = false
