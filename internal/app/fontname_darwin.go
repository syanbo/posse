//go:build darwin

package app

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// ctNames 是取字体本地化名字要用的 CoreFoundation、CoreText 接口，第一次用时加载。
var ctNames struct {
	once sync.Once
	ok   bool

	familyAttr          uintptr // kCTFontFamilyNameAttribute
	keyCallbacks        uintptr // &kCFTypeDictionaryKeyCallBacks
	valueCallbacks      uintptr // &kCFTypeDictionaryValueCallBacks
	stringCreate        func(alloc uintptr, s *byte, enc uint32) uintptr
	stringLength        func(s uintptr) int
	stringMaxSize       func(n int, enc uint32) int
	stringGet           func(s uintptr, buf *byte, size int, enc uint32) bool
	dictCreate          func(alloc uintptr, keys, values unsafe.Pointer, n int, kcb, vcb uintptr) uintptr
	descriptorCreate    func(attrs uintptr) uintptr
	descriptorLocalized func(desc, attr uintptr, lang *uintptr) uintptr
	release             func(uintptr)
}

const cfUTF8 = 0x08000100

func loadCTNames() bool {
	ctNames.once.Do(func() {
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		ct, err := purego.Dlopen("/System/Library/Frameworks/CoreText.framework/CoreText", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		attr, err := purego.Dlsym(ct, "kCTFontFamilyNameAttribute")
		if err != nil {
			return
		}
		kcb, err1 := purego.Dlsym(cf, "kCFTypeDictionaryKeyCallBacks")
		vcb, err2 := purego.Dlsym(cf, "kCFTypeDictionaryValueCallBacks")
		if err1 != nil || err2 != nil {
			return
		}
		// Dlsym 给的是全局变量的地址，变量里才是 CFStringRef；经指针的指针转换，vet 不把它当成整数转指针
		ctNames.familyAttr = **(**uintptr)(unsafe.Pointer(&attr))
		ctNames.keyCallbacks, ctNames.valueCallbacks = kcb, vcb
		purego.RegisterLibFunc(&ctNames.stringCreate, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&ctNames.stringLength, cf, "CFStringGetLength")
		purego.RegisterLibFunc(&ctNames.stringMaxSize, cf, "CFStringGetMaximumSizeForEncoding")
		purego.RegisterLibFunc(&ctNames.stringGet, cf, "CFStringGetCString")
		purego.RegisterLibFunc(&ctNames.dictCreate, cf, "CFDictionaryCreate")
		purego.RegisterLibFunc(&ctNames.release, cf, "CFRelease")
		purego.RegisterLibFunc(&ctNames.descriptorCreate, ct, "CTFontDescriptorCreateWithAttributes")
		purego.RegisterLibFunc(&ctNames.descriptorLocalized, ct, "CTFontDescriptorCopyLocalizedAttribute")
		ctNames.ok = true
	})
	return ctNames.ok
}

// localizedFamily 取字体家族在用户语言下的名字（"PingFang SC" 是"苹方-简"），没有时返回原名。
// 只用 C 接口、不经过 Objective-C，可以在后台协程里调用。
func localizedFamily(family string) string {
	if !loadCTNames() || family == "" {
		return family
	}
	n := &ctNames
	cstr := append([]byte(family), 0)
	name := n.stringCreate(0, &cstr[0], cfUTF8)
	if name == 0 {
		return family
	}
	defer n.release(name)
	keys, values := [1]uintptr{n.familyAttr}, [1]uintptr{name}
	attrs := n.dictCreate(0, unsafe.Pointer(&keys[0]), unsafe.Pointer(&values[0]), 1, n.keyCallbacks, n.valueCallbacks)
	if attrs == 0 {
		return family
	}
	defer n.release(attrs)
	desc := n.descriptorCreate(attrs)
	if desc == 0 {
		return family
	}
	defer n.release(desc)
	loc := n.descriptorLocalized(desc, n.familyAttr, nil)
	if loc == 0 {
		return family
	}
	defer n.release(loc)
	size := n.stringMaxSize(n.stringLength(loc), cfUTF8) + 1
	buf := make([]byte, size)
	if !n.stringGet(loc, &buf[0], size, cfUTF8) {
		return family
	}
	if i := indexZero(buf); i > 0 {
		return string(buf[:i])
	}
	return family
}

func indexZero(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return len(b)
}
