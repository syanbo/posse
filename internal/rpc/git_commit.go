package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"posse/internal/files"
	"posse/internal/gitx"
)

const (
	GitCommitParamsLimit  = FileSaveRequestLimit + 6*GitCommitMessageLimit
	GitCommitResultLimit  = GitFetchResultLimit
	GitCommitMessageLimit = 1 << 20
	GitCommitPaths        = "paths"
	GitCommitWhole        = "whole"
	GitCommitChange       = "change"
)

type GitCommitParams struct {
	GitFetchParams
	Action      string    `json:"action"`
	Head        string    `json:"head"`
	IndexID     string    `json:"indexID"`
	Message     string    `json:"message,omitempty"`
	MessageHash string    `json:"messageHash"`
	Amend       bool      `json:"amend,omitempty"`
	Paths       []GitPath `json:"paths,omitempty"`
	Add         []string  `json:"add,omitempty"`
	Path        string    `json:"path,omitempty"`
	Data        []byte    `json:"data,omitempty"`
	Hash        string    `json:"hash,omitempty"`
}

type GitCommitResult struct {
	Target        GitCommitParams `json:"target"`
	Status        string          `json:"status"`
	Started       bool            `json:"started"`
	Error         *Error          `json:"error,omitempty"`
	Logs          []gitx.Run      `json:"logs"`
	LogsTruncated bool            `json:"logsTruncated"`
}

// 操作缓存保留说明和正文的摘要，避免大说明挤掉可查询的终态。
func (p GitCommitParams) Metadata() GitCommitParams { p.Message = ""; p.Data = nil; return p }

func GitCommitHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func validCommitHash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && strings.ToLower(s) == s
}

func ValidGitCommitTarget(p GitCommitParams) bool {
	if p.Message != "" && (strings.TrimSpace(p.Message) == "" || len(p.Message) > GitCommitMessageLimit || !utf8.ValidString(p.Message) || strings.ContainsRune(p.Message, 0) || GitCommitHash([]byte(p.Message)) != p.MessageHash) {
		return false
	}
	if len(p.Data) > files.MaxSize || len(p.Data) > 0 && GitCommitHash(p.Data) != p.Hash {
		return false
	}
	if !ValidGitFetchParams(p.GitFetchParams) || p.Head != "" && !ValidGitBranchOID(p.Head) || !validCommitHash(p.IndexID) || !validCommitHash(p.MessageHash) {
		return false
	}
	seen := map[string]bool{}
	for _, item := range p.Paths {
		e := item.Entry
		if !ValidGitPath(e.Path) || seen[e.Path] || e.Submodule || !validPathVersion(item.Version) {
			return false
		}
		seen[e.Path] = true
		if e.Orig != "" {
			if !ValidGitPath(e.Orig) || seen[e.Orig] || !validPathVersion(item.OrigVersion) {
				return false
			}
			seen[e.Orig] = true
		} else if item.OrigVersion != nil {
			return false
		}
	}
	added := map[string]bool{}
	for _, path := range p.Add {
		if !seen[path] || added[path] || !slices.ContainsFunc(p.Paths, func(item GitPath) bool { return item.Entry.Path == path && item.Entry.Untracked }) {
			return false
		}
		added[path] = true
	}
	switch p.Action {
	case GitCommitPaths:
		if len(p.Paths) == 0 && !p.Amend {
			return false
		}
		for _, item := range p.Paths {
			if item.Entry.Conflict {
				return false
			}
			if slices.Contains(p.Add, item.Entry.Path) && !item.Entry.Untracked {
				return false
			}
		}
	case GitCommitWhole:
		if len(p.Add) != 0 {
			return false
		}
	case GitCommitChange:
		if p.Amend || len(p.Paths) != 0 || len(p.Add) != 0 || !ValidGitPath(p.Path) || p.Head == "" || !validCommitHash(p.Hash) {
			return false
		}
	default:
		return false
	}
	if p.Action != GitCommitChange && (p.Path != "" || len(p.Data) != 0 || p.Hash != "") {
		return false
	}
	raw, err := json.Marshal(p.Metadata())
	return err == nil && len(raw) <= GitFetchParamsLimit
}

func ValidGitCommitParams(p GitCommitParams) bool {
	if !ValidGitCommitTarget(p) || strings.TrimSpace(p.Message) == "" || len(p.Message) > GitCommitMessageLimit || !utf8.ValidString(p.Message) || strings.ContainsRune(p.Message, 0) || GitCommitHash([]byte(p.Message)) != p.MessageHash || len(p.Data) > files.MaxSize {
		return false
	}
	if p.Action == GitCommitChange && GitCommitHash(p.Data) != p.Hash {
		return false
	}
	raw, err := json.Marshal(p)
	return err == nil && len(raw) <= GitCommitParamsLimit
}

func (p GitCommitParams) WorktreePaths() []string {
	var paths []string
	for _, item := range p.Paths {
		paths = append(paths, item.Entry.Path)
		if item.Entry.Orig != "" {
			paths = append(paths, item.Entry.Orig)
		}
	}
	if p.Action == GitCommitChange {
		paths = append(paths, p.Path)
	}
	return paths
}

// paths 只预先加入显式的新文件，whole 则暂存全部勾选路径后提交整个索引。
func GitCommitCommands(p GitCommitParams) [][]string {
	if p.Action == GitCommitChange {
		return nil
	}
	args := func(prefix, paths []string) []string {
		out := append(append([]string{}, prefix...), "--")
		for _, path := range paths {
			out = append(out, ":(literal)"+path)
		}
		return out
	}
	add := p.Add
	if p.Action == GitCommitWhole {
		add = p.WorktreePaths()
	}
	var out [][]string
	if len(add) > 0 {
		out = append(out, args([]string{"add", "-A"}, add))
	}
	commit := []string{"commit", "-F", "-"}
	if p.Action == GitCommitPaths {
		commit = append(commit, "--only")
	}
	if p.Amend {
		commit = append(commit, "--amend")
	}
	if p.Action == GitCommitPaths {
		commit = args(commit, p.WorktreePaths())
	}
	return append(out, commit)
}
