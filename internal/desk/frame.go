// Package desk 是手机看 Mac 桌面的采集侧。常驻进程拉起同一个可执行文件里的
// 子进程，子进程只采集和编码，帧从管道交出来。
package desk

import (
	"encoding/binary"
	"errors"
	"fmt"
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
	KindDisplays  byte = 5 // 负载是 Displays 的 JSON：开始时、切屏后、拔插或改分辨率后各发一次
	KindWindows   byte = 6 // 负载是 Windows 的 JSON：回 CtrlWindows

	flagKeyframe byte = 1

	// 状态帧负载的第一个字节。
	StatusLocked          byte = 1
	StatusUnlocked        byte = 2
	StatusNoScreen        byte = 3 // 没有屏幕录制权限，子进程随后退出
	StatusNoAccessibility byte = 4 // 没有辅助功能权限，能看不能操作
	StatusAccessibility   byte = 5 // 看的过程中用户打开了辅助功能，可以操作了
	StatusRestarted       byte = 6 // 常驻进程重新拉起了子进程，后面是新子进程的帧。由常驻进程插进来，子进程不发
	StatusPasswordTyped   byte = 7 // 一条密码事件整串投给了锁屏、按了回车；丢掉或中途停下的不报

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

// Config 是 KindConfig 负载：编码和输出像素尺寸。FullWidth、FullHeight 是不降分辨率时的尺寸
// （CtrlScale 降了一半时 Width、Height 是它的一半），旧子进程不报，读出来和 Width、Height 一样。
type Config struct {
	Codec                 byte
	Width, Height         int
	FullWidth, FullHeight int
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
	case KindVideo, KindHeartbeat, KindConfig, KindStatus, KindDisplays, KindWindows:
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

// Display 是 Mac 上的一块屏。位置和大小是全局坐标里的点（主屏左上角是原点），排布图照它画；
// 像素是这块屏现在的分辨率。
type Display struct {
	ID          uint32  `json:"id"`
	Name        string  `json:"name"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"w"`
	Height      float64 `json:"h"`
	PixelWidth  int     `json:"pw"`
	PixelHeight int     `json:"ph"`
	Main        bool    `json:"main,omitempty"`
}

// Displays 是 KindDisplays 负载：所有的屏和正在看的那块。只看一个窗口时 Window 是那个窗口，
// Current 是它所在的屏；Gone 是正在看的窗口关掉或最小化了，退回了整块屏。
type Displays struct {
	Current uint32    `json:"current"`
	List    []Display `json:"displays"`
	Window  *Window   `json:"window,omitempty"`
	Gone    bool      `json:"gone,omitempty"`
}

// Window 是 Mac 上一个可以单独看的窗口。宽高是点数，Display 是它中心所在的屏。
type Window struct {
	ID      uint32  `json:"id"`
	App     string  `json:"app"`
	Title   string  `json:"title,omitempty"`
	Width   float64 `json:"w,omitempty"`
	Height  float64 `json:"h,omitempty"`
	Display uint32  `json:"display,omitempty"`
	// Icon 是 App 图标在 Windows.Icons 里的键，取不到图标时为空
	Icon string `json:"icon,omitempty"`
}

// Windows 是 KindWindows 负载：当前空间里看得见的窗口，最前的在前面。Icons 是各 App 的图标（PNG），
// 同一个 App 的几个窗口共用一张。
type Windows struct {
	List  []Window          `json:"windows"`
	Icons map[string][]byte `json:"icons,omitempty"`
}

// MarshalConfig 编码配置负载。
func MarshalConfig(c Config) []byte {
	if c.FullWidth == 0 || c.FullHeight == 0 {
		c.FullWidth, c.FullHeight = c.Width, c.Height
	}
	var b [17]byte
	b[0] = c.Codec
	binary.LittleEndian.PutUint32(b[1:5], uint32(c.Width))
	binary.LittleEndian.PutUint32(b[5:9], uint32(c.Height))
	binary.LittleEndian.PutUint32(b[9:13], uint32(c.FullWidth))
	binary.LittleEndian.PutUint32(b[13:17], uint32(c.FullHeight))
	return b[:]
}

// UnmarshalConfig 解开配置负载。
func UnmarshalConfig(b []byte) (Config, error) {
	if (len(b) != 9 && len(b) != 17) || (b[0] != CodecHEVC && b[0] != CodecH264) {
		return Config{}, errPayload
	}
	c := Config{
		Codec:  b[0],
		Width:  int(binary.LittleEndian.Uint32(b[1:5])),
		Height: int(binary.LittleEndian.Uint32(b[5:9])),
	}
	c.FullWidth, c.FullHeight = c.Width, c.Height
	if len(b) == 17 {
		c.FullWidth = int(binary.LittleEndian.Uint32(b[9:13]))
		c.FullHeight = int(binary.LittleEndian.Uint32(b[13:17]))
	}
	return c, nil
}

const (
	CtrlKeyframe byte = 1
	CtrlBitrate  byte = 2
	// 3 以前是停止（CtrlStop），父进程从来不发、子进程靠控制管道关闭停下：别再挪作他用
	CtrlInput   byte = 4
	CtrlDisplay byte = 5
	CtrlFPS     byte = 6
	CtrlScale   byte = 7 // 画面长边除以几：1 原样，2 减半（码率低到 15 帧及以下时，关键帧小得多）
	CtrlWindows byte = 8 // 要窗口列表，子进程回一帧 KindWindows
	CtrlWindow  byte = 9 // 只看这个窗口（CGWindowID）；CtrlDisplay 回到整块屏
)

// Control 是父进程写给子进程的一条控制。码率只在 CtrlBitrate 时有意义，单位是比特每秒。
// Events 只在 CtrlInput 时有意义，是去重之后要执行的事件。Display 只在 CtrlDisplay 时有意义，是要切到的屏。
// FPS 只在 CtrlFPS 时有意义，是每秒最多采几帧。Scale 只在 CtrlScale 时有意义，是长边除以几。
// Window 只在 CtrlWindow 时有意义，是要看的窗口。
type Control struct {
	Kind    byte
	Bitrate uint32
	Events  []Event
	Display uint32
	FPS     uint32
	Scale   uint32
	Window  uint32
}

// WriteControl 写成 4 字节长度加内容。长度包含类型字节。
func WriteControl(w io.Writer, c Control) error {
	var body []byte
	switch c.Kind {
	case CtrlBitrate:
		body = controlU32(c.Kind, c.Bitrate)
	case CtrlDisplay:
		body = controlU32(c.Kind, c.Display)
	case CtrlFPS:
		body = controlU32(c.Kind, c.FPS)
	case CtrlScale:
		body = controlU32(c.Kind, c.Scale)
	case CtrlWindow:
		body = controlU32(c.Kind, c.Window)
	case CtrlInput:
		ev, err := EncodeInput(c.Events)
		if err != nil {
			return err
		}
		body = make([]byte, 1+len(ev))
		body[0] = c.Kind
		copy(body[1:], ev)
	default:
		body = []byte{c.Kind}
	}
	buf := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(buf[:4], uint32(len(body)))
	copy(buf[4:], body)
	_, err := w.Write(buf)
	return err
}

// controlU32 是只带一个 u32 的控制：类型字节加小端的值。
func controlU32(kind byte, v uint32) []byte {
	body := make([]byte, 5)
	body[0] = kind
	binary.LittleEndian.PutUint32(body[1:], v)
	return body
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
	switch c.Kind {
	case CtrlBitrate, CtrlDisplay, CtrlFPS, CtrlScale, CtrlWindow:
		if len(buf) != 5 {
			return Control{}, fmt.Errorf("desk: control %d has %d bytes, want 5", c.Kind, len(buf))
		}
		v := binary.LittleEndian.Uint32(buf[1:5])
		switch c.Kind {
		case CtrlBitrate:
			c.Bitrate = v
		case CtrlDisplay:
			c.Display = v
		case CtrlFPS:
			c.FPS = v
		case CtrlWindow:
			c.Window = v
		default:
			c.Scale = v
		}
	case CtrlInput:
		ev, err := DecodeInput(buf[1:])
		if err != nil {
			return Control{}, err
		}
		c.Events = ev
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

// DirectBitrate 是直连时的起始码率，照 Moonlight 的默认值（getDefaultBitrate）：按像素数在
// 360p、480p、720p、1080p、1440p、4K 对应 1、2、5、10、20、40 Mbps（30 帧）之间插值，60 帧翻倍。
// PictureBitrate 那档在直连下太省：1920×1246 只给 8.6 Mbps，实测文字一滚动就糊（亮度 PSNR 37 dB），
// 20 Mbps 时 48 dB，编码器实际只用了一半左右。
func DirectBitrate(w, h int) uint32 {
	table := []struct{ pixels, mbps float64 }{
		{640 * 360, 1}, {854 * 480, 2}, {1280 * 720, 5}, {1920 * 1080, 10}, {2560 * 1440, 20}, {3840 * 2160, 40},
	}
	p := float64(w * h)
	factor := table[len(table)-1].mbps
	for i, row := range table {
		if p <= row.pixels {
			factor = row.mbps
			if i > 0 {
				prev := table[i-1]
				factor = prev.mbps + (p-prev.pixels)/(row.pixels-prev.pixels)*(row.mbps-prev.mbps)
			}
			break
		}
	}
	bits := factor * 2 * 1_000_000
	return uint32(min(max(bits, float64(PictureBitrate(w, h))), rateCeil))
}

// startFloor 是起始码率的下限：小画面在好网络上也从它起步、按 60 帧采，差网络再往 rateFloor 降。
const startFloor = 1_500_000

func PictureBitrate(w, h int) uint32 {
	bits := float64(w*h) * 60 * 0.06
	if bits < startFloor {
		bits = startFloor
	}
	if bits > relayCeil {
		bits = relayCeil
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
