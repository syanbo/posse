package gitx

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestTraceContextIsolation(t *testing.T) {
	dirs := []string{t.TempDir(), t.TempDir()}
	for _, dir := range dirs {
		gitIn(t, dir, "init", "-q")
	}
	var mu sync.Mutex
	var fallback []Run
	old := Trace
	Trace = func(r Run) { mu.Lock(); fallback = append(fallback, r); mu.Unlock() }
	t.Cleanup(func() { Trace = old })
	var records [2][]Run
	var wg sync.WaitGroup
	for i, dir := range dirs {
		wg.Go(func() {
			ctx := WithOp(WithTrace(context.Background(), func(r Run) {
				mu.Lock()
				records[i] = append(records[i], r)
				mu.Unlock()
			}), "operation")
			if _, err := runRaw(ctx, dir, nil, nil, "status", "--porcelain=v2"); err != nil {
				t.Error(err)
			}
			if got := TopLevel(ctx, dir); got == "" {
				t.Error("missing repository root")
			}
			OpResult(ctx, dir, "domain failure")
			OpCanceled(ctx, dir)
		})
	}
	wg.Wait()
	if len(fallback) != 0 {
		t.Fatalf("target commands reached default collector: %+v", fallback)
	}
	for i, runs := range records {
		if len(runs) != 6 {
			t.Fatalf("collector %d: %+v", i, runs)
		}
		for _, r := range runs {
			if r.Dir != dirs[i] || r.Op != "operation" || r.OpID == 0 || r.OpID != runs[0].OpID {
				t.Fatalf("collector %d received unrelated record: %+v", i, r)
			}
		}
		for j := 0; j < 4; j += 2 {
			if runs[j].Done || !runs[j+1].Done || runs[j].ID != runs[j+1].ID {
				t.Fatalf("command start/end mismatch: %+v", runs[j:j+2])
			}
		}
		if runs[4].Err != "domain failure" || !runs[4].Done || !runs[5].Canceled {
			t.Fatalf("operation outcomes: %+v", runs[4:])
		}
	}
	if records[0][0].OpID == records[1][0].OpID {
		t.Fatal("parallel operations share an ID")
	}

	ctx := WithOp(WithTrace(context.Background(), nil), "muted")
	runRaw(ctx, dirs[0], nil, nil, "branch", "-D", "missing")
	TopLevel(ctx, dirs[0])
	OpResult(ctx, dirs[0], "muted failure")
	OpCanceled(ctx, dirs[0])
	if len(fallback) != 0 {
		t.Fatalf("explicit nil collector fell back: %+v", fallback)
	}
	if _, err := runRaw(context.Background(), dirs[0], nil, nil, "branch", "-D", "missing"); err == nil {
		t.Fatal("expected missing branch failure")
	}
	if len(fallback) != 2 || !fallback[1].Done || fallback[1].Err == "" {
		t.Fatalf("default collector no longer works: %+v", fallback)
	}
}

func TestTraceCommandKeepsCollector(t *testing.T) {
	old := Trace
	t.Cleanup(func() { Trace = old })
	var first, second []Run
	Trace = func(r Run) { first = append(first, r) }
	ctx := WithOp(context.Background(), "operation")
	rec := traceStart(ctx, "/repo", []string{"commit", "-F", "-"})
	Trace = func(r Run) { second = append(second, r) }
	traceEnd(rec, []byte(strings.Repeat("x", 16<<10)), nil, "", false)
	if len(first) != 2 || len(second) != 0 || first[0].ID != first[1].ID || !first[1].Done {
		t.Fatalf("command changed collector: first=%+v second=%+v", first, second)
	}
	if _, _, _, ok := Clipped(first[1].Out); !ok {
		t.Fatal("command output no longer bounded")
	}
}
