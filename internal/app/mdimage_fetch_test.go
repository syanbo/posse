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

// slowServer 收到的请求都卡在 gate 上，放开后返回路径；记下每个路径被请求了几次。
func slowServer(t *testing.T) (*httptest.Server, chan struct{}, func() map[string]int) {
	var mu sync.Mutex
	hits := map[string]int{}
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		<-gate
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv, gate, func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int{}
		for k, v := range hits {
			out[k] = v
		}
		return out
	}
}

func waitHits(t *testing.T, hits func() map[string]int, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		total := 0
		for _, v := range hits() {
			total += v
		}
		if total == n {
			return
		}
		if total > n || time.Now().After(deadline) {
			t.Fatalf("requests %v, want %d in total", hits(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 文档关了：排队等名额的图不再发请求，已经在下载的照常下完；新开的文档只等在下载的那几张，不等关掉的文档的队列。
// 两个文档引用同一张图，关掉一个不影响另一个；关了再开（新的一份）照常能下载。
func TestMDImagesClosedDocStopsQueue(t *testing.T) {
	srv, gate, hits := slowServer(t)
	a := newMDImages(t.TempDir(), "")
	b := newMDImages(t.TempDir(), "")
	type result struct {
		src string
		err error
	}
	done := make(chan result, 64)
	fetch := func(l *mdImages, src string) {
		go func() {
			_, _, err := l.read(src)
			done <- result{src, err}
		}()
	}
	for i := range 24 {
		fetch(a, fmt.Sprintf("%s/a%d.png", srv.URL, i))
	}
	waitHits(t, hits, mdFetchSlots) // 四张在下，其余在排队
	shared := srv.URL + "/shared.png"
	fetch(b, shared) // b 也在排队，引用的图 a 关掉后照样要拿到
	a.close()
	// a 排队的那些马上退出，不发请求
	closed := 0
	for closed < 24-mdFetchSlots {
		r := <-done
		if r.err != errMDClosed {
			t.Fatalf("%s: %v before the gate opened", r.src, r.err)
		}
		closed++
	}
	close(gate)
	got := map[string]error{}
	for range mdFetchSlots + 1 {
		r := <-done
		got[r.src] = r.err
	}
	if err, ok := got[shared]; !ok || err != nil {
		t.Fatalf("shared image of the open document: %v (%v)", err, ok)
	}
	waitHits(t, hits, mdFetchSlots+1) // 关掉的文档只下了已经在下的那四张
	// 关了再开：新的一份照常下载
	again := newMDImages(t.TempDir(), "")
	if body, _, err := again.read(srv.URL + "/a10.png"); err != nil || string(body) != "/a10.png" {
		t.Fatalf("reopened: %q %v", body, err)
	}
}

// 关掉文档（dropDoc、换掉预览标签、工作区没了都经 closeWeb）时它预览里的图跟着关。
func TestCloseDocClosesMDImages(t *testing.T) {
	d := &fileDoc{mdImgs: newMDImages(t.TempDir(), "")}
	closeWeb(d)
	select {
	case <-d.mdImgs.closed:
	default:
		t.Fatal("markdown images still open after the document closed")
	}
	closeWeb(d) // 再关一次不出错
}
