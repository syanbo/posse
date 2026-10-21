// Package usage 记录各 agent 套餐的用量（5 小时、每周、计费周期这些窗口用了多少、什么时候重置）。
// 读数只在 agent 运行时顺带得到：Claude 的 statusLine 输入和它自己的缓存、Codex 会话文件、Grok 的日志；
// 只有 Cursor 本地没有，在它用过之后向 Cursor 查一次。
package usage

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Window 是一个限额窗口。
type Window struct {
	Percent  float64 `json:"percent"`            // 已用，0–100
	Minutes  int     `json:"minutes,omitempty"`  // 窗口长度，0 是不知道
	ResetsAt int64   `json:"resetsAt,omitempty"` // 重置时间，Unix 毫秒，0 是不知道
	// Name 是分池的名字：Claude 按模型计的周额度（Fable），Cursor 套餐里的 Auto 和 API。空的是总额度
	Name string `json:"name,omitempty"`
	// At 是分池自己的读数时间，和整家的不是同一份读数时才有（Claude 的 Fable 只在它自己的缓存里）
	At int64 `json:"at,omitempty"`
}

// Spend 是按金额计的额度，单位美分；Limit 为 0 是没有上限。
type Spend struct {
	Used  float64 `json:"used"`
	Limit float64 `json:"limit,omitempty"`
	At    int64   `json:"at,omitempty"`
}

// Share 是额度用在了哪里（Claude 本周用量里 Claude Code、网页对话各占多少）。
type Share struct {
	Name    string  `json:"name"`
	Percent float64 `json:"percent"`
}

type Shares struct {
	Items []Share `json:"items"`
	At    int64   `json:"at,omitempty"`
}

// Resets 是手里的重置卡（Codex 用一张立刻清空 5 小时和每周的用量）。
type Resets struct {
	Count     int   `json:"count"`
	ExpiresAt int64 `json:"expiresAt,omitempty"` // 最早一张的过期时间，Unix 毫秒
	At        int64 `json:"at,omitempty"`
}

// Usage 是一家 agent 最近一次的读数。总额度窗口、套餐和套餐内金额来自同一份读数；
// 分池窗口和下面几项各自带读数时间，来源不同、新旧不同。
type Usage struct {
	Agent     string   `json:"agent"`
	Plan      string   `json:"plan,omitempty"`
	Windows   []Window `json:"windows"`
	Spend     *Spend   `json:"spend,omitempty"`    // 套餐内的金额（Cursor）
	OnDemand  *Spend   `json:"onDemand,omitempty"` // 超出套餐后按量付费的部分
	Shares    *Shares  `json:"shares,omitempty"`
	Resets    *Resets  `json:"resets,omitempty"`
	UpdatedAt int64    `json:"updatedAt"` // agent 给出这份读数的时间，Unix 毫秒
}

// merge 把新读数并进旧的：总额度取较新的一份，分池和各项各取较新的。
func merge(old, in Usage) Usage {
	main, other := in, old
	if in.UpdatedAt < old.UpdatedAt {
		main, other = old, in
	}
	out := Usage{Agent: main.Agent, Plan: main.Plan, Spend: main.Spend, UpdatedAt: main.UpdatedAt}
	// 有的来源不带套餐名（Claude 的 statusLine），较新的读数没有时沿用旧的
	if out.Plan == "" {
		out.Plan = cmp.Or(in.Plan, old.Plan)
	}
	for _, w := range main.Windows {
		if w.Name == "" {
			out.Windows = append(out.Windows, w)
		}
	}
	// Claude 的读数有时不带 5 小时窗口（刚重置、这个窗口里还没用过），只看较新的一份，状态栏会在
	// 5 小时和每周之间来回跳。较旧的一份里有、这份没有的总额度窗口沿用，已过重置时间的算清零。
	for _, w := range other.Windows {
		if main.Agent != "claude" || w.Name != "" || slices.ContainsFunc(out.Windows, func(x Window) bool { return x.Name == "" && x.Minutes == w.Minutes }) {
			continue
		}
		if w.ResetsAt != 0 && w.ResetsAt <= main.UpdatedAt {
			w.Percent, w.ResetsAt = 0, 0
		}
		// 按窗口长短插回去，5 小时仍在每周前面
		i := slices.IndexFunc(out.Windows, func(x Window) bool { return x.Name == "" && x.Minutes > w.Minutes })
		if i < 0 {
			i = len(out.Windows)
		}
		out.Windows = slices.Insert(out.Windows, i, w)
	}
	at := func(t, base int64) int64 {
		if t == 0 {
			return base
		}
		return t
	}
	var names []string
	named := map[string]Window{}
	for _, u := range []Usage{old, in} {
		for _, w := range u.Windows {
			if w.Name == "" {
				continue
			}
			w.At = at(w.At, u.UpdatedAt)
			if cur, ok := named[w.Name]; !ok || w.At >= cur.At {
				if !ok {
					names = append(names, w.Name)
				}
				named[w.Name] = w
			}
		}
	}
	for _, n := range names {
		out.Windows = append(out.Windows, named[n])
	}
	if old.OnDemand != nil && (in.OnDemand == nil || at(old.OnDemand.At, old.UpdatedAt) > at(in.OnDemand.At, in.UpdatedAt)) {
		out.OnDemand = old.OnDemand
	} else {
		out.OnDemand = in.OnDemand
	}
	if old.Shares != nil && (in.Shares == nil || at(old.Shares.At, old.UpdatedAt) > at(in.Shares.At, in.UpdatedAt)) {
		out.Shares = old.Shares
	} else {
		out.Shares = in.Shares
	}
	if old.Resets != nil && (in.Resets == nil || at(old.Resets.At, old.UpdatedAt) > at(in.Resets.At, in.UpdatedAt)) {
		out.Resets = old.Resets
	} else {
		out.Resets = in.Resets
	}
	// 各项都记上自己的读数时间，以后再并时不会被当成整家最新的那份
	if out.OnDemand != nil {
		d := *out.OnDemand
		d.At = at(d.At, pick(old.OnDemand == out.OnDemand, old.UpdatedAt, in.UpdatedAt))
		out.OnDemand = &d
	}
	if out.Shares != nil {
		d := *out.Shares
		d.At = at(d.At, pick(old.Shares == out.Shares, old.UpdatedAt, in.UpdatedAt))
		out.Shares = &d
	}
	if out.Resets != nil {
		d := *out.Resets
		d.At = at(d.At, pick(old.Resets == out.Resets, old.UpdatedAt, in.UpdatedAt))
		out.Resets = &d
	}
	return out
}

func pick(cond bool, a, b int64) int64 {
	if cond {
		return a
	}
	return b
}

// sameReading 比较除读数时间以外的内容。
func sameReading(a, b Usage) bool {
	a.UpdatedAt, b.UpdatedAt = 0, 0
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// order 是列出的顺序，和 agent.Known 一致。
var order = []string{"claude", "codex", "grok", "cursor"}

// file 是 $HIVE_HOME/usage.json 的内容。
type file struct {
	Usages    []Usage `json:"usages"`
	CursorOff bool    `json:"cursorOff,omitempty"` // 不向 Cursor 查询
	CodexOff  bool    `json:"codexOff,omitempty"`  // 不让 Codex 查重置卡
}

// Store 保存各家的最近读数，存在 usage.json 里，常驻进程重启后还在。
type Store struct {
	path string
	mu   sync.Mutex
	f    file
}

func Open(home string) *Store {
	s := &Store{path: filepath.Join(home, "usage.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		json.Unmarshal(b, &s.f)
	}
	return s
}

// confirmEvery：同样的读数再报上来时，隔这么久才把更新时间往后挪（并写盘）。
// statusLine 每条回复都报一次，不必每次都动。
const confirmEvery = time.Minute

// Update 记下一份读数，比已有的旧就丢掉。界面看得出的变化返回 true。
func (s *Store) Update(u Usage) bool {
	if u.Agent == "" || (len(u.Windows) == 0 && u.Spend == nil && u.Resets == nil) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.f.Usages, func(o Usage) bool { return o.Agent == u.Agent })
	if i >= 0 {
		old := s.f.Usages[i]
		m := merge(old, u)
		if sameReading(m, old) && time.Duration(m.UpdatedAt-old.UpdatedAt)*time.Millisecond < confirmEvery {
			return false
		}
		s.f.Usages[i] = m
	} else {
		s.f.Usages = append(s.f.Usages, merge(Usage{}, u))
		slices.SortFunc(s.f.Usages, func(a, b Usage) int { return slices.Index(order, a.Agent) - slices.Index(order, b.Agent) })
	}
	s.saveLocked()
	return true
}

// UpdatedAt 是这一家已有读数的时间，没有是 0。
func (s *Store) UpdatedAt(agent string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.f.Usages {
		if u.Agent == agent {
			return u.UpdatedAt
		}
	}
	return 0
}

// List 返回各家的读数。已经过了重置时间的窗口按用完归零：下一个窗口要等 agent 再用才知道从哪开始。
func (s *Store) List(now time.Time) []Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms := now.UnixMilli()
	out := make([]Usage, 0, len(s.f.Usages))
	for _, u := range s.f.Usages {
		u.Windows = slices.Clone(u.Windows)
		for i, w := range u.Windows {
			if w.ResetsAt > 0 && w.ResetsAt <= ms {
				u.Windows[i] = Window{Minutes: w.Minutes, Name: w.Name, At: w.At}
			}
		}
		out = append(out, u)
	}
	return out
}

func (s *Store) CursorOff() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.CursorOff
}

func (s *Store) SetCursorOff(off bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f.CursorOff != off {
		s.f.CursorOff = off
		s.saveLocked()
	}
}

func (s *Store) CodexOff() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.CodexOff
}

func (s *Store) SetCodexOff(off bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f.CodexOff != off {
		s.f.CodexOff = off
		s.saveLocked()
	}
}

// saveLocked 写临时文件再改名，写到一半退出不会留下坏文件。文件很小，在锁里写。
func (s *Store) saveLocked() {
	b, err := json.MarshalIndent(s.f, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, s.path)
	}
}

// titleWord 把 agent 给的小写套餐类型（plus、pro、team）写成首字母大写，照它们网页上的叫法。
func titleWord(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
