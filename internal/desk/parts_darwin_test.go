//go:build darwin

package desk

import (
	"testing"
	"unsafe"
)

// 放掉采集流用过的东西：等样本队列排空、放输出对象和队列，不需要屏幕录制权限。
func TestReleaseStreamParts(t *testing.T) {
	m, err := loadMac()
	if err != nil {
		t.Fatal(err)
	}
	if err := registerDelegate(m); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		del := m.init(m.alloc(delegateClass, m.sel("alloc")), m.sel("init"))
		name := append([]byte("posse.desk.test"), 0)
		queue := m.queueCreate(unsafe.Pointer(&name[0]), 0)
		if del == 0 || queue == 0 {
			t.Fatal("could not create the delegate or queue")
		}
		m.releaseParts(streamParts{del: del, queue: queue})
	}
}
