package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 一篇文档里有很多远程图片：同时下载的不超过 mdFetchSlots 张，每张照样拿到自己的内容。
func TestMDImagesFetchLimit(t *testing.T) {
	var now, most atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := now.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		now.Add(-1)
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, r.URL.Path)
	}))
	defer srv.Close()
	l := newMDImages(t.TempDir(), "")
	const n = 24
	var wg sync.WaitGroup
	got := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, _, err := l.read(fmt.Sprintf("%s/img%d.png", srv.URL, i))
			if err != nil {
				t.Error(err)
			}
			got[i] = string(b)
		}()
	}
	wg.Wait()
	for i, s := range got {
		if s != fmt.Sprintf("/img%d.png", i) {
			t.Fatalf("image %d got %q", i, s)
		}
	}
	t.Logf("most concurrent downloads: %d of %d", most.Load(), n)
	if most.Load() > mdFetchSlots {
		t.Fatalf("%d downloads at once, limit %d", most.Load(), mdFetchSlots)
	}
}
