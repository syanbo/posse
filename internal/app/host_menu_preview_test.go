package app

import (
	"fmt"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	"posse/internal/client"
	"posse/internal/rpc"
)

func TestPreviewHostMenu(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		for _, state := range []string{"local", "viewing", "controlled", "disconnected"} {
			t.Run(fmt.Sprintf("%s-%t", state, dark), func(t *testing.T) {
				home := t.TempDir()
				local := newHostApp(&client.Client{Home: home}, home)
				v := newHostViews(local)
				a := local
				if state != "local" {
					a = v.forHost(newHostSession("office", "Posse"), &client.Client{Remote: true})
					v.active.Store(a)
					v.target = a
				} else {
					a.host = newHostSession(localHostID, "Posse")
				}
				h := remoteTestHost("office")
				h.Name = "Office Mac"
				writeHostMenuConfig(t, local, h)
				a.loadHostMenu()
				a.loaded = true
				s := rpc.DesktopState{InstanceID: "preview", Epoch: 1}
				controlled := state == "controlled" || state == "local"
				if controlled {
					s.Owner, s.Label = a.host.controllerID, "Posse"
				} else if state == "viewing" {
					s.Owner, s.Label = "another-window", "Studio Mac"
				}
				a.host.current = hostSnapshot{HostID: a.viewID, Desktop: s, Connected: state != "disconnected", Controlled: controlled}
				tt := newTester(v.build, 1000, 640)
				tt.SetTitleBar(ui.TitleBar{Height: 40, Left: 70})
				tt.SetDark(dark)
				tt.SetScale(2)
				settleMenu(tt)
				a.openHostMenu()
				settleMenu(tt)
				suffix := "light"
				if dark {
					suffix = "dark"
				}
				savePNG(t, tt, "host-menu-"+state+"-"+suffix+".png")
			})
		}
	}
}
