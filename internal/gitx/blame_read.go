package gitx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"posse/internal/files"
)

const (
	BlameReadSizeLimit = 1 << 20
	BlameReadLineLimit = 10000
)

var (
	ErrBlameQuery   = errors.New("invalid Git blame query")
	ErrBlameOutput  = errors.New("invalid Git blame output")
	ErrBlameChanged = errors.New("Git blame inputs changed; reload before trying again")
	ErrBlameNotText = errors.New("Git blame requires a regular UTF-8 text file without NUL")
	ErrBlameNoHead  = errors.New("Git blame requires an existing HEAD commit")
)

type BlameRead struct {
	Head    string
	Version files.Version
	Lines   []BlameLine
}

type blameInputs struct {
	head                          string
	index, attrs, config, shallow []byte
}

func (b blameInputs) same(other blameInputs) bool {
	return b.head == other.head && bytes.Equal(b.index, other.index) && bytes.Equal(b.attrs, other.attrs) && bytes.Equal(b.config, other.config) && bytes.Equal(b.shallow, other.shallow)
}

func ValidBlameFile(name string) bool {
	return utf8.ValidString(name) && ValidFileQuery(FileIndex, "", name)
}

func readBlameInputs(ctx context.Context, dir, name string) (blameInputs, error) {
	var state blameInputs
	exists, err := HeadExistsChecked(context.WithValue(ctx, readStrictKey{}, true), dir)
	if err != nil {
		return state, err
	}
	if !exists {
		return state, ErrBlameNoHead
	}
	head, err := historyRead(ctx, dir, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return state, err
	}
	state.head = strings.TrimSuffix(string(head), "\n")
	if !ValidBlobOID(state.head) {
		return state, ErrBlameOutput
	}
	state.index, err = historyRead(ctx, dir, nil, "--literal-pathspecs", "ls-files", "--stage", "-z", "--", name)
	if err != nil {
		return state, err
	}
	if len(state.index) > 0 {
		if state.index[len(state.index)-1] != 0 {
			return state, ErrBlameOutput
		}
		records := bytes.Split(state.index[:len(state.index)-1], []byte{0})
		for _, record := range records {
			meta, path, ok := strings.Cut(string(record), "\t")
			if !ok || path != name {
				return state, ErrBlameOutput
			}
			fields := strings.Fields(meta)
			if len(fields) != 3 || !ValidBlobOID(fields[1]) || len(fields[1]) != len(state.head) {
				return state, ErrBlameOutput
			}
			if fields[2] != "0" {
				return state, ErrFileUnmerged
			}
			if fields[0] != "100644" && fields[0] != "100755" {
				return state, ErrBlameNotText
			}
		}
		if len(records) != 1 {
			return state, ErrBlameOutput
		}
	}
	state.attrs, err = historyRead(ctx, dir, []byte(name+"\x00"), "check-attr", "-z", "--stdin", "filter", "ident", "working-tree-encoding", "text", "eol")
	if err != nil {
		return state, err
	}
	if _, err := blameAttributes(name, state.attrs); err != nil {
		return state, err
	}
	state.config, err = historyRead(ctx, dir, nil, "config", "--null", "--get-regexp", `^core\.(autocrlf|eol|safecrlf)$`)
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return state, err
		}
		state.config = nil
	}
	if _, err := blameConfigArgs(state.config); err != nil {
		return state, err
	}
	state.shallow, err = historyShallow(ctx, dir)
	return state, err
}

func blameAttributes(name string, raw []byte) (string, error) {
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return "", ErrBlameOutput
	}
	parts := bytes.Split(raw[:len(raw)-1], []byte{0})
	if len(parts) != 15 {
		return "", ErrBlameOutput
	}
	attributes := []string{"*", "!filter", "!diff", "!ident", "!working-tree-encoding"}
	for i, key := range []string{"filter", "ident", "working-tree-encoding", "text", "eol"} {
		if string(parts[i*3]) != name || string(parts[i*3+1]) != key {
			return "", ErrBlameOutput
		}
		value := string(parts[i*3+2])
		switch key {
		case "filter", "ident", "working-tree-encoding":
			if value != "unspecified" && value != "unset" {
				return "", fmt.Errorf("Git blame does not support the %s attribute", key)
			}
		case "text":
			switch value {
			case "unspecified":
			case "unset":
				attributes = append(attributes, "-text")
			case "set":
				attributes = append(attributes, "text")
			case "auto":
				attributes = append(attributes, "text=auto")
			default:
				return "", ErrBlameOutput
			}
		case "eol":
			switch value {
			case "unspecified":
			case "unset":
				attributes = append(attributes, "-eol")
			case "lf", "crlf":
				attributes = append(attributes, "eol="+value)
			default:
				return "", ErrBlameOutput
			}
		}
	}
	return strings.Join(attributes, " ") + "\n", nil
}

func blameConfigArgs(raw []byte) ([]string, error) {
	args := []string{"-c", "core.attributesFile=/dev/null"}
	if len(raw) == 0 {
		return args, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, ErrBlameOutput
	}
	for _, record := range bytes.Split(raw[:len(raw)-1], []byte{0}) {
		key, value, ok := strings.Cut(string(record), "\n")
		if !ok {
			return nil, ErrBlameOutput
		}
		switch key {
		case "core.autocrlf", "core.eol", "core.safecrlf":
		default:
			return nil, ErrBlameOutput
		}
		args = append(args, "-c", key+"="+value)
	}
	return args, nil
}

func blameWorktree(ctx context.Context, root, name string) ([]byte, files.Version, error) {
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return nil, files.Version{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, files.Version{}, ErrBlameNotText
	}
	r, err := files.ReadResourceRange(ctx, root, name, 0, BlameReadSizeLimit, nil)
	if err != nil {
		return nil, files.Version{}, err
	}
	if !r.EOF || r.Stamp.Size > BlameReadSizeLimit {
		return nil, files.Version{}, fmt.Errorf("Git blame file exceeds %d bytes", BlameReadSizeLimit)
	}
	if !utf8.Valid(r.Data) || bytes.IndexByte(r.Data, 0) >= 0 {
		return nil, files.Version{}, ErrBlameNotText
	}
	lines := bytes.Count(r.Data, []byte{'\n'})
	if len(r.Data) > 0 && r.Data[len(r.Data)-1] != '\n' {
		lines++
	}
	if lines > BlameReadLineLimit {
		return nil, files.Version{}, fmt.Errorf("Git blame file exceeds %d lines", BlameReadLineLimit)
	}
	version, err := files.ReadResourceVersion(ctx, root, name)
	if err != nil {
		return nil, files.Version{}, err
	}
	sum := sha256.Sum256(r.Data)
	if !r.Stamp.Same(version.Stamp) || r.Stamp.Mode != version.Stamp.Mode || hex.EncodeToString(sum[:]) != version.Hash {
		return nil, files.Version{}, ErrBlameChanged
	}
	return r.Data, version, nil
}

// --contents 仍会执行目标 clean/process；隔离目录只借用对象，固定 HEAD 和当前索引项。
// 临时 index/属性均由本次请求生成，不读取目标的可执行配置、ignoreRevsFile 或 grafts。
func isolatedBlame(ctx context.Context, id RepositoryIdentity, name string, input []byte, state blameInputs) ([]byte, error) {
	dir, err := os.MkdirTemp("", "posse-blame-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	gitDir := filepath.Join(dir, ".git")
	for _, sub := range []string{"objects/info", "refs", "info"} {
		if err := os.MkdirAll(filepath.Join(gitDir, sub), 0700); err != nil {
			return nil, err
		}
	}
	if err := os.Symlink(filepath.Join(id.CommonDir, "objects"), filepath.Join(gitDir, "source-objects")); err != nil {
		return nil, err
	}
	config := "[core]\nrepositoryformatversion = 0\nbare = false\n"
	if len(state.head) == 64 {
		config = "[core]\nrepositoryformatversion = 1\nbare = false\n[extensions]\nobjectformat = sha256\n"
	}
	attrs, err := blameAttributes(name, state.attrs)
	if err != nil {
		return nil, err
	}
	contents := map[string][]byte{"HEAD": []byte(state.head + "\n"), "config": []byte(config), "objects/info/alternates": []byte("../source-objects\n"), "info/attributes": []byte(attrs)}
	if len(state.shallow) > 0 {
		contents["shallow"] = state.shallow
	}
	for path, data := range contents {
		if err := os.WriteFile(filepath.Join(gitDir, path), data, 0600); err != nil {
			return nil, err
		}
	}
	ctx = context.WithValue(ctx, readIsolatedKey{}, true)
	if len(state.index) > 0 {
		if _, err := historyRead(ctx, dir, state.index, "update-index", "-z", "--index-info"); err != nil {
			return nil, err
		}
	}
	args, err := blameConfigArgs(state.config)
	if err != nil {
		return nil, err
	}
	args = append(args, "--literal-pathspecs", "blame", "--line-porcelain", "--no-textconv", "--no-ext-diff", "--encoding=UTF-8", "--contents", "-", "--", name)
	return historyRead(ctx, dir, input, args...)
}

func ReadBlameChecked(ctx context.Context, id RepositoryIdentity, name string) (BlameRead, error) {
	var result BlameRead
	if !ValidBlameFile(name) {
		return result, ErrBlameQuery
	}
	state, err := readBlameInputs(ctx, id.Root, name)
	if err != nil {
		return result, err
	}
	data, version, err := blameWorktree(ctx, id.Root, name)
	if err != nil {
		return result, err
	}
	out, readErr := isolatedBlame(ctx, id, name, data, state)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if errors.Is(readErr, ErrReadLimit) {
		return result, readErr
	}
	after, err := readBlameInputs(ctx, id.Root, name)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if errors.Is(err, ErrReadLimit) {
			return result, err
		}
		return result, ErrBlameChanged
	}
	_, afterVersion, err := blameWorktree(ctx, id.Root, name)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if errors.Is(err, ErrReadLimit) {
			return result, err
		}
		return result, ErrBlameChanged
	}
	if !state.same(after) || !version.Same(afterVersion) {
		return result, ErrBlameChanged
	}
	if readErr != nil {
		return result, readErr
	}
	lines, err := parseBlameChecked(out, len(state.head))
	if err != nil {
		return result, err
	}
	expected := blameTextLines(data)
	if len(lines) != len(expected) {
		return result, ErrBlameOutput
	}
	for i := range lines {
		want := expected[i]
		hasLF := i < len(expected)-1 || len(data) > 0 && data[len(data)-1] == '\n'
		if lines[i].Text != want && !(hasLF && strings.HasSuffix(want, "\r") && lines[i].Text == strings.TrimSuffix(want, "\r")) {
			return result, ErrBlameOutput
		}
		lines[i].Text = want
	}
	result = BlameRead{Head: state.head, Version: version, Lines: lines}
	return result, ctx.Err()
}

func blameTextLines(data []byte) []string {
	if len(data) == 0 {
		return []string{}
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}
