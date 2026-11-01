package history

import (
	"slices"
	"testing"
)

func TestWriteTargets(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{`go test ./... > /tmp/out.log 2>&1`, []string{"/tmp/out.log"}},
		{`cat < /etc/hosts | grep x`, nil},
		{`mkdir -p ~/Downloads/ddt-apk && cp a.apk ~/Downloads/ddt-apk/`, []string{"~/Downloads/ddt-apk", "~/Downloads/ddt-apk/"}},
		{`cd /Users/x/Desktop && mkdir cl-snap`, []string{"/Users/x/Desktop/cl-snap"}},
		{`curl -sL -o /tmp/a.zip https://x && unzip -q /tmp/a.zip -d ~/tools/a`, []string{"/tmp/a.zip", "~/tools/a"}},
		{`sudo tee /etc/x.conf <<EOF
a
EOF`, []string{"/etc/x.conf"}},
		{`adb -s abc pull /sdcard/a.png /Users/x/Downloads/a.png`, []string{"/Users/x/Downloads/a.png"}},
		{`bash -lc 'echo hi > /Users/x/b.tmp'`, []string{"/Users/x/b.tmp"}},
		{`echo $(touch /Users/x/c)`, []string{"/Users/x/c"}},
		{`grep -o foo bar.txt`, nil},
		{`ls >/dev/null`, nil},
		{`go build -o bin/posse ./cmd/posse`, []string{"bin/posse"}},
	}
	for _, c := range cases {
		if got := WriteTargets(c.cmd); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.cmd, got, c.want)
		}
	}
}
