package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadBudgetShared(t *testing.T) {
	ctx := WithReadLimit(t.Context(), 9)
	for i := 0; i < 3; i++ {
		out, err := runRaw(ctx, t.TempDir(), nil, nil, "-c", "alias.emit=!printf 12345", "emit")
		if i == 0 && (err != nil || string(out) != "12345") {
			t.Fatalf("first output: %q %v", out, err)
		}
		if i > 0 && !errors.Is(err, ErrReadLimit) {
			t.Fatalf("budget reset for command %d: %v", i, err)
		}
	}
	ctx = WithReadLimit(t.Context(), 9)
	_, err := runRaw(ctx, t.TempDir(), nil, nil, "-c", "alias.emit=!printf 12345; printf 67890 >&2", "emit")
	if !errors.Is(err, ErrReadLimit) {
		t.Fatalf("stdout/stderr budget split: %v", err)
	}
}

func TestReadBudgetCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := runRaw(WithReadLimit(ctx, 1024), t.TempDir(), nil, nil, "status")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query: %v", err)
	}
}

func TestDiscoverCheckedCompatible(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	initRepo(t, filepath.Join(root, "nested"))
	outside := t.TempDir()
	initRepo(t, outside)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, multi := range []bool{false, true} {
		want := Discover(t.Context(), root, multi)
		got, ids, err := DiscoverChecked(WithReadLimit(t.Context(), 4<<20), root, multi)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("discovery differs: got %+v want %+v err %v", got, want, err)
		}
		if len(ids) != len(got.Repos) {
			t.Fatalf("identities missing: %v", ids)
		}
	}
	// 预算不够时按没找全交回，不报错，也不越过预算
	ctx := WithReadLimit(t.Context(), 8)
	got, _, err := DiscoverChecked(ctx, root, true)
	if err != nil || !got.Partial {
		t.Fatalf("budget exhaustion: %+v %v", got, err)
	}
	if b := ctx.Value(readLimitKey{}).(*readBudget); b.remaining < 0 {
		t.Fatalf("budget overrun: %d", b.remaining)
	}
}

// 读满预算前找到的仓库照常交回，标成没找全。
func TestDiscoverCheckedPartialKeepsFound(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		initRepo(t, filepath.Join(root, name))
	}
	full, _, err := DiscoverChecked(WithReadLimit(t.Context(), 4<<20), root, true)
	if err != nil || full.Partial || len(full.Repos) != 3 {
		t.Fatalf("full discovery: %+v %v", full, err)
	}
	for limit := 64; limit < 64<<10; limit *= 2 {
		got, ids, err := DiscoverChecked(WithReadLimit(t.Context(), limit), root, true)
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if len(ids) != len(got.Repos) {
			t.Fatalf("limit %d: identities missing: %v", limit, ids)
		}
		if got.Partial && len(got.Repos) > 0 && len(got.Repos) < 3 {
			return
		}
	}
	t.Fatal("no budget produced a partial result with some repos")
}
