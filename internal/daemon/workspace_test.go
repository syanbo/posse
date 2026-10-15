package daemon

import "testing"

func TestValidName(t *testing.T) {
	for _, ok := range []string{"feat-a", "v1.2", "坠崖十年"} {
		if err := validName(ok); err != nil {
			t.Errorf("validName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "..", "-x", "a/b", "a b", "a:b"} {
		if validName(bad) == nil {
			t.Errorf("validName(%q) accepted", bad)
		}
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{"/tmp", "/private/tmp", true},
		{"/a/b/c", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
	}
	for _, c := range cases {
		if got := within(c.path, c.dir); got != c.want {
			t.Errorf("within(%q, %q) = %v", c.path, c.dir, got)
		}
	}
}
