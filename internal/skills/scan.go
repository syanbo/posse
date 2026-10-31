// Package skills 扫描四家 agent（Claude、Codex、Grok、Cursor）能读到的技能，按各家的读取规则和开关算出谁用得上，
// 找出断链、同名冲突、多余的链接这类问题。只读；改各家配置的在 config.go。规则的出处见 docs/plans/skills.md。
package skills

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Agent 是会读技能的一家 agent。
type Agent int

const (
	Claude Agent = iota
	Codex
	Grok
	Cursor
	NumAgents
)

// Agents 按界面上的顺序列出各家。
var Agents = [NumAgents]Agent{Claude, Codex, Grok, Cursor}

func (a Agent) String() string {
	return [...]string{"claude", "codex", "grok", "cursor"}[a]
}

// Scope 是技能从哪一层来。
type Scope string

const (
	ScopeUser    Scope = "user"    // 用户目录（~/.agents/skills、~/.claude/skills 等）
	ScopeProject Scope = "project" // 工作区和它往上到仓库根的各级目录
	ScopePlugin  Scope = "plugin"  // 插件带的
	ScopeSynced  Scope = "synced"  // Claude 从账号同步下来的（~/.claude/skills/synced）
	ScopeSystem  Scope = "system"  // agent 自带的
)

// State 是某一家对一个技能的状态。
type State int

const (
	Unseen   State = iota // 这家不读它所在的目录，或配置里藏起来了
	On                    // 模型和斜杠命令都能用
	Manual                // 只能用户手动调用（模型看不到）
	Off                   // 配置里关了
	Shadowed              // 读得到，但被同名的另一份盖住
)

// Status 是一家对一个技能的状态和原因。
type Status struct {
	State State
	// Why 写原因（哪个配置、哪一项），给悬停提示用；On 时可以为空。
	Why string
	// Inferred 表示这家的读取规则是推断，没有核实过（Cursor）。
	Inferred bool
	// Ignored 表示是被 Grok 的 [skills] ignore 藏掉的（算作关了，打开时从 ignore 里去掉）。
	Ignored bool
	// Via 是这家从哪个路径读到它的（链接或目录本身）。
	Via string
}

// Skill 是一个技能。链接到同一个目录的算同一个。
type Skill struct {
	Name        string
	Description string
	// Dir 是解开链接后的真实目录；File 是其中的 SKILL.md。
	Dir, File string
	Scope     Scope
	// Source 是 npx skills 锁文件里记的来源（如 vercel-labs/skills），不是它装的为空。
	Source string
	// SourceURL 是来源的网页（GitHub 上技能所在的目录、well-known 的 SKILL.md），推不出来时为空。
	SourceURL string
	// LockHash 是锁文件里记的装上时的版本（GitHub 的目录树哈希或 well-known 的摘要），和 CheckUpdates 查到的比。
	LockHash string
	// Paths 是它出现在各个技能目录里的位置（链接或目录本身）。
	Paths  []string
	Meta   Meta
	Agents [NumAgents]Status
}

// IssueKind 是体检发现的一类问题。
type IssueKind string

const (
	BrokenLink    IssueKind = "broken"    // 链接指向的目录不在了
	Duplicate     IssueKind = "duplicate" // 同名的两份不同内容，同一家都读得到，只有一份生效
	RedundantLink IssueKind = "redundant" // 链接所在目录的各家本来就读得到它指向的那份
	LockMissing   IssueKind = "lock"      // npx skills 的锁文件里有，目录里没有
)

// Issue 是体检发现的一个问题。
type Issue struct {
	Kind IssueKind
	Name string
	// Path 是出问题的那个路径（断掉或多余的链接、没生效的那一份、锁文件）。
	Path string
	// Other 是相关的另一个路径（生效的那一份、链接指向的地方）。
	Other string
	// Agents 是受影响的几家（同名冲突时）。
	Agents []Agent
}

// Catalog 是一次扫描的结果。
type Catalog struct {
	Skills []Skill
	Issues []Issue
	// Codex 是 Codex 的状态从哪来：app-server 的 skills/list 为真，按文件推算为假。
	CodexLive bool
}

// Options 是扫描的参数。
type Options struct {
	Home string
	// Dir 是工作区目录；非空时也扫它往上到仓库根的项目级技能。
	Dir string
	// Codex 是 app-server 的 skills/list 结果（CodexList），没有时按文件推算。
	Codex []CodexSkill
}

// root 是一个技能目录：谁读它、按什么顺序。
type root struct {
	dir   string
	scope Scope
	// prio 是各家读它的优先级，越小越先，同名时先读到的生效；-1 是不读。
	prio [NumAgents]int
	// prefix 加在技能名前面（插件技能）。
	prefix string
	// walk 为真时往下各级找技能（Grok 的 [skills] paths：可以是目录，也可以是一个 SKILL.md）。
	walk bool
}

func prios(claude, codex, grok, cursor int) [NumAgents]int {
	return [NumAgents]int{claude, codex, grok, cursor}
}

// roots 列出各家会读的目录。优先级：项目的在用户的前面（Claude 相反：个人的盖过项目的）；
// Grok 文档里用户级几处同属最低一档，顺序按它的表。
func roots(o Options, cfg configs) []root {
	h := o.Home
	no := -1
	grokClaude, grokCursor := 30, 40
	if !cfg.grok.claudeSkills {
		grokClaude = no
	}
	if !cfg.grok.cursorSkills {
		grokCursor = no
	}
	rs := []root{
		{dir: filepath.Join(h, ".claude", "skills"), scope: ScopeUser, prio: prios(10, no, grokClaude, 30)},
		{dir: filepath.Join(h, ".agents", "skills"), scope: ScopeUser, prio: prios(no, 10, 20, 20)},
		{dir: filepath.Join(h, ".grok", "skills"), scope: ScopeUser, prio: prios(no, no, 10, no)},
		{dir: filepath.Join(h, ".cursor", "skills"), scope: ScopeUser, prio: prios(no, no, grokCursor, 10)},
		{dir: filepath.Join(h, ".codex", "skills"), scope: ScopeUser, prio: prios(no, no, no, 40)},
		{dir: filepath.Join(h, ".codex", "skills", ".system"), scope: ScopeSystem, prio: prios(no, 50, no, no)},
		{dir: filepath.Join(h, ".cursor", "skills-cursor"), scope: ScopeSystem, prio: prios(no, no, no, 50)},
	}
	// Claude 从账号同步下来的技能，一个桶一个目录
	if buckets, err := os.ReadDir(filepath.Join(h, ".claude", "skills", "synced")); err == nil {
		for _, b := range buckets {
			if b.IsDir() && !strings.HasPrefix(b.Name(), ".") {
				rs = append(rs, root{dir: filepath.Join(h, ".claude", "skills", "synced", b.Name()), scope: ScopeSynced, prio: prios(20, no, no, no)})
			}
		}
	}
	for i, p := range cfg.grok.paths {
		rs = append(rs, root{dir: p, scope: ScopeUser, prio: prios(no, no, 60+i, no), walk: true})
	}
	if o.Dir != "" {
		// 从工作区往上到仓库根，近的优先；Claude 只读工作区自己的 .claude/skills，
		// 同名时个人的盖过项目的（Claude Code 文档：enterprise > personal > project）
		for i, d := range upToRepo(o.Dir) {
			p := i // 越近越小
			claude := no
			cursorAt := no
			if i == 0 {
				claude, cursorAt = 15, 0
			}
			rs = append(rs,
				root{dir: filepath.Join(d, ".grok", "skills"), scope: ScopeProject, prio: prios(no, no, p, no)},
				root{dir: filepath.Join(d, ".agents", "skills"), scope: ScopeProject, prio: prios(no, p, p, cursorAt)},
				root{dir: filepath.Join(d, ".claude", "skills"), scope: ScopeProject, prio: prios(claude, no, p, cursorAt)},
				root{dir: filepath.Join(d, ".cursor", "skills"), scope: ScopeProject, prio: prios(no, no, p, cursorAt)},
			)
		}
	}
	return rs
}

// upToRepo 是 dir 和它往上直到仓库根（有 .git 的目录，worktree 里是文件）的各级目录；不在仓库里时只有 dir。
func upToRepo(dir string) []string {
	dir = filepath.Clean(dir)
	var out []string
	for d := dir; ; {
		out = append(out, d)
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return out
		}
		up := filepath.Dir(d)
		if up == d {
			return []string{dir}
		}
		d = up
	}
}

// found 是在某个目录里找到的一项。
type found struct {
	root  int
	path  string // 目录里的这一项（可能是链接）
	real  string // 解开链接后的目录
	name  string
	meta  Meta
	alias bool // path 是链接
}

// Scan 扫描各家的技能目录和配置。
func Scan(o Options) Catalog {
	cfg := readConfigs(o.Home, o.Dir)
	rs := roots(o, cfg)
	var cat Catalog
	var items []found
	seenRoot := map[string]bool{}
	for i, r := range rs {
		if seenRoot[r.dir] {
			continue
		}
		seenRoot[r.dir] = true
		for _, p := range candidates(r) {
			fi, err := os.Lstat(p)
			if err != nil {
				continue
			}
			alias := fi.Mode()&os.ModeSymlink != 0
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				if alias {
					cat.Issues = append(cat.Issues, Issue{Kind: BrokenLink, Name: filepath.Base(p), Path: p, Other: linkTarget(p)})
				}
				continue
			}
			src, err := os.ReadFile(filepath.Join(real, "SKILL.md"))
			if err != nil {
				continue
			}
			m := parseMeta(string(src))
			name := m.Name
			if name == "" {
				name = filepath.Base(p)
			}
			items = append(items, found{root: i, path: p, real: real, name: r.prefix + name, meta: m, alias: alias})
		}
	}
	cat.Skills = resolve(o, rs, cfg, items, &cat)
	if len(o.Codex) > 0 {
		cat.Skills = applyCodex(cat.Skills, o.Codex)
		cat.CodexLive = true
	}
	cat.Issues = append(cat.Issues, redundant(rs, items)...)
	cat.Issues = append(cat.Issues, lockMissing(o.Home, cfg.lock)...)
	return cat
}

// candidates 是 r 里可能是技能的各项：一般是下一层的各个目录（含链接）；walk 的往下各级找有 SKILL.md 的目录，
// 找到就不再往里找，r.dir 本身是 SKILL.md 时就是它所在的目录。
func candidates(r root) []string {
	if r.walk {
		if filepath.Base(r.dir) == "SKILL.md" {
			return []string{filepath.Dir(r.dir)}
		}
		var out []string
		var walk func(dir string, depth int)
		walk = func(dir string, depth int) {
			entries, err := os.ReadDir(dir)
			if err != nil || depth > 6 {
				return
			}
			for _, e := range entries {
				p := filepath.Join(dir, e.Name())
				if strings.HasPrefix(e.Name(), ".") {
					continue
				}
				if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err == nil {
					out = append(out, p)
					continue
				}
				if e.IsDir() {
					walk(p, depth+1)
				}
			}
		}
		if _, err := os.Stat(filepath.Join(r.dir, "SKILL.md")); err == nil {
			return []string{r.dir}
		}
		walk(r.dir, 0)
		return out
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			out = append(out, filepath.Join(r.dir, e.Name()))
		}
	}
	return out
}

func linkTarget(p string) string {
	t, err := os.Readlink(p)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(p), t)
	}
	return filepath.Clean(t)
}

// resolve 把找到的各项按真实目录合并成技能，再按各家的优先级算状态。
func resolve(o Options, rs []root, cfg configs, items []found, cat *Catalog) []Skill {
	byDir := map[string]int{}
	var out []Skill
	for _, it := range items {
		k, ok := byDir[it.real]
		if !ok {
			k = len(out)
			byDir[it.real] = k
			r := rs[it.root]
			s := Skill{Name: it.name, Description: it.meta.Description, Dir: it.real, File: filepath.Join(it.real, "SKILL.md"), Scope: r.scope, Meta: it.meta}
			// 链接的范围按它指向的地方算：项目里链到用户目录的仍是用户级
			if it.alias {
				if sc, ok := scopeOf(rs, it.real); ok {
					s.Scope = sc
				}
			}
			if e, ok := cfg.lock[it.name]; ok && filepath.Dir(it.real) == realDir(filepath.Join(o.Home, ".agents", "skills")) {
				s.Source, s.SourceURL, s.LockHash = e.Source, e.url(), e.hash()
			}
			out = append(out, s)
		}
		if !slices.Contains(out[k].Paths, it.path) {
			out[k].Paths = append(out[k].Paths, it.path)
		}
	}
	for _, a := range Agents {
		// 这家读得到的各项，按优先级排；同名的先到先得
		var seen []found
		for _, it := range items {
			if rs[it.root].prio[a] >= 0 && !cfg.hidden(a, it.path) {
				seen = append(seen, it)
			}
		}
		slices.SortStableFunc(seen, func(x, y found) int { return rs[x.root].prio[a] - rs[y.root].prio[a] })
		winner := map[string]found{}
		for _, it := range seen {
			s := &out[byDir[it.real]]
			if s.Agents[a].State != Unseen {
				continue // 同一个目录经另一条路径已经算过
			}
			st := Status{Via: it.path, Inferred: a == Cursor}
			if w, ok := winner[it.name]; ok && w.real != it.real {
				st.State, st.Why = Shadowed, "被同名的 "+w.path+" 盖住"
				addDuplicate(cat, it.name, it.path, w.path, a)
			} else {
				winner[it.name] = it
				st.State, st.Why = cfg.state(a, s)
			}
			s.Agents[a] = st
		}
		// 被 ignore 藏掉的（项目技能在 Posse 里关掉就是这样）算作关了，不算读不到：界面上还能再打开
		for _, it := range items {
			s := &out[byDir[it.real]]
			if rs[it.root].prio[a] >= 0 && cfg.hidden(a, it.path) && s.Agents[a].State == Unseen {
				s.Agents[a] = Status{State: Off, Why: "config.toml 的 [skills] ignore 里有它", Via: it.path, Ignored: true}
			}
		}
	}
	return out
}

// scopeOf 是 dir 所在技能目录的范围。
func scopeOf(rs []root, dir string) (Scope, bool) {
	for _, r := range rs {
		if filepath.Dir(dir) == realDir(r.dir) {
			return r.scope, true
		}
	}
	return "", false
}

func realDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func addDuplicate(cat *Catalog, name, path, winner string, a Agent) {
	for i := range cat.Issues {
		is := &cat.Issues[i]
		if is.Kind == Duplicate && is.Path == path && is.Other == winner {
			if !slices.Contains(is.Agents, a) {
				is.Agents = append(is.Agents, a)
			}
			return
		}
	}
	cat.Issues = append(cat.Issues, Issue{Kind: Duplicate, Name: name, Path: path, Other: winner, Agents: []Agent{a}})
}

// redundant 找多余的链接：链接所在目录的每一家，都已经从别的目录读到它指向的那一份。
// 比如 ~/.grok/skills/x → ~/.agents/skills/x，Grok 本来就读 ~/.agents/skills。
func redundant(rs []root, items []found) []Issue {
	var out []Issue
	for _, it := range items {
		if !it.alias {
			continue
		}
		r := rs[it.root]
		needed := false
		read := false
		for _, a := range Agents {
			if r.prio[a] < 0 {
				continue
			}
			read = true
			// 这家还有没有别的路径读到同一个目录
			other := false
			for _, o := range items {
				if o.path != it.path && o.real == it.real && rs[o.root].prio[a] >= 0 {
					other = true
					break
				}
			}
			if !other {
				needed = true
				break
			}
		}
		if read && !needed {
			out = append(out, Issue{Kind: RedundantLink, Name: it.name, Path: it.path, Other: it.real})
		}
	}
	return out
}

// lockMissing 找锁文件里有、~/.agents/skills 里没有的技能。
func lockMissing(home string, lock map[string]lockEntry) []Issue {
	dir := filepath.Join(home, ".agents", "skills")
	var out []Issue
	for name := range lock {
		if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
			out = append(out, Issue{Kind: LockMissing, Name: name, Path: filepath.Join(home, ".agents", ".skill-lock.json"), Other: filepath.Join(dir, name)})
		}
	}
	slices.SortFunc(out, func(x, y Issue) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// lockEntry 是 npx skills 锁文件里的一项。
type lockEntry struct {
	Source     string `json:"source"`
	SourceType string `json:"sourceType"`
	SourceURL  string `json:"sourceUrl"`
	SkillPath  string `json:"skillPath"`
	Ref        string `json:"ref"`

	SkillFolderHash string `json:"skillFolderHash"`
	SourceBaseURL   string `json:"sourceBaseUrl"`
	WellKnownDigest string `json:"wellKnownDigest"`
}

// hash 是装上时的版本：well-known 的看摘要，别的看目录树哈希。
func (e lockEntry) hash() string {
	if e.SourceType == "well-known" {
		return e.WellKnownDigest
	}
	return e.SkillFolderHash
}

// url 是来源的网页：GitHub 的指到技能所在目录（没记分支用 HEAD，GitHub 认），别的 http(s) 地址去掉 .git 原样用。
func (e lockEntry) url() string {
	if e.SourceType == "github" && strings.Count(e.Source, "/") == 1 {
		u := "https://github.com/" + e.Source
		if dir := path.Dir(e.SkillPath); e.SkillPath != "" && dir != "." {
			ref := e.Ref
			if ref == "" {
				ref = "HEAD"
			}
			u += "/tree/" + ref + "/" + dir
		}
		return u
	}
	if strings.HasPrefix(e.SourceURL, "https://") || strings.HasPrefix(e.SourceURL, "http://") {
		return strings.TrimSuffix(e.SourceURL, ".git")
	}
	return ""
}

// readLock 读 npx skills 的锁文件，返回 名字 → 记录。
func readLock(home string) map[string]lockEntry {
	b, err := os.ReadFile(filepath.Join(home, ".agents", ".skill-lock.json"))
	if err != nil {
		return nil
	}
	var l struct {
		Skills map[string]lockEntry `json:"skills"`
	}
	if json.Unmarshal(b, &l) != nil {
		return nil
	}
	return l.Skills
}
