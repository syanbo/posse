package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFetchOutputBoundsAndRedaction(t *testing.T) {
	for _, chunk := range []int{1, 7, 4096, 32768} {
		b := &fetchOutput{}
		text := "From https://user:private-value@example.invalid/repo?key=hidden-value\nAuthorization: Bearer auth-value\nremote: arbitrary-server-value\n" + strings.Repeat("progress line\n", 10000) + "https://user:" + strings.Repeat("long-value", 1000) + "@example.invalid/repo\nfinished\n"
		for start := 0; start < len(text); start += chunk {
			b.Write([]byte(text[start:min(start+chunk, len(text))]))
		}
		b.finish()
		out := b.String()
		if !b.truncated || len(out) > 5<<10 || !strings.Contains(out, "finished") || !strings.Contains(out, "bytes omitted") {
			t.Fatalf("chunk %d bounded %d truncated=%v", chunk, len(out), b.truncated)
		}
		for _, secret := range []string{"private-value", "hidden-value", "auth-value", "arbitrary-server-value", "long-value", "example.invalid"} {
			if strings.Contains(out, secret) {
				t.Fatalf("chunk %d leaked %s", chunk, secret)
			}
		}
		if len(b.head)+len(b.tail)+len(b.line) > 2*fetchStreamLimit {
			t.Fatal("retained unbounded output")
		}
	}
}

func TestFetchOutputQuotedURLAndIdempotence(t *testing.T) {
	for _, input := range []string{
		"fatal: unable to access 'https://user:prefix'credential-tail@example.invalid/repo/': Failed to connect\n",
		"From ssh://user:prefix'credential-tail@example.invalid/repo\n",
		"fatal: https://user:prefix\"credential-tail@example.invalid/repo\n",
		"From user'credential-tail@example.invalid:repo\n",
	} {
		want := "[remote URL omitted]\n"
		if got := SanitizeFetchText(input); got != want || SanitizeFetchText(got) != got {
			t.Fatalf("URL sanitization or idempotence failed: %q", got)
		}
		for _, chunk := range []int{1, 7, 4096} {
			out := &fetchOutput{}
			for start := 0; start < len(input); start += chunk {
				out.Write([]byte(input[start:min(start+chunk, len(input))]))
			}
			out.finish()
			if got := out.String(); got != want || SanitizeFetchText(got) != got {
				t.Fatalf("chunk %d retained URL or changed on repeat: %q", chunk, got)
			}
		}
	}
	plain := "fatal: cannot lock ref 'refs/remotes/origin/main': Permission denied\nerror: could not fetch origin\n"
	if got := SanitizeFetchText(plain); got != plain || SanitizeFetchText(got) != got {
		t.Fatalf("ordinary diagnostic changed: %q", got)
	}
}

func TestFetchBoundedPreservesSuccess(t *testing.T) {
	root := t.TempDir()
	divergenceWrapper(t, "test \"$*\" = 'fetch --all' || exit 99; i=0; while test $i -lt 5000; do printf 'ordinary progress line\\n'; i=$((i+1)); done; printf 'From https://user:private-value@example.invalid/repo\\n' >&2; exit 0")
	var records []Run
	ctx := WithOp(WithTrace(t.Context(), func(r Run) {
		if r.Done {
			records = append(records, r)
		}
	}), "fetch")
	truncated, err := FetchBounded(ctx, root)
	if err != nil || !truncated || len(records) != 1 || records[0].Err != "" || strings.Contains(records[0].Out, "private-value") {
		t.Fatalf("success truncated=%v logs=%+v err=%v", truncated, records, err)
	}
	if _, err := FetchBounded(WithReadLimit(ctx, 100), root); err == nil {
		t.Fatal("read context fetched")
	}
}

func TestFetchBoundedCancelAndTimeout(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "child")
			t.Setenv("FETCH_CHILD_PID", marker)
			divergenceWrapper(t, "/bin/sleep 30 &\nprintf '%s' \"$!\" > \"$FETCH_CHILD_PID\"\nwait")
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if mode == "cancel" {
				go func() {
					ticker := time.NewTicker(5 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							if _, err := os.Stat(marker); err == nil {
								cancel()
								return
							}
						}
					}
				}()
			}
			var record Run
			_, err := FetchBounded(WithOp(WithTrace(ctx, func(r Run) {
				if r.Done {
					record = r
				}
			}), "fetch"), t.TempDir())
			want := context.DeadlineExceeded
			if mode == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) || !record.Done || record.Canceled != (mode == "cancel") {
				t.Fatalf("%s cause=%v log=%+v", mode, err, record)
			}
			data, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Kill(pid, syscall.SIGKILL)
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for syscall.Kill(pid, 0) != syscall.ESRCH {
				select {
				case <-deadline.C:
					t.Fatal("fetch child survived")
				case <-tick.C:
				}
			}
		})
	}
}
