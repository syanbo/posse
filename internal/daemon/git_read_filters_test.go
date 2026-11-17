package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/rpc"
)

func assertFilterNotRun(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("filter executed: %v", err)
	}
}

func TestDesktopGitReadFilters(t *testing.T) {
	for _, kind := range []string{"clean", "process"} {
		for _, submodule := range []bool{false, true} {
			name := kind
			if submodule {
				name += "-submodule"
			}
			t.Run(name, func(t *testing.T) {
				_, c := gitReadClient(t)
				base := t.TempDir()
				root := readGitRepo(t, filepath.Join(base, "root"), true)
				filtered := root
				if submodule {
					source := readGitRepo(t, filepath.Join(base, "source"), true)
					readGit(t, root, "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "module")
					filtered = filepath.Join(root, "module")
				}
				marker := filepath.Join(base, "executed")
				t.Setenv("FILTER_MARKER", marker)
				if err := os.WriteFile(filepath.Join(filtered, ".gitattributes"), []byte("a.txt filter=marker\n"), 0600); err != nil {
					t.Fatal(err)
				}
				readGit(t, filtered, "add", ".gitattributes")
				command := ": > \"$FILTER_MARKER\"; /bin/cat"
				if kind == "process" {
					command = ": > \"$FILTER_MARKER\"; exit 1"
				}
				readGit(t, filtered, "config", "filter.marker."+kind, command)
				// 等长改写迫使 status 比较内容，不能只凭文件大小判断已修改。
				if err := os.WriteFile(filepath.Join(filtered, "a.txt"), []byte("next\n"), 0600); err != nil {
					t.Fatal(err)
				}
				for _, full := range []bool{false, true} {
					_, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: root, Full: full})
					if err == nil || !strings.Contains(err.Error(), "Git content query would execute filter marker") {
						t.Fatalf("filter was not explicitly rejected: %v", err)
					}
					assertFilterNotRun(t, marker)
				}
				if _, err := c.GitBranches(t.Context(), rpc.GitBranchesParams{Path: root}); err != nil {
					t.Fatalf("branches scanned content: %v", err)
				}
				if _, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: root}); err != nil {
					t.Fatalf("discovery scanned content: %v", err)
				}
				assertFilterNotRun(t, marker)
				// 确认夹具确实会触发过滤器，不能用未命中的属性得到假通过。
				cmd := exec.Command("/usr/bin/git", "status", "--porcelain=v2")
				cmd.Dir = filtered
				cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT=0")
				_ = cmd.Run()
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("native status did not execute fixture filter: %v", err)
				}
			})
		}
	}
}

func TestDesktopGitReadUnusedGlobalFilter(t *testing.T) {
	_, c := gitReadClient(t)
	base := t.TempDir()
	root := readGitRepo(t, filepath.Join(base, "repo"), true)
	marker := filepath.Join(base, "executed")
	config := filepath.Join(base, "global.config")
	t.Setenv("FILTER_MARKER", marker)
	readGit(t, root, "config", "--file", config, "filter.lfs.clean", ": > \"$FILTER_MARKER\"; /bin/cat")
	readGit(t, root, "config", "--file", config, "filter.lfs.process", ": > \"$FILTER_MARKER\"; exit 1")
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("modified\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.bin filter=lfs\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// 未跟踪文件只列名称，不应误判成需要内容过滤。
	if err := os.WriteFile(filepath.Join(root, "untracked.bin"), []byte("raw\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: root, Full: true})
	if err != nil || len(st.Status.Entries) != 3 {
		t.Fatalf("unused global filter blocked status: %+v %v", st, err)
	}
	if _, err := c.GitBranches(t.Context(), rpc.GitBranchesParams{Path: root}); err != nil {
		t.Fatal(err)
	}
	assertFilterNotRun(t, marker)
}
