package history

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// nestedTranscript 认出放在父会话目录里的子会话文件。
// Claude 是 projects/<项目>/<会话>/subagents/agent-<id>.jsonl，Cursor 是
// agent-transcripts/<会话>/subagents/<id>.jsonl。child 是子会话自己的 ID。
func nestedTranscript(path string) (parent, child string, claude bool) {
	dir := filepath.Dir(path)
	if filepath.Base(dir) != "subagents" {
		return "", "", false
	}
	sessionDir := filepath.Dir(dir)
	parent = filepath.Base(sessionDir)
	child = sessionIDFromPath(path)
	switch {
	case filepath.Base(filepath.Dir(filepath.Dir(sessionDir))) == "projects":
		return parent, strings.TrimPrefix(child, "agent-"), true
	case filepath.Base(filepath.Dir(sessionDir)) == "agent-transcripts":
		return parent, child, false
	default:
		return "", "", false
	}
}

// externalParent 是不读会话正文就能确定的父会话 ID。Codex 的父子关系写在正文里，返回 false。
func (x *Index) externalParent(src source, sessionID string) (string, bool) {
	if parent, _, _ := nestedTranscript(src.transcript); parent != "" {
		return cleanParent(parent, sessionID), true
	}
	if src.agent == "antigravity" {
		return cleanParent(src.parent, sessionID), true
	}
	if src.agent == "grok" {
		p := x.grokParents[sessionID]
		if p == "" {
			p = x.grokParents[filepath.Base(filepath.Dir(src.transcript))]
		}
		return cleanParent(p, sessionID), true
	}
	return "", false
}

func cleanParent(parent, sessionID string) string {
	if parent == "" || parent == sessionID {
		return ""
	}
	return parent
}

// sessionIDHint 用路径和已保存的状态估计会话 ID，供还没打开文件时对父子关系。
func sessionIDHint(src source, state string) string {
	var st fileState
	if state != "" {
		_ = json.Unmarshal([]byte(state), &st)
	}
	if st.SessionID == "" {
		st.SessionID = sessionIDFromPath(src.transcript)
		if src.agent == "grok" && (st.SessionID == "updates" || st.SessionID == "chat_history") {
			st.SessionID = filepath.Base(filepath.Dir(src.transcript))
		}
	}
	if _, child, _ := nestedTranscript(src.transcript); child != "" {
		st.SessionID = child
	}
	return st.SessionID
}

// applyParent 把路径、发现阶段和 Grok 登记表上的父会话写进状态。正文里已经写过的不覆盖。
func (x *Index) applyParent(src source, st *fileState) {
	if parent, child, _ := nestedTranscript(src.transcript); parent != "" {
		if child != "" {
			st.SessionID = child
		}
		if st.Parent == "" {
			st.Parent = parent
		}
	}
	if st.Parent == "" && src.parent != "" {
		st.Parent = src.parent
	}
	if st.Parent == "" && src.agent == "grok" {
		st.Parent = x.grokParents[st.SessionID]
		if st.Parent == "" {
			st.Parent = x.grokParents[filepath.Base(filepath.Dir(src.transcript))]
		}
	}
	st.Parent = cleanParent(st.Parent, st.SessionID)
}

// grokSubagentParents 读每个主会话 subagents/*/meta.json。child id → 父会话 id。
func grokSubagentParents(root string) map[string]string {
	out := map[string]string{}
	for _, meta := range globFiles(filepath.Join(root, "*", "*", "subagents", "*", "meta.json")) {
		b, err := os.ReadFile(meta)
		if err != nil {
			continue
		}
		var v struct {
			Parent string `json:"parent_session_id"`
			Child  string `json:"child_session_id"`
			Sub    string `json:"subagent_id"`
		}
		if json.Unmarshal(b, &v) != nil {
			continue
		}
		if v.Parent == "" {
			v.Parent = filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(meta))))
		}
		for _, id := range []string{v.Child, v.Sub} {
			if id != "" && id != v.Parent {
				out[id] = v.Parent
			}
		}
	}
	return out
}

// antigravityParents 从摘要库读派生会话的父会话。查询失败时返回 nil，调用方改靠元数据缓存。
func antigravityParents(base string) map[string]string {
	db := openAntigravitySummaries(base)
	if db == nil {
		return nil
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT conversation_id, parent_conversation_id FROM conversation_summaries WHERE parent_conversation_id != ''
		UNION SELECT conversation_id, parent_id FROM conversation_parents WHERE parent_id != ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, parent string
		if rows.Scan(&id, &parent) != nil {
			return nil
		}
		if id != "" && parent != "" && id != parent {
			out[id] = parent
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

func cursorComposerParent(data cursorComposer) string {
	if !cursorSubagent(data.Subagent) {
		return ""
	}
	var info struct {
		Parent string `json:"parentComposerId"`
	}
	if json.Unmarshal(data.Subagent, &info) != nil {
		return ""
	}
	return strings.TrimSpace(info.Parent)
}

func spawnTitle(path, nick string) string {
	task := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		task = path[i+1:]
	}
	if task == "" {
		return nick
	}
	return task
}

// nestedSessionDir 是嵌套转录的父会话目录（subagents 的上一层）；不是嵌套转录时返回空。
func nestedSessionDir(path string) string {
	if parent, _, _ := nestedTranscript(path); parent == "" {
		return ""
	}
	return filepath.Dir(filepath.Dir(path))
}

// mainTranscriptOf 报告 path 是不是会话目录 dir 的主转录：Claude 是 <dir>.jsonl，Cursor 是 <dir>/<会话>.jsonl。
func mainTranscriptOf(path, dir string) bool {
	return path == dir+".jsonl" || filepath.Dir(path) == dir
}

// parentIndexes 给出每个会话的父会话在 all 里的下标，-1 是顶层。
// all 按更新时间从新到旧，同 ID 的父会话有几份时取最新的那份；
// 嵌套转录只认同一会话目录里的那份（Cursor 同一个 ID 会出现在不同项目），
// 那里没有时才认同 ID 的 IDE 对话（转录目录里只有子会话、主对话仍在 IDE 库里）。
func parentIndexes(all []Session) []int {
	type key struct{ agent, id string }
	byID := map[key][]int{}
	for i, s := range all {
		k := key{s.Agent, s.ID}
		byID[k] = append(byID[k], i)
	}
	direct := make([]int, len(all))
	for i, s := range all {
		direct[i] = -1
		if s.Parent == "" {
			continue
		}
		dir := nestedSessionDir(s.Path)
		ide := -1
		for _, p := range byID[key{s.Agent, s.Parent}] {
			if p == i {
				continue
			}
			if dir == "" || mainTranscriptOf(all[p].Path, dir) {
				direct[i] = p
				break
			}
			if ide < 0 && IsCursorIDEChat(all[p].Path) {
				ide = p
			}
		}
		if direct[i] < 0 {
			direct[i] = ide
		}
	}
	out := make([]int, len(all))
	for i := range all {
		out[i] = direct[i]
		// 父子关系绕成圈时，圈上的都留在顶层
		seen := map[int]bool{}
		for n := direct[i]; n >= 0 && !seen[n]; n = direct[n] {
			if n == i {
				out[i] = -1
				break
			}
			seen[n] = true
		}
	}
	return out
}

// Latest 是这条会话和它所有子会话里最近的更新时间。列表按它排先后和分组，每行显示的仍是 UpdatedAt。
func (s Session) Latest() time.Time {
	t := s.UpdatedAt
	for _, c := range s.Children {
		if l := c.Latest(); l.After(t) {
			t = l
		}
	}
	return t
}

// SortByLatest 按整棵树最近的更新时间从新到旧排，时间相同的保持原来的先后。
func SortByLatest(ss []Session) {
	slices.SortStableFunc(ss, func(a, b Session) int { return b.Latest().Compare(a.Latest()) })
}

// nestSessions 把子会话挂到父会话的 Children 上，各层按整棵树最近的更新时间排。
// 父会话不在这批结果里时，子会话留在顶层。
func nestSessions(all []Session) []Session {
	kids := map[int][]int{}
	var roots []int
	for i, p := range parentIndexes(all) {
		if p < 0 {
			roots = append(roots, i)
		} else {
			kids[p] = append(kids[p], i)
		}
	}
	var walk func(int) Session
	walk = func(i int) Session {
		s := all[i]
		for _, c := range kids[i] {
			s.Children = append(s.Children, walk(c))
		}
		SortByLatest(s.Children)
		return s
	}
	out := make([]Session, 0, len(roots))
	for _, i := range roots {
		out = append(out, walk(i))
	}
	SortByLatest(out)
	return out
}

type childRef struct {
	id, path, spawn string
}

// childRefs 列出 parent 的子会话。Codex 子线程续写的那几段不带父线程，按会话 ID 一并取来，path 是最新的一段；
// 嵌套转录只认 parent 目录下的（见 parentIndexes）。
func (x *Index) childRefs(ctx context.Context, parent Session) ([]childRef, error) {
	if parent.Agent == "" || parent.ID == "" {
		return nil, nil
	}
	rows, err := x.db.QueryContext(ctx, `SELECT session_id, path, COALESCE(json_extract(NULLIF(state, ''), '$.spawn'), '')
		FROM sessions WHERE agent = ? AND messages > 0 AND (parent = ? OR (agent = 'codex' AND session_id IN (
			SELECT session_id FROM sessions WHERE agent = 'codex' AND parent = ?)))
		ORDER BY created_at, id`, parent.Agent, parent.ID, parent.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var order []string
	byID := map[string]childRef{}
	for rows.Next() {
		var c childRef
		if err := rows.Scan(&c.id, &c.path, &c.spawn); err != nil {
			return nil, err
		}
		if c.id == "" {
			continue
		}
		if dir := nestedSessionDir(c.path); dir != "" && !mainTranscriptOf(parent.Path, dir) && !IsCursorIDEChat(parent.Path) {
			continue
		}
		if old, ok := byID[c.id]; ok {
			if c.spawn != "" {
				old.spawn = c.spawn
			}
			old.path = c.path
			byID[c.id] = old
			continue
		}
		byID[c.id] = c
		order = append(order, c.id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]childRef, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

// linkChildMessages 给父会话里对得上的工具调用填上子会话。同一条子会话只挂一次。
func linkChildMessages(msgs []Message, children []childRef) {
	pending := append([]childRef(nil), children...)
	for i := range msgs {
		if msgs[i].Role != "tool" || len(pending) == 0 {
			continue
		}
		for j, c := range pending {
			key := c.spawn
			if key == "" && len(c.id) >= 8 {
				key = c.id
			}
			if key == "" || !strings.Contains(msgs[i].Text, key) {
				continue
			}
			msgs[i].Child = c.id
			msgs[i].ChildPath = c.path
			pending = append(pending[:j], pending[j+1:]...)
			break
		}
	}
}
