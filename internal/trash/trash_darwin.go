//go:build darwin

package trash

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type foundation struct {
	poolPush func() unsafe.Pointer
	poolPop  func(unsafe.Pointer)
	object   func(objc.ID, objc.SEL) objc.ID
	string   func(objc.ID, objc.SEL, string) objc.ID
	fileURL  func(objc.ID, objc.SEL, objc.ID) objc.ID
	trash    func(objc.ID, objc.SEL, objc.ID, *objc.ID, *objc.ID) bool
	utf8     func(objc.ID, objc.SEL) string
}

var loadFoundation = sync.OnceValues(func() (*foundation, error) {
	lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("trash: load Objective-C runtime: %w", err)
	}
	if _, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return nil, fmt.Errorf("trash: load Foundation: %w", err)
	}
	f := &foundation{}
	for _, binding := range []struct {
		name string
		fn   any
	}{
		{"objc_autoreleasePoolPush", &f.poolPush},
		{"objc_autoreleasePoolPop", &f.poolPop},
		{"objc_msgSend", &f.object},
		{"objc_msgSend", &f.string},
		{"objc_msgSend", &f.fileURL},
		{"objc_msgSend", &f.trash},
		{"objc_msgSend", &f.utf8},
	} {
		symbol, err := purego.Dlsym(lib, binding.name)
		if err != nil {
			return nil, fmt.Errorf("trash: load %s: %w", binding.name, err)
		}
		purego.RegisterFunc(binding.fn, symbol)
	}
	return f, nil
})

func move(path string) (string, error) {
	f, err := loadFoundation()
	if err != nil {
		return "", err
	}
	// 自动释放池按线程记；结果字符串也必须在池回收前复制到 Go。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := f.poolPush()
	defer f.poolPop(pool)

	nsPath := f.string(objc.ID(objc.GetClass("NSString")), objc.RegisterName("stringWithUTF8String:"), path)
	if nsPath == 0 {
		return "", errors.New("trash: path is not valid UTF-8")
	}
	url := f.fileURL(objc.ID(objc.GetClass("NSURL")), objc.RegisterName("fileURLWithPath:"), nsPath)
	if url == 0 {
		return "", errors.New("trash: cannot create file URL")
	}
	manager := f.object(objc.ID(objc.GetClass("NSFileManager")), objc.RegisterName("defaultManager"))
	var result, nsError objc.ID
	if !f.trash(manager, objc.RegisterName("trashItemAtURL:resultingItemURL:error:"), url, &result, &nsError) {
		if nsError != 0 {
			description := f.object(nsError, objc.RegisterName("localizedDescription"))
			if message := f.utf8(description, objc.RegisterName("UTF8String")); message != "" {
				return "", errors.New(message)
			}
		}
		return "", errors.New("trash: cannot move item to trash")
	}
	if result == 0 {
		return "", errors.New("trash: moved item but Foundation returned no destination")
	}
	destination := f.utf8(f.object(result, objc.RegisterName("path")), objc.RegisterName("UTF8String"))
	if destination == "" {
		return "", errors.New("trash: moved item but Foundation returned an empty destination")
	}
	return destination, nil
}
