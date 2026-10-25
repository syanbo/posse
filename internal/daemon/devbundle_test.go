package daemon

import "testing"

func TestDevBundleHome(t *testing.T) {
	if !isDevBundle("/x/build/dev/PosseDev.app/Contents/MacOS/Posse") {
		t.Fatal("dev bundle not recognized")
	}
	for _, exe := range []string{"/x/build/darwin-arm64/Posse.app/Contents/MacOS/Posse", "/x/bin/posse", "/x/PosseDev.app.new/Contents/MacOS/Posse"} {
		if isDevBundle(exe) {
			t.Fatalf("%s taken for the dev bundle", exe)
		}
	}
	for _, c := range []struct {
		env  string
		dev  bool
		want string
	}{
		{"", false, "/Users/a/.posse"},
		{"/tmp/e2e", false, "/tmp/e2e"},
		{"", true, "/Users/a/.posse-dev"},
		// 在 Posse 的终端里运行开发包，继承来的日常目录不算
		{"/Users/a/.posse/", true, "/Users/a/.posse-dev"},
		{"/tmp/e2e", true, "/tmp/e2e"},
	} {
		if got := resolveHome(c.env, "/Users/a", c.dev); got != c.want {
			t.Errorf("resolveHome(%q, dev=%v) = %s, want %s", c.env, c.dev, got, c.want)
		}
	}
}
