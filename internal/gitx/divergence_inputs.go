package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	divergencePathLimit        = 20000
	divergenceBlobLimit  int64 = 8 << 20
	divergenceInputLimit int64 = 32 << 20
)

type divergenceBaseline struct {
	Version                         int
	Head, Base, BaseRef, GitVersion string
	Ahead, Behind                   int
	Shallow, Config, Attributes     []byte
	MergeBases                      []string
}

type divergenceInputs struct {
	baseline   divergenceBaseline
	args       []string
	attributes string
	reason     string
}

func readDivergenceInputs(ctx context.Context, id RepositoryIdentity, base string) (divergenceInputs, error) {
	s := divergenceInputs{baseline: divergenceBaseline{Version: 1, BaseRef: base}}
	var err error
	s.baseline.Head, s.baseline.Base, err = compareStarts(ctx, id.Root, "HEAD", base)
	if err != nil {
		return s, err
	}
	s.baseline.Shallow, err = historyShallow(ctx, id.Root)
	if err != nil {
		return s, err
	}
	raw, err := historyRead(ctx, id.Root, nil, "rev-list", "--left-right", "--count", s.baseline.Head+"..."+s.baseline.Base, "--")
	if err != nil {
		return s, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return s, ErrDivergenceOutput
	}
	for i, target := range []*int{&s.baseline.Ahead, &s.baseline.Behind} {
		n, e := strconv.Atoi(fields[i])
		if e != nil || n < 0 || strconv.Itoa(n) != fields[i] {
			return s, ErrDivergenceOutput
		}
		*target = n
	}
	if s.baseline.Ahead == 0 || s.baseline.Behind == 0 {
		return s, nil
	}
	version, err := historyRead(ctx, id.Root, nil, "--version")
	if err != nil {
		return s, err
	}
	s.baseline.GitVersion = string(version)
	if len(s.baseline.Shallow) != 0 {
		s.reason = DivergenceShallow
		return s, nil
	}
	raw, err = historyRead(ctx, id.Root, nil, "merge-base", "--all", s.baseline.Head, s.baseline.Base)
	if err != nil {
		if pushNoMatch(err) {
			s.reason = DivergenceUnrelated
			return s, nil
		}
		return s, err
	}
	for _, oid := range strings.Fields(string(raw)) {
		if !validDivergenceOID(oid) || len(oid) != len(s.baseline.Head) {
			return s, ErrDivergenceOutput
		}
		s.baseline.MergeBases = append(s.baseline.MergeBases, oid)
	}
	if len(s.baseline.MergeBases) == 0 {
		return s, ErrDivergenceOutput
	}
	sort.Strings(s.baseline.MergeBases)
	if len(s.baseline.MergeBases) != 1 {
		s.reason = DivergenceMultipleBases
		return s, nil
	}
	s.baseline.Config, err = historyRead(ctx, id.Root, nil, "config", "--null", "--get-regexp", `^(merge\..*|diff\.(algorithm|renames|renamelimit)|core\.(bigfilethreshold|autocrlf|eol|safecrlf))$`)
	if err != nil {
		if !pushNoMatch(err) {
			return s, err
		}
		s.baseline.Config = nil
	}
	s.args, s.reason, err = divergenceConfig(s.baseline.Config)
	if err != nil || s.reason != "" {
		return s, err
	}
	trees := make([]map[string]worktreeBase, 3)
	names, changed, blobs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, oid := range []string{s.baseline.MergeBases[0], s.baseline.Head, s.baseline.Base} {
		trees[i], err = worktreeTree(ctx, id.Root, oid)
		if err != nil {
			return s, err
		}
		for name := range trees[i] {
			names[name] = true
		}
		if len(names) > divergencePathLimit {
			s.reason = DivergenceLimit
			return s, nil
		}
	}
	for name := range names {
		if trees[0][name] == trees[1][name] && trees[0][name] == trees[2][name] {
			continue
		}
		changed[name] = true
		for _, tree := range trees {
			e := tree[name]
			if e.mode == "160000" {
				s.reason = DivergenceSubmodule
				return s, nil
			}
			if e.oid != "" {
				blobs[e.oid] = true
			}
		}
	}
	// ort 的内容合并及重命名候选来自基准与两端的变更项；未改的大资源不读正文。
	if err := divergenceBlobSizes(ctx, id.Root, blobs); err != nil {
		if errors.Is(err, errDivergenceInputLimit) {
			s.reason = DivergenceLimit
			return s, nil
		}
		return s, err
	}
	paths := make([]string, 0, len(names))
	for name := range names {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	if len(paths) > 0 {
		s.baseline.Attributes, err = readDivergenceAttributes(ctx, id.Root, paths)
		if err != nil {
			return s, err
		}
		s.attributes, s.reason, err = divergenceAttributes(paths, changed, s.baseline.Attributes)
		if err != nil || s.reason != "" {
			return s, err
		}
	}
	// 目录重命名可能合成三棵树以外的新路径，逐路径捕获的属性不能替它作决定。
	// 仅在确实出现该候选时拒绝预测，普通文件重命名仍由 ort 计算。
	for _, tip := range []string{s.baseline.Head, s.baseline.Base} {
		raw, e := historyRead(ctx, id.Root, nil, "diff-tree", "--no-commit-id", "--no-ext-diff", "--no-textconv", "-r", "-M", "--name-status", "-z", s.baseline.MergeBases[0], tip, "--")
		if e != nil {
			return s, e
		}
		changes, e := parseHistoryChanges(raw)
		if e != nil {
			return s, e
		}
		for _, c := range changes {
			if c.Status != 'R' || path.Dir(c.Orig) == path.Dir(c.Path) {
				continue
			}
			old, to := path.Dir(c.Orig)+"/", path.Dir(c.Path)+"/"
			for name := range changed {
				if strings.HasPrefix(name, old) && !names[to+strings.TrimPrefix(name, old)] {
					s.reason = DivergenceAttributes
					return s, nil
				}
			}
		}
	}
	return s, nil
}

var errDivergenceInputLimit = errors.New("Git divergence input exceeds the prediction limit")

func divergenceBlobSizes(ctx context.Context, dir string, blobs map[string]bool) error {
	if len(blobs) == 0 {
		return nil
	}
	oids := make([]string, 0, len(blobs))
	for oid := range blobs {
		oids = append(oids, oid)
	}
	sort.Strings(oids)
	raw, err := historyRead(ctx, dir, []byte(strings.Join(oids, "\n")+"\n"), "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != len(oids) {
		return ErrDivergenceOutput
	}
	var total int64
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != oids[i] || f[1] != "blob" {
			return ErrDivergenceOutput
		}
		n, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil || n < 0 {
			return ErrDivergenceOutput
		}
		if n > divergenceBlobLimit || n > divergenceInputLimit-total {
			return errDivergenceInputLimit
		}
		total += n
	}
	return nil
}

func divergenceConfig(raw []byte) ([]string, string, error) {
	args := []string{"-c", "core.attributesFile=/dev/null", "-c", "core.hooksPath=/dev/null"}
	if len(raw) == 0 {
		return args, "", nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, "", ErrDivergenceOutput
	}
	values := map[string]string{}
	for _, rec := range bytes.Split(raw[:len(raw)-1], []byte{0}) {
		k, v, ok := strings.Cut(string(rec), "\n")
		if !ok {
			return nil, "", ErrDivergenceOutput
		}
		values[k] = v
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := values[k]
		switch k {
		case "merge.renormalize":
			if v != "false" && v != "no" && v != "0" {
				return nil, DivergenceConfig, nil
			}
		case "merge.default":
			if v != "text" && v != "binary" && v != "union" {
				return nil, DivergenceConfig, nil
			}
			args = append(args, "-c", k+"="+v)
		case "merge.conflictstyle", "merge.renames", "merge.renamelimit", "merge.directoryrenames", "diff.renames", "diff.renamelimit", "diff.algorithm", "core.bigfilethreshold":
			args = append(args, "-c", k+"="+v)
		}
	}
	return args, "", nil
}

// merge-tree 不读目标索引；普通 check-attr 的暂存区回退会把未生效的 union 当成已生效。
func readDivergenceAttributes(ctx context.Context, dir string, paths []string) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "posse-divergence-attrs-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	ctx = context.WithValue(ctx, readStrictKey{}, true)
	return runRead(ctx, dir, []byte(strings.Join(paths, "\x00")+"\x00"), []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index"), "GIT_NO_REPLACE_OBJECTS=1"}, "check-attr", "-z", "--stdin", "merge", "conflict-marker-size")
}

func divergenceAttributes(paths []string, changed map[string]bool, raw []byte) (string, string, error) {
	parts := bytes.Split(raw, []byte{0})
	if len(parts) != len(paths)*6+1 || len(parts[len(parts)-1]) != 0 {
		return "", "", ErrDivergenceOutput
	}
	var rules strings.Builder
	for i, name := range paths {
		attributes := []string{}
		for j, key := range []string{"merge", "conflict-marker-size"} {
			n := i*6 + j*3
			if string(parts[n]) != name || string(parts[n+1]) != key {
				return "", "", ErrDivergenceOutput
			}
			v := string(parts[n+2])
			switch v {
			case "unspecified":
				attributes = append(attributes, "!"+key)
			case "set":
				attributes = append(attributes, key)
			case "unset":
				attributes = append(attributes, "-"+key)
			default:
				if key == "merge" && v != "text" && v != "binary" && v != "union" {
					if changed[name] {
						return "", DivergenceAttributes, nil
					}
					attributes = append(attributes, "!merge")
					continue
				}
				if key == "conflict-marker-size" {
					n, err := strconv.Atoi(v)
					if err != nil || n < 1 || n > 1024 {
						return "", DivergenceAttributes, nil
					}
				}
				attributes = append(attributes, key+"="+v)
			}
		}
		rules.WriteString(divergenceAttributePattern(name))
		rules.WriteByte(' ')
		rules.WriteString(strings.Join(attributes, " "))
		rules.WriteByte('\n')
	}
	return rules.String(), "", nil
}

func divergenceAttributePattern(name string) string {
	pattern := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]").Replace("/" + name)
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range []byte(pattern) {
		switch {
		case c == '\\' || c == '"':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 32 || c >= 127:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
