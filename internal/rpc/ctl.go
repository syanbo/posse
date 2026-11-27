package rpc

import "path/filepath"

// AppCtlSocket 是窗口进程接 posse web 请求的 unix socket 路径。
func AppCtlSocket(home string) string { return filepath.Join(home, "app.sock") }
