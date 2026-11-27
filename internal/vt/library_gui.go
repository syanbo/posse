//go:build !headless

package vt

import "github.com/egoist/mygo/plugins/terminal"

func fallbackLibraryPath(_ NativeLibrary) (string, error) {
	return terminal.LibraryPath()
}
