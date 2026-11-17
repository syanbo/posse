package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"posse/internal/files"
)

var (
	ErrWorktreeQuery       = errors.New("invalid Git working tree comparison query")
	ErrWorktreeOutput      = errors.New("invalid Git working tree comparison output")
	ErrWorktreeChanged     = errors.New("Git working tree comparison changed; refresh the comparison")
	ErrWorktreeUnsupported = errors.New("Git working tree comparison cannot safely read this index or file type")
	ErrWorktreeLimit       = errors.New("Git working tree comparison exceeds its scan limit")
)

type WorktreeFile struct {
	FileChange
	Kind     string        `json:"kind"`
	Version  files.Version `json:"version"`
	Link     string        `json:"link,omitempty"`
	OID      string        `json:"oid,omitempty"`
	Dirty    bool          `json:"dirty,omitempty"`
	BaseMode string        `json:"baseMode"`
	BaseOID  string        `json:"baseOID"`
}

type WorktreeComparison struct {
	Commit   string         `json:"commit"`
	Baseline string         `json:"baseline"`
	Files    []WorktreeFile `json:"files"`
}

func ValidWorktreeQuery(revision, baseline string) bool {
	return ValidReadRevision(revision) && (baseline == "" || ValidLogBaseline(baseline))
}

// 两遍原始内容摘要发现期间仍可观察到的外部修改；这不是对外部写入的原子快照。
func ReadWorktreeComparison(ctx context.Context, id RepositoryIdentity, revision, baseline string) (WorktreeComparison, error) {
	var result WorktreeComparison
	if !ValidWorktreeQuery(revision, baseline) {
		return result, ErrWorktreeQuery
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	commit, err := compareRevision(ctx, id.Root, revision)
	if err != nil {
		return result, err
	}
	budget := &worktreeBudget{bytes: WorktreeScanLimit}
	before, err := readWorktreeNode(ctx, id, 0, budget)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256([]byte(commit + "\x00" + before.digest))
	result.Baseline = hex.EncodeToString(sum[:])
	if baseline != "" && baseline != result.Baseline {
		return WorktreeComparison{}, ErrWorktreeChanged
	}
	tree, err := worktreeTree(ctx, id.Root, commit)
	if err != nil {
		return WorktreeComparison{}, err
	}
	changes, readErr := worktreeDiff(ctx, before, commit)
	if ctx.Err() != nil {
		return WorktreeComparison{}, ctx.Err()
	}
	if errors.Is(readErr, ErrReadLimit) || errors.Is(readErr, ErrWorktreeLimit) {
		return WorktreeComparison{}, readErr
	}
	after, err := readWorktreeNode(ctx, id, 0, budget)
	if err != nil {
		return WorktreeComparison{}, worktreeCheckError(ctx, err)
	}
	afterCommit, err := compareRevision(ctx, id.Root, revision)
	if err != nil {
		return WorktreeComparison{}, worktreeCheckError(ctx, err)
	}
	if afterCommit != commit || before.digest != after.digest {
		return WorktreeComparison{}, ErrWorktreeChanged
	}
	if readErr != nil {
		return WorktreeComparison{}, readErr
	}
	entries := map[string]WorktreeFile{}
	for _, e := range before.entries {
		entries[e.name] = e.file
	}
	result.Commit, result.Files = commit, []WorktreeFile{}
	for _, change := range changes {
		file, ok := entries[change.Path]
		if !ok {
			file = WorktreeFile{Kind: "missing", Version: files.Version{Path: filepath.Join(id.Root, change.Path), Stamp: files.Stamp{Missing: true}}}
		}
		file.FileChange = change
		oldPath := change.Path
		if change.Orig != "" {
			oldPath = change.Orig
		}
		old := tree[oldPath]
		file.BaseMode, file.BaseOID = old.mode, old.oid
		if change.Status == 'A' && (old.mode != "" || old.oid != "") {
			return WorktreeComparison{}, ErrWorktreeOutput
		}
		if change.Status != 'A' && old.mode == "" {
			return WorktreeComparison{}, ErrWorktreeOutput
		}
		result.Files = append(result.Files, file)
	}
	return result, ctx.Err()
}

func worktreeCheckError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrReadLimit) || errors.Is(err, ErrWorktreeLimit) {
		return err
	}
	return ErrWorktreeChanged
}
