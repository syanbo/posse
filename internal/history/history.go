// Package history 把 claude、codex、cursor、grok 的会话记录增量索引进 SQLite，提供全文搜索。
// 索引只读各 agent 的会话文件，不写回；索引库是可以随时删掉重建的派生数据。
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("session not found")

// Agents 是支持索引的 agent，顺序即界面里的筛选顺序。
var Agents = []string{"claude", "codex", "cursor", "grok"}

type Session struct {
	Agent     string    `json:"agent"`
	ID        string    `json:"id"` // agent 自己的会话 ID，恢复会话时用它
	Title     string    `json:"title"`
	Cwd       string    `json:"cwd,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Messages  int       `json:"messages"`
	Path      string    `json:"path"`            // 会话文件；分成几段的是最新的一段
	Model     string    `json:"model,omitempty"` // 最近一次回复用的模型
	// Parts 是分成几段的会话的全部文件，从早到晚；只有一段时为空。只有 Find 填它
	Parts []string `json:"parts,omitempty"`
}

type Message struct {
	Seq  int       `json:"seq"`
	Role string    `json:"role"` // user | assistant | tool
	Text string    `json:"text"`
	At   time.Time `json:"at,omitzero"`
}

// Hit 是一个命中的会话，带上最相关的一条消息的摘要。
type Hit struct {
	Session
	Matches int       `json:"matches"` // 会话里命中的消息数
	Role    string    `json:"role"`
	Seq     int       `json:"seq"`
	At      time.Time `json:"at,omitzero"`
	Snippet string    `json:"snippet"`
	Spans   []Span    `json:"spans,omitempty"`
}

type Index struct {
	db    *sql.DB
	codex codexTitles // 更新索引时读过的 codex 会话名
}

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	agent TEXT NOT NULL,
	session_id TEXT NOT NULL,
	path TEXT NOT NULL UNIQUE,
	title TEXT NOT NULL DEFAULT '',
	first_prompt TEXT NOT NULL DEFAULT '',
	cwd TEXT NOT NULL DEFAULT '',
	branch TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0,
	messages INTEGER NOT NULL DEFAULT 0,
	-- 增量读取的进度：offset 之前的内容已入库；tail 是 offset 前的最后一段字节，
	-- 对不上说明文件被改写，要整份重读；state 是解析器跨批次要记住的东西
	size INTEGER NOT NULL DEFAULT 0,
	mtime INTEGER NOT NULL DEFAULT 0,
	offset INTEGER NOT NULL DEFAULT 0,
	tail BLOB,
	state TEXT NOT NULL DEFAULT '',
	-- 元数据旁路文件（grok 的 summary.json）的 mtime
	meta_mtime INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS sessions_sid ON sessions(session_id);
CREATE INDEX IF NOT EXISTS sessions_updated ON sessions(updated_at);
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY,
	session INTEGER NOT NULL,
	seq INTEGER NOT NULL,
	role TEXT NOT NULL,
	at INTEGER NOT NULL DEFAULT 0,
	text TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_session ON messages(session, seq);
-- 不存原文（原文在 messages 里），索引的是 splitCJK 处理过的文本。
-- 每条消息只填自己角色那一列，列权重让用户说的话比工具输出更相关
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
	user, assistant, tool, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 2'
);
INSERT INTO messages_fts(messages_fts, rank) VALUES('rank', 'bm25(4.0, 2.0, 1.0)');
-- 会话标题（没有标题时是第一句话），rowid 是 sessions.id
CREATE VIRTUAL TABLE IF NOT EXISTS titles_fts USING fts5(
	title, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 2'
);
`

// schemaVersion 变化时清空重建：索引是派生数据，重建比迁移简单可靠。解析规则变了也要改它。
const schemaVersion = "5"

// Open 打开索引供更新用，版本不对时清空重建。
func Open(path string) (*Index, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=cache_size(-4000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	x := &Index{db: db}
	if err := x.migrate(false); err != nil {
		db.Close()
		return nil, fmt.Errorf("open history index: %w", err)
	}
	return x, nil
}

// ErrStale 表示索引是另一个版本的 posse 建的。常驻进程跨应用升级继续运行，
// 只读的一方不迁移，免得新旧两个版本轮流清空重建。
var ErrStale = errors.New("the history index was built by a different posse version; restart the daemon (posse daemon stop)")

// OpenReader 打开索引只做查询，不建表、不迁移；索引还不存在时查询返回空结果。
// SQLite 的分配器不把内存还给系统，页缓存给小些，查询速度不受影响（页面在系统文件缓存里）。
func OpenReader(path string) (*Index, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)&_pragma=cache_size(-500)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &Index{db: db}, nil
}

func (x *Index) Close() error { return x.db.Close() }

// ready 检查索引能否查询：还没建好返回 false，版本不对返回 ErrStale。
func (x *Index) ready(ctx context.Context) (bool, error) {
	var v string
	err := x.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema'`).Scan(&v)
	switch {
	case err != nil && (errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no such table")):
		return false, nil
	case err != nil:
		return false, err
	case v != schemaVersion:
		return false, ErrStale
	}
	return true, nil
}

// Reset 清空索引，下次更新整份重建。在原文件上重建表而不删文件，别的进程开着的连接也能看到新内容。
func (x *Index) Reset() error { return x.migrate(true) }

func (x *Index) migrate(force bool) error {
	if !force {
		if ok, _ := x.ready(context.Background()); ok {
			return nil
		}
	}
	// 常驻进程和索引子进程可能同时打开新库，用写事务串行化，进来后再确认一次版本
	conn, err := x.db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	commit := false
	defer func() {
		if !commit {
			conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	var v string
	if !force && conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema'`).Scan(&v) == nil && v == schemaVersion {
		return nil
	}
	for _, t := range []string{"titles_fts", "messages_fts", "messages", "sessions", "meta"} {
		if _, err := conn.ExecContext(ctx, "DROP TABLE IF EXISTS "+t); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('schema',?)`, schemaVersion); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	commit = true
	return nil
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// sessionCols 是 scanSession 读的列，prefix 是表的别名（如 "s."）。
// 模型只记在增量读取的状态里，不单独成列。
func sessionCols(prefix string) string {
	var b strings.Builder
	for _, c := range []string{"agent", "session_id", "title", "first_prompt", "cwd", "branch", "created_at", "updated_at", "messages", "path"} {
		b.WriteString(prefix + c + ", ")
	}
	b.WriteString("COALESCE(json_extract(NULLIF(" + prefix + "state, ''), '$.model'), '')")
	return b.String()
}

func scanSession(sc interface{ Scan(...any) error }, extra ...any) (Session, error) {
	var s Session
	var first string
	var created, updated int64
	dest := append([]any{&s.Agent, &s.ID, &s.Title, &first, &s.Cwd, &s.Branch, &created, &updated, &s.Messages, &s.Path, &s.Model}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return s, err
	}
	if s.Title == "" {
		s.Title = first
	}
	s.CreatedAt, s.UpdatedAt = fromMS(created), fromMS(updated)
	return s, nil
}

type ListOptions struct {
	Agent string
	Cwd   string // 只列这个目录（含子目录）里的会话
	Limit int
}

func (o ListOptions) where(alias string) (string, []any) {
	var where []string
	var args []any
	// 没有任何消息的会话（只开了没说话）不列出来
	where = append(where, alias+"messages > 0")
	if o.Agent != "" {
		where = append(where, alias+"agent = ?")
		args = append(args, o.Agent)
	}
	if o.Cwd != "" {
		cwd := strings.TrimRight(o.Cwd, "/")
		where = append(where, "("+alias+"cwd = ? OR "+alias+"cwd LIKE ? ESCAPE '\\')")
		args = append(args, cwd, likeEscape(cwd)+"/%")
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// groupKeyOf 是 groupKey 带上表别名，第二列叫 gkey。
func groupKeyOf(alias string) string {
	return alias + `agent AS agent, CASE ` + alias + `agent WHEN 'codex' THEN ` + alias + `session_id ELSE ` + alias + `path END AS gkey`
}

// Codex 新格式（history_mode=paginated）的会话会分成几个文件：后一段的 session_meta 带 history_base，
// 接着前一段往下记，会话 ID 相同。索引里每个文件一行，查询时按 groupKey 合成一个会话：信息取最新的一段，
// 消息数相加，创建时间取最早的一段。别家同一个 ID 的不同文件是不同的会话（Cursor 在两个项目里
// 出现过同一个 ID、内容毫不相干的对话），按文件分开。
const groupKey = `agent, CASE agent WHEN 'codex' THEN session_id ELSE path END`

// merged 是合并后的会话表，供 FROM 使用：part 为 1 的行代表一个会话，parts 是它有几段。
// where 筛的是每一段（ListOptions.where 用空别名生成）。
func merged(where string) string {
	return `(SELECT id, agent, session_id, title, cwd, branch, updated_at, path, state,
		FIRST_VALUE(first_prompt) OVER (PARTITION BY ` + groupKey + ` ORDER BY created_at, id) AS first_prompt,
		MIN(created_at) OVER g AS created_at, SUM(messages) OVER g AS messages, COUNT(*) OVER g AS parts,
		ROW_NUMBER() OVER (PARTITION BY ` + groupKey + ` ORDER BY updated_at DESC, id DESC) AS part
		FROM sessions` + where + ` WINDOW g AS (PARTITION BY ` + groupKey + `))`
}

// part 是分成几段的会话里的一段，offset 是它的消息序号在整个会话里要加的数。
type part struct {
	id     int64
	path   string
	offset int
}

// parts 按从早到晚列出 s 的各段；只有一段时返回 nil。
func (x *Index) parts(ctx context.Context, s Session) ([]part, error) {
	if s.Agent != "codex" {
		return nil, nil
	}
	rows, err := x.db.QueryContext(ctx, `SELECT s.id, s.path, COALESCE((SELECT MAX(seq) + 1 FROM messages WHERE session = s.id), 0)
		FROM sessions s WHERE agent = ? AND session_id = ? ORDER BY created_at, id`, s.Agent, s.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []part
	offset := 0
	for rows.Next() {
		var p part
		var n int
		if err := rows.Scan(&p.id, &p.path, &n); err != nil {
			return nil, err
		}
		p.offset = offset
		offset += n
		out = append(out, p)
	}
	if len(out) < 2 {
		return nil, rows.Err()
	}
	return out, rows.Err()
}

// Sessions 按最近更新排列会话。
func (x *Index) Sessions(ctx context.Context, o ListOptions) ([]Session, error) {
	if ok, err := x.ready(ctx); !ok {
		return nil, err
	}
	where, args := o.where("")
	q := `SELECT ` + sessionCols("") + ` FROM ` + merged(where) + ` WHERE part = 1 ORDER BY updated_at DESC`
	if o.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", o.Limit)
	}
	rows, err := x.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Find 按会话 ID 或其前缀查找；前缀对应多个会话时报错列出候选。
func (x *Index) Find(ctx context.Context, id string) (Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Session{}, ErrNotFound
	}
	if ok, err := x.ready(ctx); !ok {
		if err == nil {
			err = ErrNotFound
		}
		return Session{}, err
	}
	rows, err := x.db.QueryContext(ctx, `SELECT `+sessionCols("")+` FROM `+merged("")+` WHERE part = 1 AND (session_id = ? OR session_id LIKE ? ESCAPE '\') ORDER BY session_id = ? DESC, updated_at DESC LIMIT 6`,
		id, likeEscape(id)+"%", id)
	if err != nil {
		return Session{}, err
	}
	var found []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return Session{}, err
		}
		found = append(found, s)
	}
	// 索引只有一个连接，补查 Parts 之前先关掉结果集
	rows.Close()
	if err := rows.Err(); err != nil {
		return Session{}, err
	}
	switch {
	case len(found) == 0:
		return Session{}, ErrNotFound
	case found[0].ID == id || len(found) == 1:
		return x.withParts(ctx, found[0])
	}
	var ids []string
	for _, s := range found {
		ids = append(ids, s.Agent+" "+s.ID)
	}
	return Session{}, fmt.Errorf("session %q is ambiguous: %s", id, strings.Join(ids, ", "))
}

// FindPath 返回包含这个会话文件的会话：同一个 ID 有几个不同会话时（见 groupKey）用它定位。
func (x *Index) FindPath(ctx context.Context, path string) (Session, error) {
	if ok, err := x.ready(ctx); !ok {
		if err == nil {
			err = ErrNotFound
		}
		return Session{}, err
	}
	row := x.db.QueryRowContext(ctx, `SELECT `+sessionCols("m.")+` FROM `+merged("")+` m JOIN sessions s
		ON s.path = ? AND m.agent = s.agent AND (m.path = s.path OR (m.agent = 'codex' AND m.session_id = s.session_id))
		WHERE m.part = 1 AND m.messages > 0`, path)
	s, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	return x.withParts(ctx, s)
}

// withParts 给分成几段的会话填上 Parts。
func (x *Index) withParts(ctx context.Context, s Session) (Session, error) {
	ps, err := x.parts(ctx, s)
	for _, p := range ps {
		s.Parts = append(s.Parts, p.path)
	}
	return s, err
}

// TranscriptOptions 筛选 Transcript 返回的消息，零值是全部。
type TranscriptOptions struct {
	Tail    int  // 只要最后几条
	NoTools bool // 不要工具调用和输出
}

// Transcript 按顺序返回会话的已索引消息。
func (x *Index) Transcript(ctx context.Context, s Session, o TranscriptOptions) ([]Message, error) {
	where := ""
	if o.NoTools {
		where = " AND m.role <> 'tool'"
	}
	limit := -1
	if o.Tail > 0 {
		limit = o.Tail
	}
	ps, err := x.parts(ctx, s)
	if err != nil {
		return nil, err
	}
	if ps != nil {
		// 分成几段的会话按段接起来，序号顺延
		var out []Message
		for _, p := range ps {
			rows, err := x.db.QueryContext(ctx, `SELECT m.seq, m.role, m.at, m.text FROM messages m WHERE m.session = ?`+where+` ORDER BY m.seq`, p.id)
			if err != nil {
				return nil, err
			}
			msgs, err := scanMessages(rows)
			if err != nil {
				return nil, err
			}
			for i := range msgs {
				msgs[i].Seq += p.offset
			}
			out = append(out, msgs...)
		}
		if limit >= 0 && len(out) > limit {
			out = out[len(out)-limit:]
		}
		return out, nil
	}
	rows, err := x.db.QueryContext(ctx, `SELECT seq, role, at, text FROM (
		SELECT m.seq, m.role, m.at, m.text FROM messages m JOIN sessions s ON s.id = m.session
		WHERE s.path = ?`+where+` ORDER BY m.seq DESC LIMIT ?
	) ORDER BY seq`, s.Path, limit)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var at int64
		if err := rows.Scan(&m.Seq, &m.Role, &at, &m.Text); err != nil {
			return nil, err
		}
		m.At = fromMS(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

type SearchOptions struct {
	ListOptions
	Role string // 只搜某一类消息：user | assistant | tool
}

// Search 全文搜索，每个会话一条结果。标题命中的排在前面，其余按最相关的一条消息排，
// 同一会话里命中的消息越多越靠前。没有消息同时含所有词时，退而返回含任一词的结果，any 为真。
func (x *Index) Search(ctx context.Context, query string, o SearchOptions) (hits []Hit, any bool, err error) {
	terms := parseTerms(query)
	if len(terms) == 0 {
		return nil, false, nil
	}
	if ok, err := x.ready(ctx); !ok {
		return nil, false, err
	}
	hits, err = x.search(ctx, terms, matchExpr(terms), o)
	if err != nil || len(hits) > 0 || len(terms) < 2 {
		return hits, false, err
	}
	hits, err = x.search(ctx, terms, anyExpr(terms), o)
	return hits, len(hits) > 0, err
}

func (x *Index) search(ctx context.Context, terms []term, match string, o SearchOptions) ([]Hit, error) {
	limit := o.Limit
	if limit <= 0 {
		limit = 50
	}
	where, args := o.where("s.")
	msgWhere, msgArgs := where, append([]any{}, args...)
	if o.Role != "" {
		msgWhere += " AND m.role = ?"
		msgArgs = append(msgArgs, o.Role)
	}
	// 指定了消息角色时标题不参与匹配
	// 结果按合并后的会话（groupKey）算：分成几段的会话只出一条
	titled := `SELECT DISTINCT ` + groupKeyOf("s.") + ` FROM titles_fts JOIN sessions s ON s.id = titles_fts.rowid WHERE titles_fts MATCH ?`
	if o.Role != "" {
		titled = `SELECT '' AS agent, '' AS gkey WHERE ? IS NULL`
	}
	listWhere, listArgs := o.where("")
	q := `WITH hits AS (
		SELECT m.id, ` + groupKeyOf("s.") + `, m.seq, m.role, m.at, f.rank AS rank, s.path
		FROM messages_fts f JOIN messages m ON m.id = f.rowid JOIN sessions s ON s.id = m.session
		` + msgWhere + ` AND messages_fts MATCH ?
	), best AS (
		-- 会话按最相关的一条排序；摘要优先取对话里的那条，没有才用工具输出
		SELECT agent, gkey, id, seq, role, at, best_rank AS rank, n, path FROM (
			SELECT *, MIN(rank) OVER w AS best_rank, COUNT(*) OVER w AS n,
				ROW_NUMBER() OVER (PARTITION BY agent, gkey ORDER BY role = 'tool', rank) AS pick
			FROM hits WINDOW w AS (PARTITION BY agent, gkey)
		) WHERE pick = 1
	), titled AS (` + titled + `
	), cand AS (
		SELECT agent, gkey FROM best UNION SELECT agent, gkey FROM titled
	), sess AS (
		SELECT *, CASE agent WHEN 'codex' THEN session_id ELSE path END AS gkey FROM ` + merged(listWhere) + ` WHERE part = 1
	)
	SELECT ` + sessionCols("s.") + `, COALESCE(b.n, 0), COALESCE(b.role, ''), COALESCE(b.seq, 0), COALESCE(b.at, 0),
		COALESCE(m.text, ''), t.agent IS NOT NULL AS title_hit, COALESCE(b.path, ''), s.parts
	FROM cand c JOIN sess s ON s.agent = c.agent AND s.gkey = c.gkey
	LEFT JOIN best b ON b.agent = c.agent AND b.gkey = c.gkey
	LEFT JOIN messages m ON m.id = b.id
	LEFT JOIN titled t ON t.agent = c.agent AND t.gkey = c.gkey
	ORDER BY title_hit DESC, COALESCE(b.rank - 0.5 * ln(b.n), 0) ASC, s.updated_at DESC LIMIT ?`
	all := append(msgArgs, match, match)
	all = append(all, listArgs...)
	all = append(all, limit)
	rows, err := x.db.QueryContext(ctx, q, all...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ns := needles(terms)
	var out []Hit
	type partHit struct {
		i    int
		path string
	}
	var partHits []partHit
	for rows.Next() {
		var h Hit
		var at int64
		var text string
		var titleHit bool
		var hitPath string
		var nparts int
		s, err := scanSession(rows, &h.Matches, &h.Role, &h.Seq, &at, &text, &titleHit, &hitPath, &nparts)
		if err != nil {
			return nil, err
		}
		if nparts > 1 && hitPath != "" {
			partHits = append(partHits, partHit{len(out), hitPath})
		}
		h.Session, h.At = s, fromMS(at)
		if text == "" {
			// 只有标题命中
			h.Role, text = "title", s.Title
		}
		h.Snippet, h.Spans = snippet(text, ns, 120)
		out = append(out, h)
	}
	// 索引只有一个连接，换算序号之前先关掉结果集
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, ph := range partHits {
		// 命中的序号是那一段里的，换成整个会话里的
		ps, err := x.parts(ctx, out[ph.i].Session)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if p.path == ph.path {
				out[ph.i].Seq += p.offset
			}
		}
	}
	return out, nil
}

// Stats 是索引规模，给状态栏和 CLI 显示。
type Stats struct {
	Sessions  int            `json:"sessions"`
	Messages  int            `json:"messages"`
	ByAgent   map[string]int `json:"byAgent"`
	IndexedAt time.Time      `json:"indexedAt,omitzero"`
}

func (x *Index) Stats(ctx context.Context) (Stats, error) {
	st := Stats{ByAgent: map[string]int{}}
	if ok, err := x.ready(ctx); !ok {
		return st, err
	}
	rows, err := x.db.QueryContext(ctx, `SELECT agent, COUNT(DISTINCT CASE agent WHEN 'codex' THEN session_id ELSE path END), SUM(messages) FROM sessions WHERE messages > 0 GROUP BY agent`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var agent string
		var n, m int
		if err := rows.Scan(&agent, &n, &m); err != nil {
			return st, err
		}
		st.ByAgent[agent] = n
		st.Sessions += n
		st.Messages += m
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	var at string
	if x.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='indexed_at'`).Scan(&at) == nil {
		st.IndexedAt, _ = time.Parse(time.RFC3339Nano, at)
	}
	return st, nil
}
