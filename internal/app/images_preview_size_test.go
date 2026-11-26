package app

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestImagePreviewWidthBalancedWideAndTall(t *testing.T) {
	// 1440×860 视口下，宽图应能用到最大 960pt（不再被卡在 544pt），高图按高适配
	wideW := imagePreviewWidth(1920, 1080, 1920, 1440, 860, hoverPreviewMeta)
	if wideW < 900 || wideW > 960 {
		t.Fatalf("wide image preview width = %.1f, want ~960", wideW)
	}
	tallW := imagePreviewWidth(1000, 1800, 1000, 1440, 860, hoverPreviewMeta)
	tallH := tallW * 1800 / 1000
	if tallH > 860-32-hoverPreviewMeta+0.5 {
		t.Fatalf("tall image preview height %.1f overflows available height", tallH)
	}
	// 小图不强行拉爆到 960pt
	smallW := imagePreviewWidth(200, 200, 200, 1440, 860, hoverPreviewMeta)
	if smallW > imagePreviewMinCap+0.5 {
		t.Fatalf("small image preview width = %.1f, should not exceed %d", smallW, imagePreviewMinCap)
	}
	// 窄窗下不超出左侧可用宽度；空间不足 80pt 时隐藏预览返回 0
	narrowW := imagePreviewWidth(1920, 1080, 1920, 900, 700, hoverPreviewMeta)
	maxAvail := float32(900 - 8 - (8 + panelWidth + 8) - 16)
	if narrowW > maxAvail+0.5 || narrowW <= 0 {
		t.Fatalf("narrow window width %.1f out of range (0, %.1f]", narrowW, maxAvail)
	}
	if tiny := imagePreviewWidth(1920, 1080, 1920, 360, 700, hoverPreviewMeta); tiny != 0 {
		t.Fatalf("too-narrow content area should hide preview (0), got %.1f", tiny)
	}
}

func TestImageBytesInfo(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 320, 180))); err != nil {
		t.Fatal(err)
	}
	info := imageBytesInfo(buf.Bytes())
	if info.w != 320 || info.h != 180 || info.format != "png" || info.bytes != int64(buf.Len()) {
		t.Fatalf("unexpected imageBytesInfo: %+v", info)
	}
	if got := imageMetaText(info); got == "" {
		t.Fatalf("expected non-empty imageMetaText")
	}
}
