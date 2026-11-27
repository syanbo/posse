//go:build !darwin && !linux

package term

func watchExit(_ int, fn func(code int)) {
	go fn(-1)
}
