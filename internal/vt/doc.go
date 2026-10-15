// Package vt 绑定 libghostty-vt，复制自 MyGo 的 plugins/terminal/internal/vt（MIT，见 LICENSE.mygo），
// 与 MyGo 终端插件加载同一个库文件。复制的原因是常驻进程需要插件没有公开的能力：
// 读取屏幕行以修正快照（见 snapshot.go）。升级 MyGo 时需同步更新这些文件。
// 常驻进程不处理键盘、鼠标和选区，MyGo 里的 encode.go、selection.go 没有复制；
// RenderState（render_test.go）只给测试比较快照还原结果，它的符号在测试里才加载。
package vt
