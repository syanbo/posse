package files

import (
	"encoding/json"
	"path/filepath"
)

// repos 的 nil 和空切片含义不同，不能用 omitempty；dirs 可从文件路径重建。
type listJSON struct {
	Root      string
	Files     []string
	Git       bool
	Truncated bool
	Repos     []string
	InRepo    bool
}

func (l List) MarshalJSON() ([]byte, error) {
	return json.Marshal(listJSON{l.Root, l.Files, l.Git, l.Truncated, l.repos, l.inRepo})
}

func (l *List) UnmarshalJSON(data []byte) error {
	var v listJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*l = List{Root: v.Root, Files: v.Files, Git: v.Git, Truncated: v.Truncated, repos: v.Repos, inRepo: v.InRepo}
	if l.Git {
		l.dirs = make(map[string]bool)
		for _, f := range l.Files {
			for d := filepath.Dir(f); d != "." && d != string(filepath.Separator) && !l.dirs[d]; d = filepath.Dir(d) {
				l.dirs[d] = true
			}
		}
	}
	return nil
}
