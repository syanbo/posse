package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/cheatsheet"
	"posse/internal/rpc"
)

func TestRemoteCheatsheetNeverReadsLocalSkills(t *testing.T) {
	local := t.TempDir()
	file := filepath.Join(local, ".agents", "skills", "local-only", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("---\nname: local-only\ndescription: Local skill\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, supports := range []bool{true, false} {
		t.Run(fmt.Sprintf("supported=%v", supports), func(t *testing.T) {
			a, f := requestTestApp(t, "remote-cheatsheet")
			cur := terminalInfo{ID: "t_remote", Agent: "antigravity", Running: true, AntigravityDirs: []string{local}}
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				var args map[string]string
				if p.Method != "terminal.cheatsheet" || json.Unmarshal(p.Params, &args) != nil || len(args) != 2 || args["terminal"] != cur.ID || args["agent"] != cur.Agent {
					t.Errorf("unexpected desktop read: %s %s", p.Method, p.Params)
				}
				if !supports {
					return nil, rpc.Errorf(rpc.CodeUnknown, "unknown method")
				}
				return cheatsheet.TerminalInfo{Skills: []cheatsheet.Skill{{Name: "target-only"}}}, nil
			}
			f.mu.Unlock()
			tt := newTester(func(c *ui.Context) { a.pollCheat(c, &cur) }, 320, 320)
			tt.Frame()
			for deadline := time.Now().Add(2 * time.Second); !a.cheat.loaded && time.Now().Before(deadline); {
				a.runPending()
				time.Sleep(time.Millisecond)
			}
			got := a.cheat.skills
			if !a.cheat.loaded || supports && (len(got) != 1 || got[0].Name != "target-only") || !supports && len(got) != 0 {
				t.Fatalf("remote cheatsheet loaded=%v, skills=%+v", a.cheat.loaded, got)
			}
		})
	}
}
