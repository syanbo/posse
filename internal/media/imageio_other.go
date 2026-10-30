//go:build !darwin

package media

import (
	"errors"
	"image"
	"time"
)

// Anim 是一张动图；只有 macOS 上经 ImageIO 解码，别处 OpenAnim 一律当成不是动图。
type Anim struct {
	Size   image.Point
	Frame  image.Point
	Delays []time.Duration
	Loops  int
	Format string
}

var ErrNoImageIO = errors.New("ImageIO unavailable")

func OpenAnim(data []byte, maxW, maxPixels int) (*Anim, error) { return nil, nil }

func (a *Anim) Decode(i int, dst *image.RGBA) error { return ErrNoImageIO }

func (a *Anim) Close() {}

func decodeSystem(data []byte) (image.Image, string, error) { return nil, "", ErrNoImageIO }
