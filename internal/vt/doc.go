// Package vt 绑定 libghostty-vt，复制自 MyGo 的 plugins/terminal/internal/vt（MIT，见 LICENSE.mygo），
// 与 MyGo 终端插件加载同一个库文件。复制的原因是常驻进程需要插件没有公开的能力：
// 读取屏幕行以修正快照（见 snapshot.go）。升级 MyGo 时需同步更新这些文件。
package vt
