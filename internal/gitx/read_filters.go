package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	entries, err := runRead(ctx, dir, nil, nil, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	var paths bytes.Buffer
	var submodules []string
	for _, record := range strings.Split(string(entries), "\x00") {
		meta, path, ok := strings.Cut(record, "\t")
		if !ok {
			continue
		}
		mode, _, _ := strings.Cut(meta, " ")
		switch mode {
		case "100644", "100755":
			if len(drivers) > 0 {
				paths.WriteString(path)
				paths.WriteByte(0)
			}
		case "160000":
			submodules = append(submodules, path)
		}
	}
	if paths.Len() > 0 {
		attrs, err := runRead(ctx, dir, paths.Bytes(), nil, "check-attr", "-z", "--stdin", "filter")
		if err != nil {
			return err
		}
		fields := bytes.Split(attrs, []byte{0})
		for i := 0; i+2 < len(fields); i += 3 {
			if drivers[string(fields[i+2])] {
				name := string(fields[i+2])
				if len(name) > 256 {
					name = name[:256]
				}
				return fmt.Errorf("Git content query would execute filter %s", name)
			}
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
	return ctx.Err()
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
