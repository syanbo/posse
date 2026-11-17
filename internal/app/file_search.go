package app

import (
	"context"
	"time"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/rpc"
)

func (r appRequest) listFiles(ctx context.Context, root string, repos []string, single, all bool) (*files.List, error) {
	if !r.current() {
		return nil, context.Canceled
	}
	if r.targetFiles() {
		if r.client == nil {
			return nil, context.Canceled
		}
		return r.client.ListFiles(ctx, rpc.FileListParams{Root: root, SingleRepo: single, All: all})
	}
	if all {
		return files.ListAll(ctx, root)
	}
	return listRepos(ctx, root, repos)
}

func (r appRequest) searchFiles(ctx context.Context, p rpc.FileSearchParams) (rpc.FileSearchResult, error) {
	if !r.current() || r.client == nil {
		return rpc.FileSearchResult{}, context.Canceled
	}
	return r.client.SearchFiles(ctx, p)
}

func (a *app) startTargetSearch(v *filesView, delay time.Duration) {
	s := &v.search
	r := a.request()
	s.request = r
	if _, err := s.query.Compile(); err != nil {
		s.hits, s.rows, s.running, s.err, s.badRegex = nil, nil, false, err.Error(), true
		return
	}
	s.running, s.err, s.badRegex = true, "", false
	ctx, cancel := context.WithTimeout(a.windowContext(), 30*time.Second)
	s.cancel = cancel
	gen := s.gen
	p := rpc.FileSearchParams{Root: v.root, SingleRepo: a.projectSingleRepo(v), Query: s.query}
	time.AfterFunc(delay, func() {
		a.update(func() {
			if s.gen != gen {
				cancel()
				return
			}
			if !r.current() || ctx.Err() != nil {
				cancel()
				s.running = false
				return
			}
			p.Overlay = editorOverlay(v)
			go func() {
				out, err := r.searchFiles(ctx, p)
				a.update(func() {
					defer cancel()
					if s.gen != gen {
						return
					}
					s.running = false
					if !r.current() || a.files[v.wsID] != nil && a.files[v.wsID] != v {
						return
					}
					if err != nil {
						s.err = errtext.Text(err)
						return
					}
					s.hits, s.rows, s.truncated = out.Hits, searchRows(out.Hits), out.Truncated
				})
			}()
		})
	})
}

func (a *app) startTargetQuickText(v *filesView, q files.Query, gen int) {
	r := a.request()
	a.quick.text.request = r
	ctx, cancel := context.WithTimeout(a.windowContext(), 30*time.Second)
	a.quick.text.cancel, a.quick.text.running = cancel, true
	p := rpc.FileSearchParams{Root: v.root, SingleRepo: a.projectSingleRepo(v), Query: q}
	time.AfterFunc(150*time.Millisecond, func() {
		a.update(func() {
			if a.quick.text.gen != gen {
				cancel()
				return
			}
			if !r.current() || ctx.Err() != nil {
				cancel()
				a.quick.text.running = false
				return
			}
			p.Overlay = editorOverlay(v)
			go func() {
				out, err := r.searchFiles(ctx, p)
				a.update(func() {
					defer cancel()
					t := &a.quick.text
					if t.gen != gen {
						return
					}
					t.running = false
					if !r.current() || a.quick.ws != v.wsID || !a.quick.open {
						return
					}
					if err != nil {
						t.err = errtext.Text(err)
						return
					}
					t.hits, t.more = out.Hits, out.Truncated
					if len(t.hits) > quickTextMax {
						t.hits, t.more = t.hits[:quickTextMax], true
					}
				})
			}()
		})
	})
}
