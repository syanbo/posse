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
// nonce 由调用方给：分帧 2 把它拼在帧前面，分帧 3 不上线、两端各自按计数器算。

const nonceLen = 24

// hq_seal 加密 msg，往 out 写 16 字节标签和密文（和 Go 的 secretbox.Seal(nil, …) 同格式），
// out 长度必须是 16+n。
//
//export hq_seal
func hq_seal(key, nonce, msg *C.uint8_t, n C.int32_t, out *C.uint8_t) {
	k, nc := keyNonce(key, nonce)
	dst := unsafe.Slice((*byte)(out), secretbox.Overhead+int(n))
	secretbox.Seal(dst[:0], bytes(msg, n), &nc, &k)
}

// hq_open 解开 box（标签加密文），明文写进 out（长度 n-16），认证失败返回 0。
//
//export hq_open
func hq_open(key, nonce, box *C.uint8_t, n C.int32_t, out *C.uint8_t) C.int32_t {
	if int(n) < secretbox.Overhead {
		return 0
	}
	k, nc := keyNonce(key, nonce)
	dst := unsafe.Slice((*byte)(out), int(n)-secretbox.Overhead)
	if _, ok := secretbox.Open(dst[:0], unsafe.Slice((*byte)(box), int(n)), &nc, &k); !ok {
		return 0
	}
	return 1
}

func keyNonce(key, nonce *C.uint8_t) (k [32]byte, nc [nonceLen]byte) {
	copy(k[:], unsafe.Slice((*byte)(key), 32))
	copy(nc[:], unsafe.Slice((*byte)(nonce), nonceLen))
	return
}

func bytes(p *C.uint8_t, n C.int32_t) []byte {
	if n == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(p), int(n))
}
