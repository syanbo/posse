package history

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCLIPrograms(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{"git status --short && ls internal | head", []string{"git"}},
		{"cd /x; GOPROXY=https://goproxy.cn go build ./... 2>&1 | tail -5", []string{"go"}},
		{`rg -n "foo | jq" src; echo 'gh pr list'`, []string{"rg"}},
		{"command -v hive; hive --help", []string{"hive"}},
		{"env -u HIVE_TERMINAL_ID HIVE_HOME=/tmp/h hive project add . && /tmp/x/launch.sh && /usr/bin/codesign -v a", []string{"hive", "codesign"}},
		{"sudo -u root launchctl list && timeout 10 adb -s x install a.apk", []string{"launchctl", "adb"}},
		{"time nohup xargs -n 1 gofmt -l < files >out.txt", []string{"gofmt"}},
		{"x=$(git rev-parse HEAD) && echo `brew --prefix`", []string{"git", "brew"}},
		{"for f in *.go; do gofmt -l $f; done; if [ -f a ]; then make; fi", []string{"gofmt", "make"}},
		{"cat <<'EOF' > a.sh\nnpm install\ncurl x\nEOF\nchmod +x a.sh && ./a.sh && bin/hive x", nil},
		{"python3 - <<-END\n\tgit push\n\tEND\nfvm flutter test", []string{"python3", "fvm"}},
		{`bash -lc 'jq . a.json' && zsh -c "gh run list"`, []string{"jq", "gh"}},
		{"case $x in a) brew list;; b) uv run t;; esac; go vet", []string{"brew", "uv", "go"}},
		{"echo $((1+2)) >&2; $GO build; \"$HOME/bin/x\" run", nil},
		{"ls \\\n  -la && git log # npm test", []string{"git"}},
		{"(cd mobile && fvm flutter build apk) || adb devices", []string{"fvm", "adb"}},
		{"go test ./... && go vet ./...", []string{"go"}},
		// 命令里定义的函数
		{"H() { env -u X \"$D/hive\" \"$@\"; }; H project show; mk(){ ffmpeg -i \"$1\"; }\nmk a.png; function e { npx sentry; }; e -q x", []string{"ffmpeg", "npx"}},
		// 进程替换、双引号里套着引号的 $( )
		{"diff <(git show HEAD:a) b", []string{"git"}},
		{`curl -o a "https://x/$(S=$(cat f); bsk evaluate --session $S "r.querySelectorAll('a')[0]")" && gh run list`, []string{"curl", "bsk", "gh"}},
	}
	for _, c := range cases {
		var got []string
		for _, p := range cliPrograms(c.cmd) {
			got = append(got, p.Name)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q\n got %q\nwant %q", c.cmd, got, c.want)
		}
	}
}

func TestCLISource(t *testing.T) {
	root := t.TempDir()
	brew, link, cellar, sys := filepath.Join(root, "brew"), filepath.Join(root, "local"), filepath.Join(root, "Cellar", "fd", "1", "bin"), filepath.Join(root, "usr-bin")
	for _, d := range []string{brew, link, cellar, sys} {
		os.MkdirAll(d, 0o755)
	}
	touch := func(p string) { os.WriteFile(p, nil, 0o755) }
	touch(filepath.Join(brew, "rg"))
	touch(filepath.Join(brew, "python3"))
	touch(filepath.Join(sys, "python3"))
	touch(filepath.Join(sys, "git"))
	touch(filepath.Join(cellar, "fd"))
	touch(filepath.Join(link, "other"))
	os.Symlink(filepath.Join(cellar, "fd"), filepath.Join(link, "fd"))
	defer func(b, l, s []string) { brewDirs, brewLinks, systemDirs = b, l, s }(brewDirs, brewLinks, systemDirs)
	brewDirs, brewLinks, systemDirs = []string{brew}, []string{link}, []string{sys}

	for _, c := range []struct{ name, path, want string }{
		{"rg", "", SourceHomebrew},
		{"python3", "", SourceHomebrew}, // Homebrew 的排在系统前面
		{"git", "", SourceSystem},
		{"fd", "", SourceHomebrew},
		{"other", "", SourceOther},
		{"bsk", "", SourceOther},
		{"go", "/opt/homebrew/bin/go", SourceHomebrew},
		{"git", filepath.Join(sys, "git"), SourceSystem},
		{"hive", "/tmp/hive-hl/hive", SourceOther},
	} {
		if got := cliSource(c.name, c.path); got != c.want {
			t.Errorf("cliSource(%q, %q) = %q, want %q", c.name, c.path, got, c.want)
		}
	}
}
