package app

import (
	"os"
	"testing"

	"github.com/egoist/mygo/ui"
	"posse/internal/gitx"
)

func TestLogFilterRepo(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := &app{}
		v := &filesView{root: "/workspace"}
		first := &gitState{v: v, repo: "/workspace/app_av4"}
		second := &gitState{v: v, repo: "/workspace/app_lyg_1001"}
		v.git, v.repos.list = second, []*gitState{first, second}
		d := &fileDoc{log: &logDoc{root: first.repo, filter: gitx.LogFilter{Branches: []string{"fix_710_chatroom_stay"}}}}
		tt := newTester(func(c *ui.Context) {
			c.SetTheme(posseTheme(c.Theme().Dark))
			a.logFilterBar(c, v, d)
		}, 1000, 80)
		tt.SetDark(dark)
		tt.SetScale(2)
		tt.Frame()
		if _, ok := tt.Find("app_av4"); !ok {
			t.Fatal("log must show its own repository, not the active file repository")
		}
		if os.Getenv("POSSE_PREVIEW_DIR") != "" {
			suffix := "light"
			if dark {
				suffix = "dark"
			}
			savePNG(t, tt, "log-repo-"+suffix+".png")
		}
		d.log.root = second.repo
		tt.Frame()
		if _, ok := tt.Find("app_lyg_1001"); !ok {
			t.Fatal("repository label did not follow log root")
		}
		v.repos.list = []*gitState{second}
		tt.Frame()
		if _, ok := tt.Find("app_lyg_1001"); ok {
			t.Fatal("single repository should not add a repository label")
		}
	}
}
