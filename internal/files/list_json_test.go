package files

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListJSONIgnoredRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		repos  []string
		inRepo bool
	}{
		{"single", nil, false},
		{"multi", []string{"repo", "alias"}, false},
		{"nested", []string{"repo", "alias"}, true},
		{"empty", []string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "absent-target")
			l := &List{Root: root, Files: []string{"alias/src/a.go", "repo/src/a.go"}, Git: true, repos: tc.repos, inRepo: tc.inRepo,
				dirs: map[string]bool{"alias": true, "alias/src": true, "repo": true, "repo/src": true}}
			data, err := json.Marshal(*l)
			if err != nil {
				t.Fatal(err)
			}
			var got List
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(l, &got) {
				t.Fatalf("round trip: %#v != %#v", l, &got)
			}
			for _, check := range []struct {
				path string
				dir  bool
				want bool
			}{
				{"repo/src", true, false}, {"alias/src", true, false},
				{"repo", true, false}, {"repo/src/a.go", false, false},
				{"alias/ignored.log", false, tc.repos == nil || len(tc.repos) > 0},
				{"outside/missing.log", false, tc.repos == nil || tc.inRepo},
			} {
				if v := got.Ignored(filepath.Join(root, check.path), check.dir); v != check.want {
					t.Errorf("Ignored(%s, %v) = %v, want %v", check.path, check.dir, v, check.want)
				}
			}
			got.Truncated = true
			data, err = json.Marshal(&got)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &got); err != nil || got.Ignored(filepath.Join(root, "alias/ignored.log"), false) {
				t.Fatalf("truncated list ignored: %v", err)
			}
		})
	}
}
