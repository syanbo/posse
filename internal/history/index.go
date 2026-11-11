package history

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"
)

// Roots 是各 agent 会话目录所在的用户主目录；测试时指向临时目录。
type Roots struct{ Home string }

// source 是一份要索引的会话文件。transcript 是按行追加的 JSONL；
// meta 是可选的元数据旁路文件，变化时整份重读。
type source struct {
	agent      string
	transcript string
	meta       string
	cwd        func() string // 记录里没有目录时的备选（cursor 从项目目录名还原）
	parser     parser
}

// fileState 是一份会话文件解析到一半时需要记住的东西，存进 sessions.state，
// 下次增量读取时接着用。
type fileState struct {
	SessionID   string `json:"sid,omitempty"`
	Title       string `json:"title,omitempty"`
	TitleManual bool   `json:"manual,omitempty"` // 用户起的标题，自动标题不再覆盖
	FirstPrompt string `json:"first,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Created     int64  `json:"created,omitempty"`
	Updated     int64  `json:"updated,omitempty"`
	Seq         int    `json:"seq,omitempty"`
	Mode        string `json:"mode,omitempty"` // codex 的记录格式
	// Skip 为真表示这份文件不是用户的会话（如 agent 内部的子任务），不入库
	Skip bool `json:"skip,omitempty"`
	// Model 是最近一次回复用的模型
	Model string `json:"model,omitempty"`
	// LastAt 是上一条有时间的消息的时间：记录里没有时间的消息沿用它
	LastAt int64 `json:"lastAt,omitempty"`
	// GrokUpdates 是 grok 的 updates.jsonl 里最后对上的回复分段的行尾位置，下次从这里接着建时间表
	GrokUpdates int64 `json:"grokUpdates,omitempty"`
	// grok 的消息时间表，读新行前从旁路文件现建，不存
	grok *grokTimes
}

// parser 解析一种 agent 的会话文件。
type parser interface {
	// line 解析一行记录，返回其中要索引的消息，并更新 st 里的元数据
	line(raw []byte, st *fileState) []Message
}

// preparer 是读新行之前还要准备旁路数据的 parser。
type preparer interface {
	prepare(transcript string, st *fileState)
}

// metaReader 是有元数据旁路文件的 parser。
type metaReader interface {
	meta(path string, st *fileState) error
}

// lineFilter 是能只看原始字节就排除无关行的 parser：wants 为假的行 line 一定什么都不做，
// 省掉完整解析 JSON。只能匹配字段值，不能连键带值匹配（冒号后可能有空格）。
type lineFilter interface {
	wants(raw []byte, st *fileState) bool
}

// Progress 是一次索引的进度，Done 计已检查的文件数。
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// maxLine 以上的单行记录直接跳过：几乎都是内嵌图片或超大工具输出，没有可搜的文字。
const maxLine = 32 << 20

// tailLen 是用来发现文件被改写的校验长度。
const tailLen = 64

// Update 增量更新索引：新文件整份读入，追加过的文件只读新增的行，消失的文件从索引删除。
func (x *Index) Update(ctx context.Context, roots Roots, progress func(Progress)) error {
	sources := discover(roots)
	known, err := x.knownFiles(ctx)
	if err != nil {
		return err
	}
	redo, err := x.staleParsers(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	// unsettled 是这次升级解析规则时有文件没重读成、也没能标记下次重读的 agent：不写新版本号，下次整家再重读
	unsettled := map[string]bool{}
	for i, src := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[src.transcript] = true
		k := known[src.transcript]
		if redo[src.agent] && k.id != 0 {
			// 进度作废，整份重读；会话 ID 不变
			k.size, k.offset = -1, math.MaxInt64
		}
		if err := x.updateFile(ctx, src, k, roots); err != nil {
			// 单个文件读不了（权限、半截写入）不影响其他文件，下次再试
			if errors.Is(err, context.Canceled) {
				return err
			}
			// 升级解析规则时这份没重读成：库里还是旧规则的结果和进度，文件没变的话下次会被当成读过了跳过。
			// 把进度作废，下次整份重读；作废不了就不写这一家的新版本号
			if redo[src.agent] && k.id != 0 {
				if _, err := x.db.ExecContext(ctx, `UPDATE sessions SET size=-1, offset=? WHERE id=?`, int64(math.MaxInt64), k.id); err != nil {
					unsettled[src.agent] = true
				}
			}
		}
		if progress != nil && (i%20 == 0 || i == len(sources)-1) {
			progress(Progress{Done: i + 1, Total: len(sources)})
		}
	}
	for path, k := range known {
		if !seen[path] {
			if err := x.deleteSession(ctx, k.id); err != nil {
				return err
			}
		}
	}
	if err := x.applyCodexTitles(ctx, roots); err != nil {
		return err
	}
	for agent := range redo {
		if unsettled[agent] {
			continue
		}
		if _, err := x.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			"parser:"+agent, parserVersions[agent]); err != nil {
			return err
		}
	}
	_, err = x.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('indexed_at',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type knownFile struct {
	id        int64
	title     string // 标题索引里的内容，见 indexedTitle
	size      int64
	mtime     int64
	offset    int64
	tail      []byte
	state     string
	metaMtime int64
}

// parserVersions 是各家解析规则的版本。只改了某一家的解析时改这里：只重读这一家的文件，表结构版本不变，
// 跨版本继续运行的旧常驻进程照常能查询。表结构或分词变了才改 schemaVersion（整份重建，旧常驻进程要重启）。
var parserVersions = map[string]string{
	"claude": "4", // 用户在 agent 工作时插进来的话
	"codex":  "2", // 记下模型
	"grok":   "4", // 增量读取时回复不再对上已用过的分段
	"cursor": "1", // 会话 ID 跨目录重复时各配各的 meta.json
}

// staleParsers 返回解析规则比索引里记录的新的 agent。
func (x *Index) staleParsers(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	for agent, v := range parserVersions {
		var have string
		err := x.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, "parser:"+agent).Scan(&have)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if have != v {
			out[agent] = true
		}
	}
	return out, nil
}

func (x *Index) knownFiles(ctx context.Context) (map[string]knownFile, error) {
	rows, err := x.db.QueryContext(ctx, `SELECT path, id, size, mtime, offset, tail, state, meta_mtime,
		CASE WHEN COALESCE(json_extract(NULLIF(state, ''), '$.skip'), 0) THEN '' ELSE COALESCE(NULLIF(title, ''), first_prompt) END
		FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]knownFile{}
	for rows.Next() {
		var path string
		var k knownFile
		if err := rows.Scan(&path, &k.id, &k.size, &k.mtime, &k.offset, &k.tail, &k.state, &k.metaMtime, &k.title); err != nil {
			return nil, err
		}
		out[path] = k
	}
	return out, rows.Err()
}

func mtimeOf(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano()
}

func (x *Index) updateFile(ctx context.Context, src source, k knownFile, roots Roots) error {
	// 先 stat：绝大多数文件没变，不用打开
	fi, err := os.Stat(src.transcript)
	if err != nil {
		return err
	}
	size, mtime := fi.Size(), fi.ModTime().UnixNano()
	metaMtime := int64(0)
	if src.meta != "" {
		metaMtime = mtimeOf(src.meta)
	}
	if k.id != 0 && size == k.size && mtime == k.mtime && metaMtime == k.metaMtime {
		return nil
	}
	f, err := os.Open(src.transcript)
	if err != nil {
		return err
	}
	defer f.Close()

	var st fileState
	offset := k.offset
	reset := k.id == 0 || size < k.offset || !tailMatches(f, k.offset, k.tail)
	if reset {
		offset = 0
	} else if k.state != "" {
		json.Unmarshal([]byte(k.state), &st)
	}
	var msgs []Message
	var tail []byte
	if pr, ok := src.parser.(preparer); ok && size > offset {
		pr.prepare(src.transcript, &st)
	}
	if size > offset {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		var n int64
		msgs, n, tail, err = readLines(f, src.parser, &st)
		if err != nil {
			return err
		}
		offset += n
		if n == 0 && !reset {
			// 只多了半行：没读进新的整行，留着上次的结尾，否则下次对不上、当成被改写整份重读
			tail = k.tail
		}
	} else {
		tail = k.tail
	}
	if mr, ok := src.parser.(metaReader); ok && src.meta != "" && (reset || metaMtime != k.metaMtime) {
		if mr.meta(src.meta, &st) != nil {
			// 旁路文件可能正写到一半，不记它的 mtime，下次再读
			metaMtime = 0
		}
	}
	if src.agent == "codex" {
		if t := x.loadCodexTitles(roots).titles[st.SessionID]; t != "" {
			st.Title = t
		}
	}
	if st.Cwd == "" && src.cwd != nil {
		st.Cwd = src.cwd()
	}
	if st.Updated == 0 {
		st.Updated = fi.ModTime().UnixMilli()
	}
	if st.Created == 0 {
		st.Created = st.Updated
	}
	if st.SessionID == "" {
		st.SessionID = sessionIDFromPath(src.transcript)
	}
	return x.save(ctx, src, k, reset, st, msgs, size, mtime, offset, tail, metaMtime)
}

// tailMatches 核对 offset 之前的字节是否和上次读到的一致。
func tailMatches(f *os.File, offset int64, tail []byte) bool {
	if offset == 0 {
		return true
	}
	if len(tail) == 0 || int64(len(tail)) > offset {
		return false
	}
	buf := make([]byte, len(tail))
	if _, err := f.ReadAt(buf, offset-int64(len(tail))); err != nil {
		return false
	}
	return bytes.Equal(buf, tail)
}

// readLines 读到最后一个完整行为止，返回解析出的消息、消费的字节数和最后 tailLen 字节。
func readLines(r io.Reader, p parser, st *fileState) ([]Message, int64, []byte, error) {
	filter, _ := p.(lineFilter)
	var msgs []Message
	n, tail, err := scanLines(r, func(line []byte, _ int64) {
		if st.Skip {
			return
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] != '{' || (filter != nil && !filter.wants(trimmed, st)) {
			return
		}
		for _, m := range p.line(trimmed, st) {
			m.Seq = st.Seq
			st.Seq++
			msgs = append(msgs, m)
		}
	})
	if err != nil {
		return nil, 0, nil, err
	}
	return msgs, n, tail, nil
}

// scanLines 逐个交出完整的行（含换行符），到最后一个完整行为止；超过 maxLine 的行跳过。
// fn 拿到的 line 只在调用期间有效，n 是到这一行结尾为止消费的字节数。
// 返回消费的字节数和最后 tailLen 字节。
func scanLines(r io.Reader, fn func(line []byte, n int64)) (int64, []byte, error) {
	br := bufio.NewReaderSize(r, 256<<10)
	var consumed int64
	var last []byte
	var long []byte   // 超过缓冲区的行在这里拼接
	var pending int64 // 当前行已读、还没遇到换行的字节数
	skipping := false
	for {
		chunk, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			pending += int64(len(chunk))
			if !skipping {
				long = append(long, chunk...)
				if len(long) > maxLine {
					long, skipping = nil, true
				}
			}
			continue
		}
		if err == io.EOF {
			// 没有换行结尾的最后一段可能还在写，留到下次从行首重读
			break
		}
		if err != nil {
			return consumed, last, err
		}
		consumed += pending + int64(len(chunk))
		pending = 0
		line := chunk
		if long != nil {
			long = append(long, chunk...)
			line = long
		}
		if !skipping {
			fn(line, consumed)
		}
		// 跳过的超长行只剩最后一段，也足够当校验：它就是 offset 前紧挨着的字节
		last = append(last[:0], line[max(0, len(line)-tailLen):]...)
		long, skipping = nil, false
	}
	return consumed, last, nil
}

func sessionIDFromPath(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(filepath.Ext(base))]
}

func (x *Index) save(ctx context.Context, src source, k knownFile, reset bool, st fileState, msgs []Message,
	size, mtime, offset int64, tail []byte, metaMtime int64) error {
	id := k.id
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if id != 0 && (reset || st.Skip) {
		if err := deleteMessages(ctx, tx, id); err != nil {
			return err
		}
	}
	stateJSON, _ := json.Marshal(st)
	count := 0
	if !st.Skip {
		count = st.Seq
	}
	if id == 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO sessions(agent, session_id, path, title, first_prompt, cwd, branch, created_at, updated_at,
			messages, size, mtime, offset, tail, state, meta_mtime) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			src.agent, st.SessionID, src.transcript, st.Title, st.FirstPrompt, st.Cwd, st.Branch, st.Created, st.Updated,
			count, size, mtime, offset, tail, string(stateJSON), metaMtime)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
	} else {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET session_id=?, title=?, first_prompt=?, cwd=?, branch=?, created_at=?, updated_at=?,
			messages=?, size=?, mtime=?, offset=?, tail=?, state=?, meta_mtime=? WHERE id=?`,
			st.SessionID, st.Title, st.FirstPrompt, st.Cwd, st.Branch, st.Created, st.Updated,
			count, size, mtime, offset, tail, string(stateJSON), metaMtime, id)
		if err != nil {
			return err
		}
	}
	if !st.Skip && len(msgs) > 0 {
		insMsg, err := tx.PrepareContext(ctx, `INSERT INTO messages(session, seq, role, at, text) VALUES(?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer insMsg.Close()
		insFTS, err := tx.PrepareContext(ctx, `INSERT INTO messages_fts(rowid, user, assistant, tool) VALUES(?,?,?,?)`)
		if err != nil {
			return err
		}
		defer insFTS.Close()
		for _, m := range msgs {
			res, err := insMsg.ExecContext(ctx, id, m.Seq, m.Role, ms(m.At), m.Text)
			if err != nil {
				return err
			}
			rowid, _ := res.LastInsertId()
			cols := [3]string{}
			switch m.Role {
			case "user":
				cols[0] = splitCJK(m.Text)
			case "assistant":
				cols[1] = splitCJK(m.Text)
			default:
				cols[2] = splitCJK(m.Text)
			}
			if _, err := insFTS.ExecContext(ctx, rowid, cols[0], cols[1], cols[2]); err != nil {
				return err
			}
		}
	}
	if k.id == 0 || k.title != indexedTitle(st.Title, st.FirstPrompt, st.Skip) {
		if err := setTitle(ctx, tx, id, st.Title, st.FirstPrompt, st.Skip); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// indexedTitle 是进标题索引的内容：标题，没有时是第一句话；跳过的会话不进。
func indexedTitle(title, first string, skip bool) string {
	if skip {
		return ""
	}
	if title == "" {
		return first
	}
	return title
}

// setTitle 更新标题索引。
func setTitle(ctx context.Context, tx *sql.Tx, id int64, title, first string, skip bool) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM titles_fts WHERE rowid=?`, id); err != nil {
		return err
	}
	title = indexedTitle(title, first, skip)
	if title == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO titles_fts(rowid, title) VALUES(?,?)`, id, splitCJK(title))
	return err
}

func deleteMessages(ctx context.Context, tx *sql.Tx, session int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages_fts WHERE rowid IN (SELECT id FROM messages WHERE session=?)`, session); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session=?`, session)
	return err
}

func (x *Index) deleteSession(ctx context.Context, id int64) error {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteMessages(ctx, tx, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM titles_fts WHERE rowid=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// walkFiles 列出 root 下满足 keep 的文件；目录不存在时返回空。
func walkFiles(root string, keep func(path string, d fs.DirEntry) bool) []string {
	var out []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && path != root {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() && keep(path, d) {
			out = append(out, path)
		}
		return nil
	})
	return out
}
