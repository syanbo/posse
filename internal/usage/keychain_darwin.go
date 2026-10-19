package usage

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

static OSStatus hiveKeychainRead(const char *service, const char *account, char **out, long *outLen) {
	CFStringRef svc = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
	CFStringRef acc = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
	const void *keys[] = {kSecClass, kSecAttrService, kSecAttrAccount, kSecReturnData, kSecMatchLimit};
	const void *vals[] = {kSecClassGenericPassword, svc, acc, kCFBooleanTrue, kSecMatchLimitOne};
	CFDictionaryRef q = CFDictionaryCreate(NULL, keys, vals, 5, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFTypeRef result = NULL;
	OSStatus st = SecItemCopyMatching(q, &result);
	CFRelease(q);
	CFRelease(svc);
	CFRelease(acc);
	if (st != errSecSuccess) {
		return st;
	}
	CFDataRef data = (CFDataRef)result;
	long n = CFDataGetLength(data);
	char *buf = malloc(n > 0 ? n : 1);
	memcpy(buf, CFDataGetBytePtr(data), n);
	CFRelease(result);
	*out = buf;
	*outLen = n;
	return errSecSuccess;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// keychainPassword 用 Security 框架读一项通用密码。不调 /usr/bin/security：
// 用户在弹窗里对它点"始终允许"，等于放行所有借它读这一项的程序。
func keychainPassword(service, account string) (string, error) {
	cs, ca := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(ca))
	var out *C.char
	var n C.long
	st := C.hiveKeychainRead(cs, ca, &out, &n)
	switch st {
	case C.errSecSuccess:
	case C.errSecItemNotFound, C.errSecUserCanceled, C.errSecAuthFailed, C.errSecInteractionNotAllowed:
		return "", ErrCursorDenied
	default:
		return "", fmt.Errorf("keychain: OSStatus %d", int(st))
	}
	defer C.free(unsafe.Pointer(out))
	return C.GoStringN(out, C.int(n)), nil
}
