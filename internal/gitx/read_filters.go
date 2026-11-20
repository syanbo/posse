package gitx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// indexScanLimit 是一份索引清单或一份 filter 属性的流式上限。
// 这两段只用来判断会不会执行过滤器，并给索引做指纹；不保留正文，也不占返回给窗口的 4MB 内容预算。
const indexScanLimit = 32 << 20

type indexIDKey struct{}

// indexFingerprint 记下状态检查里已经流过的那份 ls-files -s -z。
type indexFingerprint struct {
	id    string
	ready bool
}

// checkReadFilters 只读配置和属性，不靠停用过滤器来返回与真实 Git 不同的状态。
func checkReadFilters(ctx context.Context, dir string, depth int, seen map[string]bool) error {
	if depth > 32 {
		return errors.New("Git submodule query exceeds the nesting limit")
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if seen[real] {
		return nil
	}
	seen[real] = true
	config, err := runRead(ctx, dir, nil, nil, "config", "--null", "--list", "--includes")
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, record := range strings.Split(string(config), "\x00") {
		key, value, _ := strings.Cut(record, "\n")
		values[key] = value
	}
	drivers := map[string]bool{}
	for key, value := range values {
		if value == "" || !strings.HasPrefix(key, "filter.") {
			continue
		}
		for _, suffix := range []string{".clean", ".process"} {
			if name, ok := strings.CutSuffix(strings.TrimPrefix(key, "filter."), suffix); ok {
				drivers[name] = true
			}
		}
	}
	scan := &indexScanner{hash: sha256.New(), drivers: drivers}
	if _, err := runReadOutput(ctx, dir, nil, nil, scan, indexScanLimit, "ls-files", "-s", "-z"); err != nil {
		return err
	}
	scan.finish()
	paths, submodules := scan.paths, scan.submodules
	if paths.Len() > 0 {
		attrs := &attrScanner{drivers: drivers}
		if _, err := runReadOutput(ctx, dir, paths.Bytes(), nil, attrs, indexScanLimit, "check-attr", "-z", "--stdin", "filter"); err != nil {
			return err
		}
		attrs.finish()
		if attrs.hit != "" {
			return fmt.Errorf("Git content query would execute filter %s", attrs.hit)
		}
	}
	for _, path := range submodules {
		child := filepath.Join(dir, filepath.FromSlash(path))
		if _, err := os.Lstat(filepath.Join(child, ".git")); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := checkReadFilters(ctx, child, depth+1, seen); err != nil {
			return err
		}
	}
	if depth == 0 && ctx.Err() == nil {
		if fp, ok := ctx.Value(indexIDKey{}).(*indexFingerprint); ok {
			fp.id = hex.EncodeToString(scan.hash.Sum(nil))
			fp.ready = true
		}
	}
	return ctx.Err()
}

type indexScanner struct {
	hash       hash.Hash
	rest       []byte
	drivers    map[string]bool
	paths      bytes.Buffer
	submodules []string
}

func (s *indexScanner) Write(p []byte) (int, error) {
	_, _ = s.hash.Write(p)
	s.rest = append(s.rest, p...)
	s.drain(false)
	return len(p), nil
}

func (s *indexScanner) finish() { s.drain(true) }

func (s *indexScanner) drain(final bool) {
	for {
		i := bytes.IndexByte(s.rest, 0)
		if i < 0 {
			if final && len(s.rest) > 0 {
				s.record(s.rest)
				s.rest = nil
			}
			return
		}
		s.record(s.rest[:i])
		s.rest = s.rest[i+1:]
	}
}

func (s *indexScanner) record(rec []byte) {
	meta, path, ok := bytes.Cut(rec, []byte{'\t'})
	if !ok {
		return
	}
	mode, _, _ := bytes.Cut(meta, []byte{' '})
	switch string(mode) {
	case "100644", "100755":
		if len(s.drivers) > 0 {
			s.paths.Write(path)
			s.paths.WriteByte(0)
		}
	case "160000":
		s.submodules = append(s.submodules, string(path))
	}
}

type attrScanner struct {
	drivers map[string]bool
	rest    []byte
	field   int
	hit     string
}

func (s *attrScanner) Write(p []byte) (int, error) {
	s.rest = append(s.rest, p...)
	for {
		i := bytes.IndexByte(s.rest, 0)
		if i < 0 {
			break
		}
		if s.field == 2 && s.hit == "" && s.drivers[string(s.rest[:i])] {
			name := string(s.rest[:i])
			if len(name) > 256 {
				name = name[:256]
			}
			s.hit = name
		}
		s.field = (s.field + 1) % 3
		s.rest = s.rest[i+1:]
	}
	return len(p), nil
}

func (s *attrScanner) finish() {
	if len(s.rest) == 0 || s.field != 2 || s.hit != "" || !s.drivers[string(s.rest)] {
		return
	}
	name := string(s.rest)
	if len(name) > 256 {
		name = name[:256]
	}
	s.hit = name
}

// HeadExistsChecked 区分尚未提交的符号分支和损坏的 HEAD，不读取工作树内容。
func HeadExistsChecked(ctx context.Context, dir string) (bool, error) {
	_, err := runRaw(ctx, dir, nil, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return false, err
	}
	ref, refErr := runRaw(ctx, dir, nil, nil, "symbolic-ref", "--quiet", "HEAD")
	if refErr != nil {
		return false, refErr
	}
	_, refErr = runRaw(ctx, dir, nil, nil, "show-ref", "--verify", "--quiet", strings.TrimSuffix(string(ref), "\n"))
	if errors.As(refErr, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	if refErr != nil {
		return false, refErr
	}
	return false, err
}
