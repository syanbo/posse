package gitx

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestIntegratePreservesWorkspaceMerge(t *testing.T) {
	for _, hasConflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-ff", true: "abort"}[hasConflict], func(t *testing.T) {
			dir := newRepo(t)
			if hasConflict {
				dir = conflict(t)
			} else {
				gitIn(t, dir, "switch", "-c", "side")
				write(t, filepath.Join(dir, "side"), "side")
				gitIn(t, dir, "add", "side")
				gitIn(t, dir, "commit", "-qm", "side")
				gitIn(t, dir, "switch", "main")
			}
			head := gitIn(t, dir, "rev-parse", "HEAD")
			var commands [][]string
			ctx := WithTrace(t.Context(), func(r Run) {
				if r.Done {
					commands = append(commands, slices.Clone(r.Args))
				}
			})
			err := Merge(ctx, dir, "side")
			if len(commands) == 0 || !slices.Equal(commands[0], []string{"merge", "--no-ff", "--no-edit", "side"}) {
				t.Fatal(commands)
			}
			if hasConflict {
				if err == nil || len(commands) != 2 || !slices.Equal(commands[1], []string{"merge", "--abort"}) || gitIn(t, dir, "rev-parse", "HEAD") != head {
					t.Fatal("workspace merge changed", commands, err)
				}
				st, _ := ReadStatus(t.Context(), dir)
				if st.Operation != "" || len(st.Entries) != 0 {
					t.Fatal(st)
				}
			} else if err != nil || len(strings.Fields(gitIn(t, dir, "show", "-s", "--format=%P", "HEAD"))) != 2 {
				t.Fatal("workspace merge fast-forwarded", err)
			}
		})
	}
}
func TestIntegrateBoundedEditorAndOutput(t *testing.T) {
	dir := newRepo(t)
	bin := t.TempDir()
	script := "#!/bin/sh\nif test \"$GIT_EDITOR\" != true; then echo missing-editor >&2; exit 1; fi\nprintf 'authorization: never-record-this\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	var output string
	ctx := WithTrace(t.Context(), func(r Run) {
		if r.Done {
			output += r.Out + r.Err
		}
	})
	if _, err := IntegrateBounded(ctx, dir, []string{"cherry-pick", "--continue"}, "", true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "never-record-this") {
		t.Fatal("unfiltered output", output)
	}
}
