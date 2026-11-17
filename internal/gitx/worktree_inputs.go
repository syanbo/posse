package gitx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"posse/internal/files"
)

const WorktreeScanLimit int64 = 512 << 20
const worktreePathLimit = 20000

type worktreeBudget struct {
	bytes        int64
	paths, repos int
	buffer       [32 << 10]byte
}
type worktreeEntry struct {
	mode, oid, name string
	file            WorktreeFile
	child           *worktreeNode
}
type worktreeNode struct {
	id                              RepositoryIdentity
	head                            string
	index, attrs, config, untracked []byte
	entries                         []worktreeEntry
	args                            []string
	attributeFile                   string
	digest                          string
	status                          bool
	ignores                         map[string]string
}

func (n *worktreeNode) readConfig(ctx context.Context) error {
	raw, err := worktreeConfigRead(ctx, n.id.Root, "--get-regexp", `^(core\.(autocrlf|eol|safecrlf|filemode|symlinks)|diff\.ignore[Ss]ubmodules|submodule\..*\.ignore)$`)
	if err != nil {
		return err
	}
	moduleSource := []string{"--file", ".gitmodules"}
	if _, statErr := os.Lstat(filepath.Join(n.id.Root, ".gitmodules")); os.IsNotExist(statErr) {
		for _, entry := range n.entries {
			if entry.name == ".gitmodules" {
				moduleSource = []string{"--blob", entry.oid}
				break
			}
		}
	} else if statErr != nil {
		return statErr
	}
	module, err := worktreeConfigRead(ctx, n.id.Root, append(moduleSource, "--get-regexp", `^submodule\..*\.(path|ignore)$`)...)
	if err != nil {
		return err
	}
	n.args = []string{"-c", "core.attributesFile=/dev/null"}
	n.ignores = map[string]string{"": "untracked"}
	names, ignores := map[string]string{}, map[string]string{}
	parse := func(data []byte, local bool) error {
		if len(data) == 0 {
			return nil
		}
		if data[len(data)-1] != 0 {
			return ErrWorktreeOutput
		}
		for _, rec := range bytes.Split(data[:len(data)-1], []byte{0}) {
			k, v, ok := strings.Cut(string(rec), "\n")
			if !ok {
				return ErrWorktreeOutput
			}
			switch strings.ToLower(k) {
			case "core.autocrlf", "core.eol", "core.safecrlf", "core.filemode", "core.symlinks":
				n.args = append(n.args, "-c", k+"="+v)
				continue
			case "diff.ignoresubmodules":
				if !validWorktreeIgnore(v) {
					return fmt.Errorf("Git working tree comparison does not support ignore setting %q", v)
				}
				n.ignores[""] = v
				continue
			}
			if !strings.HasPrefix(k, "submodule.") {
				return ErrWorktreeOutput
			}
			key := strings.TrimPrefix(k, "submodule.")
			if strings.HasSuffix(key, ".path") && !local {
				if !ValidBlameFile(v) {
					return ErrWorktreeOutput
				}
				names[strings.TrimSuffix(key, ".path")] = v
				continue
			}
			if !strings.HasSuffix(key, ".ignore") || !validWorktreeIgnore(v) {
				return fmt.Errorf("Git working tree comparison does not support ignore setting %q", v)
			}
			ignores[strings.TrimSuffix(key, ".ignore")] = v
		}
		return nil
	}
	// 路径来自 .gitmodules；本机配置覆盖其中的 ignore，再覆盖 diff 的缺省值。
	if err := parse(module, false); err != nil {
		return err
	}
	if err := parse(raw, true); err != nil {
		return err
	}
	for name, path := range names {
		if value, ok := ignores[name]; ok {
			n.ignores[path] = value
		}
	}
	n.config = append(append(raw, 0), module...)
	return nil
}
func validWorktreeIgnore(s string) bool {
	return s == "none" || s == "untracked" || s == "dirty" || s == "all"
}
func (n *worktreeNode) ignore(path string) string {
	if value, ok := n.ignores[path]; ok {
		return value
	}
	return n.ignores[""]
}
func worktreeConfigRead(ctx context.Context, dir string, args ...string) ([]byte, error) {
	raw, err := historyRead(ctx, dir, nil, append([]string{"config", "--null"}, args...)...)
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, err
		}
		return nil, nil
	}
	return raw, nil
}

func readWorktreeNode(ctx context.Context, id RepositoryIdentity, depth int, budget *worktreeBudget, inherited ...string) (*worktreeNode, error) {
	budget.repos++
	if depth > 8 || budget.repos > 64 {
		return nil, ErrWorktreeLimit
	}
	n := &worktreeNode{id: id, status: depth > 0}
	var err error
	exists, err := HeadExistsChecked(context.WithValue(ctx, readStrictKey{}, true), id.Root)
	if err != nil {
		return nil, err
	}
	if exists {
		n.head, err = compareRevision(ctx, id.Root, "HEAD")
		if err != nil {
			return nil, err
		}
	}
	raw, err := historyRead(ctx, id.Root, nil, "ls-files", "-v", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 && raw[len(raw)-1] != 0 {
		return nil, ErrWorktreeOutput
	}
	seen := map[string]bool{}
	var names bytes.Buffer
	for _, rec := range bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(rec), "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 4 || !ValidBlameFile(name) || !ValidBlobOID(f[2]) {
			return nil, ErrWorktreeOutput
		}
		if f[3] != "0" {
			return nil, ErrFileUnmerged
		}
		if f[0] != "H" {
			return nil, ErrWorktreeUnsupported
		}
		if seen[name] {
			return nil, ErrWorktreeOutput
		}
		seen[name] = true
		switch f[1] {
		case "100644", "100755", "120000", "160000":
		default:
			return nil, ErrWorktreeUnsupported
		}
		budget.paths++
		if budget.paths > worktreePathLimit {
			return nil, ErrWorktreeLimit
		}
		n.index = append(n.index, []byte(strings.Join(f[1:], " ")+"\t"+name+"\x00")...)
		n.entries = append(n.entries, worktreeEntry{mode: f[1], oid: f[2], name: name})
		names.WriteString(name)
		names.WriteByte(0)
	}
	err = n.readConfig(ctx)
	if err != nil {
		return nil, err
	}
	if len(inherited) > 0 {
		n.ignores[""] = inherited[0]
	}
	if names.Len() > 0 {
		n.attrs, err = historyRead(ctx, id.Root, names.Bytes(), "check-attr", "-z", "--stdin", "filter", "ident", "working-tree-encoding", "text", "eol", "diff")
		if err != nil {
			return nil, err
		}
		parts := bytes.Split(n.attrs, []byte{0})
		if len(parts) != len(n.entries)*18+1 || len(parts[len(parts)-1]) != 0 {
			return nil, ErrWorktreeOutput
		}
		var rules strings.Builder
		for i, e := range n.entries {
			chunk := bytes.Join(parts[i*18:i*18+15], []byte{0})
			chunk = append(chunk, 0)
			if e.mode == "120000" || e.mode == "160000" {
				continue
			}
			for j := 0; j < 3; j++ {
				v := string(parts[i*18+j*3+2])
				if v != "unspecified" && v != "unset" {
					return nil, fmt.Errorf("Git working tree comparison does not support the %s attribute", string(parts[i*18+j*3+1]))
				}
			}
			rule, err := blameAttributes(e.name, chunk)
			if err != nil {
				return nil, err
			}
			if string(parts[i*18+15]) != e.name || string(parts[i*18+16]) != "diff" {
				return nil, ErrWorktreeOutput
			}
			switch string(parts[i*18+17]) {
			case "unspecified":
			case "unset":
				rule = strings.Replace(rule, "!diff", "-diff", 1)
			case "set":
				rule = strings.Replace(rule, "!diff", "diff", 1)
			default:
				return nil, fmt.Errorf("Git working tree comparison does not support the %s attribute", "diff")
			}
			pattern := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]").Replace("/" + e.name)
			rules.WriteString(strconv.Quote(pattern))
			rules.WriteString(strings.TrimPrefix(rule, "*"))
		}
		n.attributeFile = rules.String()
	}
	// 子模块的未跟踪目录只影响 dirty 位，不读取正文；ls-files 不会启动子模块 status。
	if depth > 0 {
		n.untracked, err = historyRead(ctx, id.Root, nil, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, err
		}
		if len(n.untracked) > 0 && n.untracked[len(n.untracked)-1] != 0 {
			return nil, ErrWorktreeOutput
		}
	}
	r, err := os.OpenRoot(id.Root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	for i := range n.entries {
		e := &n.entries[i]
		e.file, err = worktreeVersion(ctx, r, id.Root, e.name, budget)
		if err != nil {
			return nil, err
		}
		if e.mode != "160000" || e.file.Kind != "directory" {
			continue
		}
		e.file.Kind, e.file.OID = "gitlink", e.oid
		_, statErr := r.Lstat(filepath.Join(e.name, ".git"))
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return nil, statErr
		}
		childID, err := RepositoryIdentityChecked(ctx, filepath.Join(id.Root, e.name))
		if err != nil {
			return nil, err
		}
		if childID.Root != filepath.Join(id.Root, e.name) {
			return nil, ErrWorktreeUnsupported
		}
		if ignore := n.ignore(e.name); ignore == "all" || ignore == "dirty" {
			e.file.OID, err = compareRevision(ctx, childID.Root, "HEAD")
			if err != nil {
				return nil, err
			}
			identity, _ := json.Marshal(childID)
			sum := sha256.Sum256(append(identity, []byte(e.file.OID)...))
			e.file.Version.Hash = hex.EncodeToString(sum[:])
			continue
		}
		e.child, err = readWorktreeNode(ctx, childID, depth+1, budget, n.ignore(e.name))
		if err != nil {
			return nil, err
		}
		if e.child.head == "" {
			return nil, ErrWorktreeUnsupported
		}
		e.file.OID = e.child.head
		e.file.Version.Hash = e.child.digest
	}
	state := struct {
		ID                              RepositoryIdentity
		Head                            string
		Index, Attrs, Config, Untracked []byte
		Files                           []WorktreeFile
	}{id, n.head, n.index, n.attrs, n.config, n.untracked, nil}
	for _, e := range n.entries {
		state.Files = append(state.Files, e.file)
	}
	encoded, _ := json.Marshal(state)
	sum := sha256.Sum256(encoded)
	n.digest = hex.EncodeToString(sum[:])
	return n, nil
}

func worktreeStamp(fi os.FileInfo) files.Stamp {
	s := files.Stamp{Size: fi.Size(), ModTime: fi.ModTime(), Mode: fi.Mode()}
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		s.Ino = uint64(stat.Ino)
	}
	return s
}

func worktreeVersion(ctx context.Context, r *os.Root, root, name string, budget *worktreeBudget) (WorktreeFile, error) {
	result := WorktreeFile{Version: files.Version{Path: filepath.Join(root, name)}}
	before, err := r.Lstat(name)
	if os.IsNotExist(err) {
		result.Kind = "missing"
		result.Version.Stamp.Missing = true
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Version.Stamp = worktreeStamp(before)
	h := sha256.New()
	switch {
	case before.Mode().IsRegular():
		result.Kind = "file"
		if before.Size() > budget.bytes {
			return result, ErrWorktreeLimit
		}
		f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return result, err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil {
			return result, err
		}
		if !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
			return result, ErrWorktreeChanged
		}
		var size int64
		for {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			n, readErr := f.Read(budget.buffer[:])
			size += int64(n)
			budget.bytes -= int64(n)
			if budget.bytes < 0 {
				return result, ErrWorktreeLimit
			}
			h.Write(budget.buffer[:n])
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return result, readErr
			}
		}
		closed, err := f.Stat()
		if err != nil {
			return result, err
		}
		if size != before.Size() || !result.Version.Stamp.Same(worktreeStamp(closed)) || before.Mode() != closed.Mode() {
			return result, ErrWorktreeChanged
		}
	case before.Mode()&os.ModeSymlink != 0:
		result.Kind = "symlink"
		result.Link, err = r.Readlink(name)
		if err != nil {
			return result, err
		}
		if !utf8.ValidString(result.Link) {
			return result, ErrWorktreeUnsupported
		}
		h.Write([]byte(result.Link))
	case before.IsDir():
		result.Kind = "directory"
	default:
		return result, ErrWorktreeUnsupported
	}
	after, err := r.Lstat(name)
	if err != nil {
		return result, err
	}
	if !result.Version.Stamp.Same(worktreeStamp(after)) || before.Mode() != after.Mode() {
		return result, ErrWorktreeChanged
	}
	result.Version.Hash = hex.EncodeToString(h.Sum(nil))
	return result, nil
}
