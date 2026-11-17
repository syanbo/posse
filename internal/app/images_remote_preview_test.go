package app

import (
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/media"
)

func TestPreviewRemoteImages(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		x := newRemoteMediaFixture(t)
		runImagesPoll(t, x.a, x.cur)
		target := x.a.images.target
		v := previewImages()
		v.term, v.target, v.found = x.cur.ID, target, true
		v.poll.at = time.Now().Add(time.Hour)
		v.made = append(v.made, imageItem{Item: media.Item{Key: "target-video", Label: "screen-recording.mp4", Path: "/target/screen-recording.mp4", Video: true}, thumb: v.made[1].thumb})
		x.a.images = v
		tt := newTester(func(c *ui.Context) {
			c.SetTheme(posseTheme(c.Theme().Dark))
			ui.Box(c).Grow(1).Background(colors(c.Theme()).canvas).Children(func() { x.a.imagesPanel(c, x.cur) })
		}, 320, 900)
		tt.SetScale(2)
		tt.SetDark(dark)
		for range 3 {
			tt.Frame()
			pass(tt, 80*time.Millisecond)
		}
		tt.Frame()
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, tt, "remote-images-"+suffix+".png")
	}
}
