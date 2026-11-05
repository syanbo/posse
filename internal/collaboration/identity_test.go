package collaboration

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestIdentityStable(t *testing.T) {
	home := t.TempDir()
	if _, err := Load(home); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("load before create: %v", err)
	}
	a, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ID) != 64 {
		t.Fatalf("fingerprint %q", a.ID)
	}
	st, err := os.Stat(filepath.Join(home, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	b, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("identity changed: %s → %s", a.ID, b.ID)
	}
	fp, _ := Fingerprint(b.Public)
	if fp != a.ID {
		t.Fatal("fingerprint mismatch")
	}
}

// 并发首次创建只留下一份身份，所有调用方拿到同一个 ID。
func TestIdentityConcurrentCreate(t *testing.T) {
	home := t.TempDir()
	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			id, err := LoadOrCreate(home)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = id.ID
		})
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("different identities: %v", ids)
		}
	}
}

func TestRoomKey(t *testing.T) {
	id := NewDiscussionID()
	if len(id) != 2+32 || !strings.HasPrefix(id, "d_") {
		t.Fatalf("discussion id %q", id)
	}
	k := RoomKey("abc", id)
	h, d, ok := SplitRoomKey(k)
	if !ok || h != "abc" || d != id {
		t.Fatalf("split %q: %q %q %v", k, h, d, ok)
	}
	if _, _, ok := SplitRoomKey("local"); ok {
		t.Fatal("split without slash")
	}
}
