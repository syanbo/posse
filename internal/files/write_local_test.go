package files

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"
)

func coordinatedText(ctx context.Context, path string, expected Version, text string) (Version, error) {
	return WriteFileCoordinated(ctx, path, expected, func(w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	})
}

func TestWriteFileCoordinatedSerializesAndCancels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "download")
	write(t, path, "original")
	before := fileVersion(t, path)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := WriteFileCoordinated(ctx, path, before, func(w io.Writer) error {
			close(entered)
			select {
			case <-unblock:
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err := io.WriteString(w, "first")
			return err
		})
		first <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		_, err := coordinatedText(ctx, path, before, "second")
		second <- err
	}()
	// 第一笔写入已进入临时文件回调并持锁，不同目的仍能独立完成。
	other := filepath.Join(filepath.Dir(path), "independent")
	if _, err := coordinatedText(ctx, other, fileVersion(t, other), "independent"); err != nil {
		t.Fatalf("different destination blocked: %v", err)
	}
	waiting, stopWaiting := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stopWaiting()
	if _, err := coordinatedText(waiting, path, before, "canceled"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting was not cancelable: %v", err)
	}
	select {
	case err := <-second:
		t.Fatalf("same destination bypassed held lock: %v", err)
	default:
	}
	release()
	if err := <-first; err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := <-second; !errors.Is(err, ErrSaveConflict) {
		t.Fatalf("same baseline must conflict after first save: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "first" {
		t.Fatalf("contents: %q %v", got, err)
	}
	// 成功、取消等待都不能留下锁；新版本仍可继续写。
	if _, err := coordinatedText(ctx, path, fileVersion(t, path), "next"); err != nil {
		t.Fatalf("lock leaked: %v", err)
	}
}

func TestWriteFileCoordinatedReleasesFailedWrite(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "write-failed", true: "canceled-owner"}[canceled], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file")
			write(t, path, "original")
			before := fileVersion(t, path)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("write failed")
			_, err := WriteFileCoordinated(ctx, path, before, func(w io.Writer) error {
				if _, err := io.WriteString(w, "partial"); err != nil {
					return err
				}
				if canceled {
					cancel()
					return nil
				}
				return failure
			})
			if canceled && !errors.Is(err, context.Canceled) || !canceled && !errors.Is(err, failure) {
				t.Fatalf("failure: %v", err)
			}
			if !before.Same(fileVersion(t, path)) {
				t.Fatal("failed writer modified destination")
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatalf("partial files: %v %v", entries, err)
			}
			retry, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			if _, err := coordinatedText(retry, path, before, "recovered"); err != nil {
				t.Fatalf("failed writer leaked lock: %v", err)
			}
		})
	}
}

func TestWriteFileCoordinatedDarwinAliases(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin path identity")
	}
	path := filepath.Join(t.TempDir(), "Résumé")
	write(t, path, "original")
	before := fileVersion(t, path)
	release, err := localWrites.Acquire(t.Context(), localWriteScope(before.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, alias := range []string{strings.ToUpper(before.Path), norm.NFD.String(before.Path)} {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		other := before
		other.Path = alias
		_, err := coordinatedText(ctx, alias, other, "changed")
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("alias did not share held lock: %s %v", alias, err)
		}
	}
}
