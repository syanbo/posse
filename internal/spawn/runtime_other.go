//go:build !darwin

package spawn

import "errors"

// 只有 macOS 有 .app 和按负责进程认的授权，别处 Runtime 不会走到这里。
func cloneTree(src, dst string) error { return errors.New("clone not supported") }

func runningIn(dirs []string) map[string]bool {
	busy := map[string]bool{}
	for _, d := range dirs {
		busy[d] = true
	}
	return busy
}
