//go:build darwin && headless

package spawn

// 无头服务端经 SSH 拉起，不剥离父进程归属，自然继承远程登录 sshd-keygen-wrapper 的完全磁盘访问权限。
const spawnDisclaim = false
