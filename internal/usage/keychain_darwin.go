package usage

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// 用 Security 框架读钥匙串（hive 不开 cgo，经 purego 调）。不调 /usr/bin/security：
// 用户在弹窗里对它点"始终允许"，等于放行所有借它读这一项的程序。

const (
	errSecSuccess               = 0
	errSecItemNotFound          = -25300
	errSecUserCanceled          = -128
	errSecAuthFailed            = -25293
	errSecInteractionNotAllowed = -25308
	kCFStringEncodingUTF8       = 0x08000100
)

var kc struct {
	once sync.Once
	err  error

	copyMatching func(query uintptr, result *uintptr) int32
	dictCreate   func(alloc uintptr, keys, values *uintptr, n int, keyCB, valCB uintptr) uintptr
	strCreate    func(alloc uintptr, s *byte, enc uint32) uintptr
	dataLen      func(data uintptr) int
	dataPtr      func(data uintptr) *byte
	release      func(ref uintptr)

	class, genericPassword, service, account, returnData, matchLimit, matchOne, cfTrue uintptr
	keyCB, valCB                                                                       uintptr // 回调结构体的地址
}

// global 取框架导出的全局变量（CFStringRef 之类）的值。
func global(h uintptr, name string) (uintptr, error) {
	addr, err := purego.Dlsym(h, name)
	if err != nil {
		return 0, err
	}
	return **(**uintptr)(unsafe.Pointer(&addr)), nil
}

func loadKeychain() error {
	kc.once.Do(func() {
		sec, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			kc.err = err
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			kc.err = err
			return
		}
		purego.RegisterLibFunc(&kc.copyMatching, sec, "SecItemCopyMatching")
		purego.RegisterLibFunc(&kc.dictCreate, cf, "CFDictionaryCreate")
		purego.RegisterLibFunc(&kc.strCreate, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&kc.dataLen, cf, "CFDataGetLength")
		purego.RegisterLibFunc(&kc.dataPtr, cf, "CFDataGetBytePtr")
		purego.RegisterLibFunc(&kc.release, cf, "CFRelease")
		for _, g := range []struct {
			h    uintptr
			name string
			dst  *uintptr
		}{
			{sec, "kSecClass", &kc.class}, {sec, "kSecClassGenericPassword", &kc.genericPassword},
			{sec, "kSecAttrService", &kc.service}, {sec, "kSecAttrAccount", &kc.account},
			{sec, "kSecReturnData", &kc.returnData}, {sec, "kSecMatchLimit", &kc.matchLimit},
			{sec, "kSecMatchLimitOne", &kc.matchOne}, {cf, "kCFBooleanTrue", &kc.cfTrue},
		} {
			if *g.dst, kc.err = global(g.h, g.name); kc.err != nil {
				return
			}
		}
		if kc.keyCB, kc.err = purego.Dlsym(cf, "kCFTypeDictionaryKeyCallBacks"); kc.err != nil {
			return
		}
		kc.valCB, kc.err = purego.Dlsym(cf, "kCFTypeDictionaryValueCallBacks")
	})
	return kc.err
}

func cfString(s string) uintptr {
	b := append([]byte(s), 0)
	return kc.strCreate(0, &b[0], kCFStringEncodingUTF8)
}

// keychainPassword 读一项通用密码。
func keychainPassword(service, account string) (string, error) {
	if err := loadKeychain(); err != nil {
		return "", err
	}
	svc, acc := cfString(service), cfString(account)
	defer kc.release(svc)
	defer kc.release(acc)
	keys := []uintptr{kc.class, kc.service, kc.account, kc.returnData, kc.matchLimit}
	vals := []uintptr{kc.genericPassword, svc, acc, kc.cfTrue, kc.matchOne}
	q := kc.dictCreate(0, &keys[0], &vals[0], len(keys), kc.keyCB, kc.valCB)
	defer kc.release(q)
	var data uintptr
	switch st := kc.copyMatching(q, &data); st {
	case errSecSuccess:
	case errSecItemNotFound, errSecUserCanceled, errSecAuthFailed, errSecInteractionNotAllowed:
		return "", ErrCursorDenied
	default:
		return "", fmt.Errorf("keychain: OSStatus %d", st)
	}
	defer kc.release(data)
	n := kc.dataLen(data)
	if n <= 0 {
		return "", ErrCursorDenied
	}
	return string(unsafe.Slice(kc.dataPtr(data), n)), nil
}
