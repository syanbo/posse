//go:build !android

package tslink

// SetAndroidBindToNetwork 在非 Android 平台上为空操作。
func SetAndroidBindToNetwork(func(fd int) error) {}
