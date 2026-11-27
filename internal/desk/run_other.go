//go:build !darwin

package desk

import "errors"

func runDarwin(Options) error {
	return errors.New("desk: the desktop helper only runs on macOS")
}
