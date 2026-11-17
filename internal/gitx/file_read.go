package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"posse/internal/files"
)

type FileSource string

const (
	FileCommit FileSource = "commit"
	FileIndex  FileSource = "index"
)

var (
	ErrFileQuery    = errors.New("invalid Git file query")
	ErrFileNotBlob  = errors.New("Git file query refers to a directory or submodule")
	ErrFileUnmerged = errors.New("Git index file has unresolved conflicts")
	ErrBlobResponse = errors.New("invalid Git blob response")
)

type FileVersion struct {
	Source   FileSource `json:"source"`
	Revision string     `json:"revision"`
	Commit   string     `json:"commit,omitempty"`
	Path     string     `json:"path"`
	Missing  bool       `json:"missing"`
	Unborn   bool       `json:"unborn"`
	OID      string     `json:"oid,omitempty"`
	Size     int64      `json:"size"`
	Binary   bool       `json:"binary"`
}

func ValidFileQuery(source FileSource, revision, name string) bool {
	if name == "" || len(name) > 4096 || strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	if source == FileIndex {
		return revision == ""
	}
	return source == FileCommit && ValidReadRevision(revision)
}

func ValidBlobOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ResolveFile 固定 commit/index 的 blob；后续读取只用该 OID，不再解析会变化的引用。
func ResolveFile(ctx context.Context, dir string, source FileSource, revision, name string) (FileVersion, error) {
	v := FileVersion{Source: source, Revision: revision, Path: name}
	if !ValidFileQuery(source, revision, name) {
		return v, ErrFileQuery
	}
	env := []string{"GIT_NO_REPLACE_OBJECTS=1"}
	var out []byte
	var err error
	if source == FileCommit {
		if revision == "HEAD" {
			exists, e := HeadExistsChecked(ctx, dir)
			if e != nil {
				return v, e
			}
			if !exists {
				v.Missing = true
				v.Unborn = true
				return v, nil
			}
		}
		b, e := runRead(ctx, dir, nil, env, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
		if e != nil {
			return v, e
		}
		v.Commit = strings.TrimSuffix(string(b), "\n")
		if !ValidBlobOID(v.Commit) {
			return v, ErrBlobResponse
		}
		out, err = runRead(ctx, dir, nil, env, "--literal-pathspecs", "ls-tree", "-z", v.Commit, "--", name)
	} else {
		out, err = runRead(ctx, dir, nil, env, "--literal-pathspecs", "ls-files", "--stage", "-z", "--", name)
	}
	if err != nil {
		return v, err
	}
	if len(out) == 0 {
		v.Missing = true
		return v, nil
	}
	if out[len(out)-1] != 0 {
		return v, ErrBlobResponse
	}
	records := bytes.Split(out[:len(out)-1], []byte{0})
	found := false
	for _, record := range records {
		meta, p, ok := strings.Cut(string(record), "\t")
		if !ok {
			return v, ErrBlobResponse
		}
		f := strings.Fields(meta)
		if len(f) != 3 || !ValidBlobOID(f[1]) && source == FileIndex {
			return v, ErrBlobResponse
		}
		if p != name {
			return v, ErrFileNotBlob
		}
		if f[0] == "160000" || f[0] == "040000" {
			return v, ErrFileNotBlob
		}
		if f[0] != "100644" && f[0] != "100755" && f[0] != "120000" {
			return v, ErrFileNotBlob
		}
		if source == FileIndex {
			if f[2] != "0" {
				return v, ErrFileUnmerged
			}
			v.OID = f[1]
		} else {
			if f[1] != "blob" {
				return v, ErrFileNotBlob
			}
			v.OID = f[2]
		}
		if found || !ValidBlobOID(v.OID) {
			return v, ErrBlobResponse
		}
		found = true
	}
	v.Size, err = BlobSize(ctx, dir, v.OID)
	if err != nil {
		return v, err
	}
	probe, err := ReadBlobRange(ctx, dir, v.OID, v.Size, 0, 8192)
	if err != nil {
		return v, err
	}
	v.Binary = files.IsBinary(probe)
	return v, nil
}

func BlobSize(ctx context.Context, dir, oid string) (int64, error) {
	if !ValidBlobOID(oid) {
		return 0, ErrFileQuery
	}
	b, err := runRead(ctx, dir, []byte(oid+"\n"), []string{"GIT_NO_REPLACE_OBJECTS=1"}, "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) != 3 || f[0] != oid || f[1] != "blob" {
		return 0, ErrBlobResponse
	}
	size, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil || size < 0 {
		return 0, ErrBlobResponse
	}
	if size > files.MaxSize {
		return 0, fmt.Errorf("file exceeds the maximum size of %d bytes", files.MaxSize)
	}
	return size, nil
}

type blobRangeWriter struct {
	offset, end, total int64
	data               []byte
}

func (w *blobRangeWriter) Write(p []byte) (int, error) {
	start, end := max(w.offset, w.total), min(w.end, w.total+int64(len(p)))
	if start < end {
		w.data = append(w.data, p[start-w.total:end-w.total]...)
	}
	w.total += int64(len(p))
	return len(p), nil
}

// cat-file 不运行 filters/textconv；只保留请求区间，其余输出流式丢弃。
func ReadBlobRange(ctx context.Context, dir, oid string, size, offset int64, limit int) ([]byte, error) {
	if !ValidBlobOID(oid) || size < 0 || size > files.MaxSize || offset < 0 || offset > size || limit < 1 || limit > files.RangeLimit {
		return nil, ErrFileQuery
	}
	w := &blobRangeWriter{offset: offset, end: min(size, offset+int64(limit)), data: make([]byte, 0, min(int64(limit), size-offset))}
	_, err := runReadOutput(ctx, dir, nil, []string{"GIT_NO_REPLACE_OBJECTS=1"}, w, int(size)+1, "cat-file", "blob", oid)
	if err != nil {
		return nil, err
	}
	if w.total != size {
		return nil, ErrBlobResponse
	}
	return w.data, nil
}
