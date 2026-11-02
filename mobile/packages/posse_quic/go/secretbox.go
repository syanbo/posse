package main

/*
#include <stdint.h>
*/
import "C"

import (
	"unsafe"

	"golang.org/x/crypto/nacl/secretbox"
)

// 手机端 E2EE 的 secretbox（XSalsa20-Poly1305）。Dart 侧的纯 Dart TweetNaCl 在 1 MiB 上比这里慢约 5 倍。
// 缓冲区都由 Dart 分配、按调用传入，Go 不保留指针，直接在 out 里写结果，不额外分配。

const nonceLen = 24

// hq_seal 加密 msg，往 out 写 nonce 加密文（和 Go 的 secretbox.Seal(nonce[:], …) 同格式），
// out 长度必须是 24+16+n。
//
//export hq_seal
func hq_seal(key, nonce, msg *C.uint8_t, n C.int32_t, out *C.uint8_t) {
	var k [32]byte
	var nc [nonceLen]byte
	copy(k[:], unsafe.Slice((*byte)(key), 32))
	copy(nc[:], unsafe.Slice((*byte)(nonce), nonceLen))
	dst := unsafe.Slice((*byte)(out), nonceLen+secretbox.Overhead+int(n))
	copy(dst, nc[:])
	secretbox.Seal(dst[:nonceLen], bytes(msg, n), &nc, &k)
}

// hq_open 解开 frame（nonce 加密文），明文写进 out（长度 n-24-16），认证失败返回 0。
//
//export hq_open
func hq_open(key, frame *C.uint8_t, n C.int32_t, out *C.uint8_t) C.int32_t {
	if int(n) < nonceLen+secretbox.Overhead {
		return 0
	}
	var k [32]byte
	var nc [nonceLen]byte
	copy(k[:], unsafe.Slice((*byte)(key), 32))
	f := unsafe.Slice((*byte)(frame), int(n))
	copy(nc[:], f[:nonceLen])
	dst := unsafe.Slice((*byte)(out), int(n)-nonceLen-secretbox.Overhead)
	if _, ok := secretbox.Open(dst[:0], f[nonceLen:], &nc, &k); !ok {
		return 0
	}
	return 1
}

func bytes(p *C.uint8_t, n C.int32_t) []byte {
	if n == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(p), int(n))
}
