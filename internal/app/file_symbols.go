package app

import (
	"context"
	"slices"
	"time"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/rpc"
	"posse/locales"
)

func (a *app) startTargetQuickSymbols(v *filesView, q files.SymbolQuery, gen int) {
	r := a.request()
	ctx, cancel := context.WithTimeout(a.windowContext(), 30*time.Second)
	a.quick.sym.request = r
	a.quick.sym.cancel, a.quick.sym.running = cancel, true
	p := rpc.FileSymbolsParams{Root: v.root, SingleRepo: a.projectSingleRepo(v), Query: q}
	time.AfterFunc(150*time.Millisecond, func() {
		a.update(func() {
			if a.quick.sym.gen != gen {
				cancel()
				return
			}
			if !r.current() || ctx.Err() != nil {
				cancel()
				a.quick.sym.running = false
				return
			}
			p.Overlay = editorOverlay(v)
			go func() {
				var out rpc.FileSymbolsResult
				err := r.invoke(ctx, "files.symbols", p, &out)
				a.update(func() {
					defer cancel()
					s := &a.quick.sym
					if s.gen != gen {
						return
					}
					s.running = false
					if !r.current() || !a.quick.open || a.quick.ws != v.wsID {
						return
					}
					if err != nil {
						s.err = errtext.Text(err)
						return
					}
					s.syms = out.Symbols
				})
			}()
		})
	})
}

func targetDefinitionCurrent(r appRequest, v *filesView, d *fileDoc, path string, version uint64) bool {
	return r.current() && v != nil && r.app.files[v.wsID] == v && v.cur == d && slices.Contains(v.docs, d) && d.path == path && d.ed.Buffer != nil && d.ed.Buffer.Version() == version
}

func (p *defPick) current() bool {
	if p.request.app == nil {
		return true
	}
	v := p.request.app.files[p.ws]
	return targetDefinitionCurrent(p.request, v, p.doc, p.origin.path, p.version)
}

func (a *app) goToTargetDefinition(m locales.Messages, w *workspace, v *filesView, d *fileDoc, off int, link files.Link, origin jumpLoc) {
	r := a.request()
	s := &v.def
	s.gen++
	gen := s.gen
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithTimeout(a.windowContext(), 30*time.Second)
	s.cancel, s.running, s.word, s.doc = cancel, true, link.Text, d
	a.defPick.open = false
	version, path := d.ed.Buffer.Version(), d.path
	p := rpc.FileDefinitionParams{Root: v.root, SingleRepo: a.projectSingleRepo(v), Path: path, Text: d.ed.Buffer.String(), Offset: off, Overlay: editorOverlay(v)}
	go func() {
		var out rpc.FileDefinitionResult
		err := r.invoke(ctx, "files.definition", p, &out)
		cancel()
		a.update(func() {
			if s.gen != gen {
				return
			}
			s.running = false
			if !targetDefinitionCurrent(r, v, d, path, version) {
				return
			}
			if err != nil {
				a.err = m.FilesDefinitionFailed(errtext.Text(err))
				return
			}
			if link.Kind == files.LinkPath {
				if out.Path == "" {
					d.hint, d.hintAt = m.FilesPathNotFound(link.Text), time.Now()
					return
				}
				v.jumpFrom = &origin
				a.openFile(w, out.Path, out.Line, [2]int{})
				return
			}
			defs := out.Definitions
			if len(defs) == 0 {
				d.hint, d.hintAt = m.FilesNoDefinition(link.Text), time.Now()
				return
			}
			from := ""
			if rel, ok := relInside(v.root, path); ok {
				from = rel
			}
			here := func(x files.Definition) bool { return from != "" && x.Path == from && x.Line == origin.line-1 }
			if slices.ContainsFunc(defs, here) {
				defs = slices.DeleteFunc(defs, here)
				if len(defs) == 0 {
					a.searchUsages(w, v, link.Text)
					return
				}
			}
			if unique, ok := files.Unique(defs); ok {
				a.jumpTo(w, v, origin, unique)
				return
			}
			a.defPick = defPick{request: r, doc: d, version: version, origin: origin, open: true, ws: w.ID, word: link.Text, defs: defs}
			v.def.origin = origin
		})
	}()
}
