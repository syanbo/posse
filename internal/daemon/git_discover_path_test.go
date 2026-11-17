package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"posse/internal/rpc"
)

func TestDesktopGitDiscoverRealPath(t *testing.T) {
	_, c := gitReadClient(t)
	base := t.TempDir()
	root := readGitRepo(t, filepath.Join(base, "repo"), true)
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-sub")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(base, "plain")
	if err := os.Mkdir(plain, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, real string
		repos            int
	}{
		{"subdirectory-link", link, sub, 1},
		{"parent-after-link", link + "/..", root, 1},
		{"nonrepository", plain, plain, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: tc.path, SingleRepo: true})
			if err != nil {
				t.Fatal(err)
			}
			real, err := filepath.EvalSymlinks(tc.real)
			if err != nil {
				t.Fatal(err)
			}
			if out.RealPath != real || len(out.Repos) != tc.repos {
				t.Fatalf("discovery: %+v, want path %q and %d repos", out, real, tc.repos)
			}
			if tc.name == "subdirectory-link" && out.RealPath == out.Repos[0].Real {
				t.Fatal("workspace subdirectory replaced by repository root")
			}
		})
	}
}
