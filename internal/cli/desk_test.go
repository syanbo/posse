package cli

import (
	"testing"

	"posse/internal/desk"
)

func TestDeskResolutionHelperArgs(t *testing.T) {
	opts, err := deskFlags([]string{"--long-edge", "800", "--resolution", "1440", "--display", "7"})
	if err != nil {
		t.Fatal(err)
	}
	helper, err := deskFlags(opts.helperArgs())
	if err != nil {
		t.Fatal(err)
	}
	w, h := desk.FitResolution(3840, 2160, helper.longEdge, desk.PinnedResolution(int64(helper.resolution)), 1)
	if w != 2560 || h != 1440 || helper.display != 7 {
		t.Fatalf("helper %dx%d display %d from %v", w, h, helper.display, opts.helperArgs())
	}
}
