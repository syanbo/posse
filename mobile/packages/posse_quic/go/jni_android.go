//go:build android

package main

import "C"

import "unsafe"

// 安卓 App 的 MainActivity.quicReset，在 Flutter 引擎销毁前调，见 hq_reset。
//
//export Java_com_neolabsapp_posse_mobile_MainActivity_quicReset
func Java_com_neolabsapp_posse_mobile_MainActivity_quicReset(env, cls unsafe.Pointer) {
	hq_reset()
}
