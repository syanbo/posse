// Package desk 是手机看 Mac 桌面的采集侧。常驻进程拉起同一个可执行文件里的
// 子进程，子进程只采集和编码，帧从管道交出来。
package desk

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
)

const (
	frameMagic      = "HDF1"
	frameHeaderSize = 32

	KindVideo     byte = 1
	KindHeartbeat byte = 2
	KindConfig    byte = 3
	KindStatus    byte = 4

	flagKeyframe byte = 1

	// 状态帧负载的第一个字节。
	StatusLocked          byte = 1
	StatusUnlocked        byte = 2
	StatusNoScreen        byte = 3 // 没有屏幕录制权限，子进程随后退出
	StatusNoAccessibility byte = 4 // 没有辅助功能权限，能看不能操作

	CodecHEVC byte = 1
	CodecH264 byte = 2
)

// Frame 是子进程写给父进程的一帧。心跳没有负载。配置负载是 Fit 之后的画面尺寸。
type Frame struct {
	Kind            byte
	Keyframe        bool
	Number          uint64
	CaptureUnixNano int64
	Payload         []byte
}

// Config 是 KindConfig 负载：编码和输出像素尺寸。
type Config struct {
	Codec         byte
	Width, Height int
}

var (
	errShort   = errors.New("desk: short frame")
	errMagic   = errors.New("desk: bad frame magic")
	errKind    = errors.New("desk: bad frame kind")
	errPayload = errors.New("desk: bad config payload")
)

// WriteFrame 把一帧写成 32 字节头加负载。
func WriteFrame(w io.Writer, f Frame) error {
	var hdr [frameHeaderSize]byte
	copy(hdr[0:4], frameMagic)
	hdr[4] = f.Kind
	if f.Keyframe {
		hdr[5] = flagKeyframe
	}
	binary.LittleEndian.PutUint64(hdr[8:16], f.Number)
	binary.LittleEndian.PutUint64(hdr[16:24], uint64(f.CaptureUnixNano))
	if len(f.Payload) > int(^uint32(0)) {
		return errors.New("desk: frame too large")
	}
	binary.LittleEndian.PutUint32(hdr[24:28], uint32(len(f.Payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(f.Payload)
	return err
}

// ReadFrame 读一帧。io.EOF 只出现在一帧都还没读到的时候。
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [frameHeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return Frame{}, errShort
		}
		return Frame{}, err
	}
	if string(hdr[0:4]) != frameMagic {
		return Frame{}, errMagic
	}
	f := Frame{
		Kind:            hdr[4],
		Keyframe:        hdr[5]&flagKeyframe != 0,
		Number:          binary.LittleEndian.Uint64(hdr[8:16]),
		CaptureUnixNano: int64(binary.LittleEndian.Uint64(hdr[16:24])),
	}
	switch f.Kind {
	case KindVideo, KindHeartbeat, KindConfig, KindStatus:
	default:
		return Frame{}, errKind
	}
	n := binary.LittleEndian.Uint32(hdr[24:28])
	if n > 8<<20 {
		return Frame{}, errors.New("desk: frame payload too large")
	}
	if n > 0 {
		f.Payload = make([]byte, n)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return Frame{}, err
		}
	}
	return f, nil
}

// MarshalConfig 编码配置负载。
func MarshalConfig(c Config) []byte {
	var b [9]byte
	b[0] = c.Codec
	binary.LittleEndian.PutUint32(b[1:5], uint32(c.Width))
	binary.LittleEndian.PutUint32(b[5:9], uint32(c.Height))
	return b[:]
}

// UnmarshalConfig 解开配置负载。
func UnmarshalConfig(b []byte) (Config, error) {
	if len(b) != 9 || (b[0] != CodecHEVC && b[0] != CodecH264) {
		return Config{}, errPayload
	}
	return Config{
		Codec:  b[0],
		Width:  int(binary.LittleEndian.Uint32(b[1:5])),
		Height: int(binary.LittleEndian.Uint32(b[5:9])),
	}, nil
}

const (
	ctrlHeader = 5

	CtrlKeyframe byte = 1
	CtrlBitrate  byte = 2
	CtrlStop     byte = 3
)

// Control 是父进程写给子进程的一条控制。码率只在 CtrlBitrate 时有意义，单位是比特每秒。
type Control struct {
	Kind    byte
	Bitrate uint32
}

// WriteControl 写成 4 字节长度加内容。长度包含类型字节。
func WriteControl(w io.Writer, c Control) error {
	payload := 1
	if c.Kind == CtrlBitrate {
		payload = 5
	}
	buf := make([]byte, 4+payload)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(payload))
	buf[4] = c.Kind
	if c.Kind == CtrlBitrate {
		binary.LittleEndian.PutUint32(buf[5:9], c.Bitrate)
	}
	_, err := w.Write(buf)
	return err
}

// ReadControl 读一条控制。不认识的类型把负载跳过，Kind 原样返回。
func ReadControl(r io.Reader) (Control, error) {
	var nbuf [4]byte
	if _, err := io.ReadFull(r, nbuf[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return Control{}, errShort
		}
		return Control{}, err
	}
	n := binary.LittleEndian.Uint32(nbuf[:])
	if n == 0 || n > 1<<20 {
		return Control{}, errors.New("desk: bad control length")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return Control{}, err
	}
	c := Control{Kind: buf[0]}
	if c.Kind == CtrlBitrate {
		if len(buf) != 5 {
			return Control{}, errors.New("desk: bad bitrate control")
		}
		c.Bitrate = binary.LittleEndian.Uint32(buf[1:5])
	}
	return c, nil
}

// Fit 把宽高缩到长边不超过 longEdge，并取偶数，比例保持。longEdge 本身也会被取成偶数。
func Fit(w, h, longEdge int) (int, int) {
	if w < 2 || h < 2 {
		return 2, 2
	}
	if longEdge < 2 {
		longEdge = 2
	}
	longEdge -= longEdge % 2
	long := w
	if h > long {
		long = h
	}
	scale := 1.0
	if long > longEdge {
		scale = float64(longEdge) / float64(long)
	}
	return evenDim(w, scale), evenDim(h, scale)
}

func PictureBitrate(w, h int) uint32 {
	bits := float64(w*h) * 60 * 0.06
	if bits < rateFloor {
		bits = rateFloor
	}
	if bits > rateCeil {
		bits = rateCeil
	}
	return uint32(bits)
}

func evenDim(n int, scale float64) int {
	v := int(math.Round(float64(n) * scale))
	if v < 2 {
		return 2
	}
	return v - v%2
}
