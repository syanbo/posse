package daemon

import "testing"

func TestDevBundleHome(t *testing.T) {
	if !isDevBundle("/x/build/dev/HiveDev.app/Contents/MacOS/Hive") {
		t.Fatal("dev bundle not recognized")
	}
	for _, exe := range []string{"/x/build/darwin-arm64/Hive.app/Contents/MacOS/Hive", "/x/bin/hive", "/x/HiveDev.app.new/Contents/MacOS/Hive"} {
		if isDevBundle(exe) {
			t.Fatalf("%s taken for the dev bundle", exe)
		}
	}
	for _, c := range []struct {
		env  string
		dev  bool
		want string
	}{
		{"", false, "/Users/a/.hive"},
		{"/tmp/e2e", false, "/tmp/e2e"},
		{"", true, "/Users/a/.hive-dev"},
		// 在 Hive 的终端里运行开发包，继承来的日常目录不算
		{"/Users/a/.hive/", true, "/Users/a/.hive-dev"},
		{"/tmp/e2e", true, "/tmp/e2e"},
	} {
		if got := resolveHome(c.env, "/Users/a", c.dev); got != c.want {
			t.Errorf("resolveHome(%q, dev=%v) = %s, want %s", c.env, c.dev, got, c.want)
		}
	}
}
