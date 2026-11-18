package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"unicode/utf8"

	"posse/internal/files"
	"posse/internal/gitx"
)

const (
	GitPathsParamsLimit = FileSaveRequestLimit
	GitPathsResultLimit = GitFetchResultLimit
	GitPathsStage       = "stage"
	GitPathsResolve     = "resolve"
	GitPathsUnstage     = "unstage"
	GitPathsRollback    = "rollback"
	GitPathsTake        = "take"
	GitPathsWrite       = "write"
)

type GitPath struct {
	Entry       gitx.Entry     `json:"entry"`
	Version     *files.Version `json:"version,omitempty"`
	OrigVersion *files.Version `json:"origVersion,omitempty"`
}

type GitPathsParams struct {
	GitFetchParams
	Action  string    `json:"action"`
	Head    string    `json:"head"`
	Paths   []GitPath `json:"paths,omitempty"`
	Restore []GitPath `json:"restore,omitempty"`
	Unstage []GitPath `json:"unstage,omitempty"`
	Remove  []GitPath `json:"remove,omitempty"`
	Ours    bool      `json:"ours,omitempty"`
	Data    []byte    `json:"data,omitempty"`
	Hash    string    `json:"hash,omitempty"`
}

type GitPathsResult struct {
	Target        GitPathsParams `json:"target"`
	Status        string         `json:"status"`
	Started       bool           `json:"started"`
	Version       *files.Version `json:"version,omitempty"`
	Error         *Error         `json:"error,omitempty"`
	Logs          []gitx.Run     `json:"logs"`
	LogsTruncated bool           `json:"logsTruncated"`
}

// 结果保留摘要和全部前提，不重复缓存可能很大的正文。
func (p GitPathsParams) Metadata() GitPathsParams { p.Data = nil; return p }

func ValidGitPath(pathname string) bool {
	if pathname == "" || pathname == "." || path.IsAbs(pathname) || path.Clean(pathname) != pathname || strings.ContainsAny(pathname, "\\\x00") || !utf8.ValidString(pathname) || len(pathname) > 4096 {
		return false
	}
	for _, part := range strings.Split(pathname, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func GitPathCategory(e gitx.Entry) string {
	switch {
	case e.Conflict:
		return "conflict"
	case e.Untracked:
		return "untracked"
	case e.X == 'A':
		return "added"
	default:
		return "changed"
	}
}

func validPathVersion(v *files.Version) bool {
	if v == nil || v.Path == "" || !strings.HasPrefix(v.Path, "/") {
		return false
	}
	if v.Stamp.Missing {
		return v.Hash == ""
	}
	hash, err := hex.DecodeString(v.Hash)
	return err == nil && len(hash) == sha256.Size && v.Stamp.Mode.IsRegular() && v.Stamp.Size >= 0 && v.Stamp.Size <= files.MaxSize
}

func ValidGitPathsTarget(p GitPathsParams) bool {
	if !ValidGitFetchParams(p.GitFetchParams) || p.Head != "" && !ValidGitBranchOID(p.Head) || p.Ours && p.Action != GitPathsTake {
		return false
	}
	seen := map[string]bool{}
	check := func(ps []GitPath, action string, destructive bool) bool {
		for _, item := range ps {
			e := item.Entry
			if !ValidGitPath(e.Path) || seen[e.Path] || e.Submodule || e.Orig != "" && (!ValidGitPath(e.Orig) || seen[e.Orig] || e.Orig == e.Path) {
				return false
			}
			seen[e.Path] = true
			if e.Orig != "" {
				seen[e.Orig] = true
			}
			cat := GitPathCategory(e)
			switch action {
			case GitPathsStage:
				if cat != "untracked" {
					return false
				}
			case GitPathsResolve, GitPathsTake:
				if cat != "conflict" {
					return false
				}
			case "restore":
				if cat != "changed" || p.Head == "" {
					return false
				}
			case "remove":
				if cat != "untracked" && cat != "added" {
					return false
				}
			case "added":
				if cat != "added" {
					return false
				}
			case GitPathsUnstage:
				if cat == "untracked" || e.X == '.' {
					return false
				}
			case GitPathsWrite:
				if cat == "conflict" {
					return false
				}
			}
			if item.OrigVersion != nil && (action != "restore" || e.Orig == "") {
				return false
			}
			if destructive {
				if !validPathVersion(item.Version) || e.Orig != "" && action == "restore" && !validPathVersion(item.OrigVersion) {
					return false
				}
			} else if item.Version != nil || item.OrigVersion != nil {
				return false
			}
		}
		return true
	}
	if p.Action == GitPathsRollback {
		if len(p.Paths) != 0 || len(p.Restore)+len(p.Unstage)+len(p.Remove) == 0 || !check(p.Restore, "restore", true) || !check(p.Unstage, "added", false) || !check(p.Remove, "remove", true) {
			return false
		}
	} else {
		if len(p.Paths) == 0 || len(p.Restore)+len(p.Unstage)+len(p.Remove) != 0 {
			return false
		}
		switch p.Action {
		case GitPathsStage, GitPathsResolve, GitPathsUnstage:
		case GitPathsTake, GitPathsWrite:
			if len(p.Paths) != 1 {
				return false
			}
		default:
			return false
		}
		if !check(p.Paths, p.Action, p.Action == GitPathsTake || p.Action == GitPathsWrite) {
			return false
		}
	}
	if p.Action == GitPathsWrite {
		hash, err := hex.DecodeString(p.Hash)
		if err != nil || len(hash) != sha256.Size {
			return false
		}
	} else if len(p.Data) > 0 || p.Hash != "" {
		return false
	}
	raw, err := json.Marshal(p.Metadata())
	return err == nil && len(raw) <= GitFetchParamsLimit
}

func ValidGitPathsParams(p GitPathsParams) bool {
	if !ValidGitPathsTarget(p) || len(p.Data) > files.MaxSize {
		return false
	}
	if p.Action == GitPathsWrite {
		sum := sha256.Sum256(p.Data)
		if p.Hash != hex.EncodeToString(sum[:]) {
			return false
		}
	}
	raw, err := json.Marshal(p)
	return err == nil && len(raw) <= GitPathsParamsLimit
}

func (p GitPathsParams) Items() []GitPath {
	out := append([]GitPath{}, p.Paths...)
	out = append(out, p.Restore...)
	out = append(out, p.Unstage...)
	return append(out, p.Remove...)
}

// 每一步只使用显式的字面路径；删除命令逐个执行，成功才删除对应工作区文件。
func GitPathsCommands(p GitPathsParams) [][]string {
	args := func(prefix []string, items []GitPath, orig bool) []string {
		out := append(append([]string{}, prefix...), "--")
		for _, item := range items {
			out = append(out, ":(literal)"+item.Entry.Path)
			if orig && item.Entry.Orig != "" {
				out = append(out, ":(literal)"+item.Entry.Orig)
			}
		}
		return out
	}
	unstage := []string{"restore", "--staged"}
	if p.Head == "" {
		unstage = []string{"rm", "--cached", "-r", "-q"}
	}
	switch p.Action {
	case GitPathsStage, GitPathsResolve:
		return [][]string{args([]string{"add", "-A"}, p.Paths, false)}
	case GitPathsUnstage:
		return [][]string{args(unstage, p.Paths, true)}
	case GitPathsTake:
		side := "--theirs"
		if p.Ours {
			side = "--ours"
		}
		return [][]string{args([]string{"checkout", side}, p.Paths, false), args([]string{"add"}, p.Paths, false)}
	case GitPathsRollback:
		var out [][]string
		if len(p.Restore) > 0 {
			out = append(out, args([]string{"restore", "--source=HEAD", "--staged", "--worktree"}, p.Restore, true))
		}
		if len(p.Unstage) > 0 {
			out = append(out, args(unstage, p.Unstage, false))
		}
		for _, item := range p.Remove {
			out = append(out, args([]string{"rm", "--cached", "-q", "--ignore-unmatch"}, []GitPath{item}, false))
		}
		return out
	}
	return nil
}
