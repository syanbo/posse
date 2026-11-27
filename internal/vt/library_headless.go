//go:build headless

package vt

import "fmt"

func fallbackLibraryPath(lib NativeLibrary) (string, error) {
	return "", fmt.Errorf("terminal: %s not found next to the posse executable; reinstall the headless server bundle or set POSSE_GHOSTTY_VT", lib.Name)
}
