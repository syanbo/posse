package history

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// 技能页的跨会话用量：每个技能、每个 MCP 服务一共用了几次、在几个会话里、最后一次什么时候、各家各几次。
// 认技能和 MCP 调用沿用「技能与工具」面板的规则（UsageScanner），按会话文件缓存结果，大小和修改时间没变的不重读；
// 只有技能页打开时才算，不进历史索引（索引的表结构一改，跨版本的旧常驻进程就要重启）。

// UseStats 是技能和 MCP 服务的用量，按名字。
type UseStats struct {
	Skills map[string]*SkillStat
	MCP    map[string]*SkillStat
}

// SkillStat 是一个技能（或一个 MCP 服务）在所有会话里的用量。
type SkillStat struct {
	Name     string         `json:"name"`
	Count    int            `json:"count"`
	Sessions int            `json:"sessions"`
	Last     time.Time      `json:"last"`
	ByAgent  map[string]int `json:"byAgent"`
}

// skillStatsVersion 是缓存的格式和认技能的规则版本，变了整份重算。
const skillStatsVersion = 3

type skillCache struct {
	Version int                       `json:"version"`
	Files   map[string]skillCacheFile `json:"files"`
}

type skillCacheFile struct {
	Agent  string               `json:"agent"`
	Size   int64                `json:"size"`
	Mtime  int64                `json:"mtime"`
	Skills map[string]skillUses `json:"skills,omitempty"`
	MCP    map[string]skillUses `json:"mcp,omitempty"`
}

type skillUses struct {
	N    int   `json:"n"`
	Last int64 `json:"last"` // 毫秒；会话文件不带时间（grok、cursor）时是文件的修改时间
}

// SkillStats 统计各家所有会话里的技能和 MCP 用量。cache 是缓存文件的路径（空时不缓存）；progress 报已看过的文件数和总数。
func SkillStats(ctx context.Context, roots Roots, cache string, progress func(done, total int)) (*UseStats, error) {
	c := skillCache{Version: skillStatsVersion, Files: map[string]skillCacheFile{}}
	if cache != "" {
		if b, err := os.ReadFile(cache); err == nil {
			var old skillCache
			if json.Unmarshal(b, &old) == nil && old.Version == skillStatsVersion && old.Files != nil {
				c.Files = old.Files
			}
		}
	}
	sources := discover(roots)
	seen := make(map[string]bool, len(sources))
	changed := false
	for i, src := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seen[src.transcript] = true
		fi, err := os.Stat(src.transcript)
		if err != nil {
			continue
		}
		f, ok := c.Files[src.transcript]
		if !ok || f.Size != fi.Size() || f.Mtime != fi.ModTime().UnixMilli() {
			c.Files[src.transcript] = scanSkillUses(src.agent, src.transcript, fi)
			changed = true
		}
		if progress != nil && (i%50 == 0 || i == len(sources)-1) {
			progress(i+1, len(sources))
		}
	}
	for path := range c.Files {
		if !seen[path] {
			delete(c.Files, path)
			changed = true
		}
	}
	if changed && cache != "" {
		if b, err := json.Marshal(c); err == nil {
			// 临时文件名各不相同：技能页关了又开，上一次还没算完的也会写
			os.MkdirAll(filepath.Dir(cache), 0o755)
			if tmp, err := os.CreateTemp(filepath.Dir(cache), filepath.Base(cache)+".*"); err == nil {
				_, werr := tmp.Write(b)
				tmp.Close()
				if werr != nil || os.Rename(tmp.Name(), cache) != nil {
					os.Remove(tmp.Name())
				}
			}
		}
	}
	out := &UseStats{Skills: map[string]*SkillStat{}, MCP: map[string]*SkillStat{}}
	add := func(m map[string]*SkillStat, agent string, uses map[string]skillUses) {
		for name, u := range uses {
			st := m[name]
			if st == nil {
				st = &SkillStat{Name: name, ByAgent: map[string]int{}}
				m[name] = st
			}
			st.Count += u.N
			st.Sessions++
			st.ByAgent[agent] += u.N
			if t := time.UnixMilli(u.Last); t.After(st.Last) {
				st.Last = t
			}
		}
	}
	for _, f := range c.Files {
		add(out.Skills, f.Agent, f.Skills)
		add(out.MCP, f.Agent, f.MCP)
	}
	return out, nil
}

// scanSkillUses 从头读一份会话文件，记下每个技能、每个 MCP 服务的次数和最后一次的时间。
func scanSkillUses(agent, path string, fi os.FileInfo) skillCacheFile {
	f := skillCacheFile{Agent: agent, Size: fi.Size(), Mtime: fi.ModTime().UnixMilli()}
	u, err := NewUsageScanner(agent, path).Scan()
	if err != nil || len(u.Skills)+len(u.MCP) == 0 {
		return f
	}
	if len(u.Skills) > 0 {
		f.Skills = map[string]skillUses{}
		for _, it := range u.Skills {
			f.Skills[it.Name] = skillUses{N: it.Count, Last: f.Mtime}
		}
	}
	if len(u.MCP) > 0 {
		f.MCP = map[string]skillUses{}
		for _, it := range u.MCP {
			f.MCP[it.Server] = skillUses{N: it.Count, Last: f.Mtime}
		}
	}
	// 有时间的会话按最后一次调用的时间算；事件只留最近的一段，最后一次一定在里面
	for _, e := range u.Events {
		if e.At.IsZero() || e.Failed {
			continue
		}
		at := e.At.UnixMilli()
		switch {
		case e.Skill != "":
			if s, ok := f.Skills[e.Skill]; ok && (s.Last == f.Mtime || at > s.Last) {
				s.Last = at
				f.Skills[e.Skill] = s
			}
		case e.Server != "":
			if s, ok := f.MCP[e.Server]; ok && (s.Last == f.Mtime || at > s.Last) {
				s.Last = at
				f.MCP[e.Server] = s
			}
		}
	}
	return f
}
