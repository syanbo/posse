//go:build darwin

package desk

import (
	"testing"

	"github.com/ebitengine/purego/objc"
)

// 换屏、缩放之后旧流的样本队列在后台排空，期间旧流交来的样本和迟到的停止回调都要认出来丢掉：
// 旧尺寸的帧不能进新编码器，旧流停了也不能把现在的流当成停了。这里不起采集，engine 只有 live，
// 走到要用 mac 的地方就会崩，所以没崩就是在入口丢掉了。
func TestOldStreamCallbacksDropped(t *testing.T) {
	e := &engine{}
	e.live.Store(0x5000)
	e.onSample(0x7000, 0x1234)

	prev := active
	active = e
	defer func() { active = prev }()
	streamDidStop(0, 0, objc.ID(0x7000), 0)
}

// 旧流的样本已经过了入口的核对、正在取像素时，停流、换编码器、开新流：它不能编进新会话（照 Codex 的复现）。
func TestAdmittedOldSampleNotEncodedOnNewSession(t *testing.T) {
	const oldStream, newStream, oldEncoder, newEncoder, oldImage = 1, 2, 11, 22, 111
	entered := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan struct{})
	var encodedOn uintptr
	e := &engine{enc: oldEncoder, pend: map[uint64]*stamp{}}
	e.mac = &macAPI{
		sampleImage: func(uintptr) uintptr {
			close(entered)
			<-resume
			return oldImage
		},
		cfRetain:  func(p uintptr) uintptr { return p },
		cfRelease: func(uintptr) {},
		vtEncode: func(session, img uintptr, _, _ cmTime, _, _ uintptr, _ *uint32) int32 {
			encodedOn = session
			return 0
		},
	}
	e.live.Store(oldStream)
	go func() {
		e.onSample(oldStream, 100)
		close(done)
	}()
	<-entered
	// stopStream 清掉旧流，resizeLocked 换编码器，openStream 发布新流
	e.live.Store(0)
	e.encMu.Lock()
	e.enc = newEncoder
	e.encMu.Unlock()
	e.live.Store(newStream)
	close(resume)
	<-done
	if encodedOn == newEncoder {
		t.Fatalf("old stream sample encoded on the new session %d", encodedOn)
	}
}
