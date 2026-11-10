package store

// 讨论：用户和多个 agent 成员在一个讨论里交流，@ 一个成员就为它排一轮执行。
// 讨论消息是长期记录，任务只是执行载体：任务被清理、成员被移除后讨论历史照样可读。
// 队列、当前轮次、上下文快照、成员绑定、接收确认和变更序号全在这里，常驻进程内存里只负责唤醒。
// 轮次的条件校验和任务状态、任务消息、讨论回复在同一个事务里提交；事务里一律用 tx 查询：
// 连接只有一个，另走 s.db 会自己等自己。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const discussionSchema = `
CREATE TABLE IF NOT EXISTS disc_meta (k TEXT PRIMARY KEY, v INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS discussions (
	key TEXT PRIMARY KEY,
	host TEXT NOT NULL,
	local_id TEXT NOT NULL UNIQUE,
	title TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	project_id TEXT NOT NULL,
	project_name TEXT NOT NULL DEFAULT '',
	project_path TEXT NOT NULL DEFAULT '',
	workspace_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	rev INTEGER NOT NULL DEFAULT 0,
	msg_seq INTEGER NOT NULL DEFAULT 0,
	changed INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	archived_at INTEGER,
	mirror INTEGER NOT NULL DEFAULT 0,
	shared INTEGER NOT NULL DEFAULT 0,
	summary TEXT NOT NULL DEFAULT '',
	summary_seq INTEGER NOT NULL DEFAULT 0,
	summary_by TEXT NOT NULL DEFAULT '',
	summary_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS disc_members (
	id TEXT PRIMARY KEY,
	discussion TEXT NOT NULL REFERENCES discussions(key),
	name TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT '',
	agent TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	effort TEXT NOT NULL DEFAULT '',
	workspace_id TEXT NOT NULL,
	workspace_label TEXT NOT NULL DEFAULT '',
	config_version INTEGER NOT NULL DEFAULT 1,
	task_id TEXT NOT NULL DEFAULT '',
	worker TEXT NOT NULL DEFAULT '',
	worker_config INTEGER NOT NULL DEFAULT 0,
	paused TEXT NOT NULL DEFAULT '',
	recv_seq INTEGER NOT NULL DEFAULT 0,
	removed_at INTEGER,
	rev INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	generation INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS disc_members_discussion ON disc_members(discussion);
CREATE TABLE IF NOT EXISTS disc_messages (
	id TEXT PRIMARY KEY,
	discussion TEXT NOT NULL REFERENCES discussions(key),
	seq INTEGER NOT NULL,
	kind TEXT NOT NULL,
	author_member TEXT NOT NULL DEFAULT '',
	author_name TEXT NOT NULL DEFAULT '',
	author_agent TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL,
	quote TEXT NOT NULL DEFAULT '',
	target TEXT NOT NULL DEFAULT '',
	target_name TEXT NOT NULL DEFAULT '',
	execution TEXT NOT NULL DEFAULT '',
	question TEXT NOT NULL DEFAULT '',
	report TEXT NOT NULL DEFAULT '',
	outcome TEXT NOT NULL DEFAULT '',
	attachments TEXT NOT NULL DEFAULT '',
	client_id TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	key_at INTEGER,
	rev INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	author_id TEXT NOT NULL DEFAULT '',
	mention TEXT NOT NULL DEFAULT '',
	mention_name TEXT NOT NULL DEFAULT '',
	key_by TEXT NOT NULL DEFAULT '',
	choices TEXT NOT NULL DEFAULT '',
	UNIQUE(discussion, seq)
);
CREATE UNIQUE INDEX IF NOT EXISTS disc_messages_client ON disc_messages(discussion, client_id) WHERE client_id != '';
CREATE UNIQUE INDEX IF NOT EXISTS disc_messages_source ON disc_messages(source) WHERE source != '';
CREATE UNIQUE INDEX IF NOT EXISTS disc_messages_answer2 ON disc_messages(discussion, question) WHERE kind = 'answer';
CREATE INDEX IF NOT EXISTS disc_messages_rev ON disc_messages(discussion, rev);
CREATE TABLE IF NOT EXISTS disc_executions (
	id TEXT PRIMARY KEY,
	discussion TEXT NOT NULL REFERENCES discussions(key),
	member TEXT NOT NULL REFERENCES disc_members(id),
	message TEXT NOT NULL,
	retry_of TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	reason TEXT NOT NULL DEFAULT '',
	task_id TEXT NOT NULL DEFAULT '',
	worker TEXT NOT NULL DEFAULT '',
	agent TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	effort TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT '',
	workspace_id TEXT NOT NULL DEFAULT '',
	workspace_label TEXT NOT NULL DEFAULT '',
	config_version INTEGER NOT NULL DEFAULT 0,
	confirmed INTEGER NOT NULL DEFAULT 0,
	context TEXT NOT NULL DEFAULT '',
	context_index TEXT NOT NULL DEFAULT '',
	context_from INTEGER NOT NULL DEFAULT 0,
	context_upto INTEGER NOT NULL DEFAULT 0,
	acked INTEGER NOT NULL DEFAULT 0,
	reply TEXT NOT NULL DEFAULT '',
	rev INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	started_at INTEGER,
	ended_at INTEGER,
	owner TEXT NOT NULL DEFAULT '',
	generation INTEGER NOT NULL DEFAULT 0,
	root TEXT NOT NULL DEFAULT '',
	depth INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS disc_executions_request ON disc_executions(message) WHERE retry_of = '';
CREATE UNIQUE INDEX IF NOT EXISTS disc_executions_retry ON disc_executions(retry_of) WHERE retry_of != '';
CREATE INDEX IF NOT EXISTS disc_executions_member ON disc_executions(member, state);
CREATE INDEX IF NOT EXISTS disc_executions_task ON disc_executions(task_id);
CREATE INDEX IF NOT EXISTS disc_executions_rev ON disc_executions(discussion, rev);
`

// 讨论状态
const (
	DiscussionActive   = "active"
	DiscussionArchived = "archived"
)

// 讨论消息的种类
const (
	DiscUser     = "user"     // 用户发言（可以 @ 一个成员）
	DiscReply    = "reply"    // 成员这一轮的完整回复（task done 的正文）
	DiscQuestion = "question" // 成员在执行中提的问题，等用户回答
	DiscAnswer   = "answer"   // 用户对问题的回答
	// DiscDelegate 是成员在自己的一轮里 @ 另一个成员的请求（作者是成员，不是人），同样为对方排一轮执行
	DiscDelegate = "delegate"
)

// 成员互相 @ 的上限：从用户的一条请求算起，最多转 MaxChainDepth 层、一共 MaxChainRounds 轮（含用户那一轮）。
// 超了就拒绝，由成员向用户汇报；用户再 @ 是新的一条链。
const (
	MaxChainDepth  = 3
	MaxChainRounds = 8
)

// 执行状态。queued、held、blocked 是还没开始的；starting、running、waiting 是正在进行的轮次，一个成员同时只有一个。
const (
	ExecQueued      = "queued"      // 排队，等上一轮结束、worker 空闲
	ExecHeld        = "held"        // 派发前发现同一目录有别的讨论 worker 在执行，等用户确认
	ExecBlocked     = "blocked"     // 上一轮的 worker 已不在（终端关了、agent 退出），等用户「重新启动并继续」
	ExecStarting    = "starting"    // 已领取，正在拉起 worker；task ID 预先分配好
	ExecRunning     = "running"     // worker 在做这一轮
	ExecWaiting     = "waiting"     // worker 提了问题，等回答
	ExecDone        = "done"        // 成员汇报完成
	ExecFailed      = "failed"      // 成员汇报失败，或启动失败（reason 写原因）
	ExecInterrupted = "interrupted" // 这一轮进行中 worker 退出、常驻进程重启
	ExecStopped     = "stopped"     // 用户停止
	ExecCancelled   = "cancelled"   // 用户在开始前取消
	// ExecMerged：同一个人连着发给同一成员的请求，排在后面的并进前一条那一轮（reason 是那一轮的执行 ID），不单独执行
	ExecMerged = "merged"
	// 局域网协作（房主一侧）：成员电脑上的 Agent 的请求
	ExecClaimed = "claimed" // 拥有者的电脑已领取，在它本机的队列里，还没开始
	ExecRevoked = "revoked" // 拥有者的访问被撤销（移除、退出）：共享侧终态，不代表对方电脑上的进程已停止
)

// 讨论数据的错误：常驻进程按这些转成 RPC 错误码，英文原文交给界面经 errtext 翻译。
var (
	ErrArchived   = errors.New("discussion is archived")
	ErrConflict   = errors.New("conflict")
	ErrStaleRound = errors.New("stale execution")
	ErrNotAcked   = errors.New("context not fully read")
	// ErrAlreadyReported 是同一轮、同样内容的重复汇报（响应丢了重跑）：调用方当作成功
	ErrAlreadyReported = errors.New("already reported")
	ErrRetried         = errors.New("execution already retried")
	ErrMemberGone      = errors.New("member removed")
	ErrBusyMember      = errors.New("member has active or queued requests")
	ErrBusyDiscuss     = errors.New("discussion has active or queued requests")
	ErrNotOwner        = errors.New("not the owner")
	ErrRevoked         = errors.New("access revoked")
	// 加 Agent 的请求 ID 重发时对不上（AddMemberOnce）：那个 Agent 已经移除或属于上一代资格，或者这次的设置不同
	ErrAddGone    = errors.New("the agent this request added was removed since; add it again from a new form")
	ErrAddChanged = errors.New("this request already added an agent with other settings; edit that agent instead")
	// ErrChainLimit 是成员互相 @ 到了上限
	ErrChainLimit = errors.New("agent-to-agent request limit reached")
)

// pendingStates 和 activeStates 合起来就是挡住归档、移除成员的状态。
var pendingStates = []string{ExecQueued, ExecHeld, ExecBlocked, ExecClaimed}
var activeStates = []string{ExecStarting, ExecRunning, ExecWaiting}

// ExecActive 报告执行是不是正在进行的轮次。
func ExecActive(state string) bool {
	return state == ExecStarting || state == ExecRunning || state == ExecWaiting
}

// ExecPending 报告执行是不是还没开始。
func ExecPending(state string) bool {
	return state == ExecQueued || state == ExecHeld || state == ExecBlocked || state == ExecClaimed
}

func inList(states []string) string {
	return "('" + strings.Join(states, "','") + "')"
}

type Discussion struct {
	Key         string     `json:"key"` // roomKey：房主安装身份指纹/讨论 ID
	Host        string     `json:"host"`
	LocalID     string     `json:"localId"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	ProjectID   string     `json:"projectId"`
	ProjectName string     `json:"projectName"`
	ProjectPath string     `json:"projectPath"` // 快照：项目移除后还能看附件在哪
	WorkspaceID string     `json:"workspaceId,omitempty"`
	Status      string     `json:"status"`
	Rev         int64      `json:"rev"`
	MsgSeq      int64      `json:"msgSeq"`
	Changed     int64      `json:"changed"`
	CreatedAt   time.Time  `json:"createdAt"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
	// Mirror 是加入的别人的讨论：这里是房主权威记录的缓存，只有自己 Agent 的执行和私有配置在本机写
	Mirror bool `json:"mirror,omitempty"`
	Shared bool `json:"shared,omitempty"` // 房主开着局域网共享
	// Summary 是成员写的讨论摘要，覆盖到序号 SummarySeq 为止的消息：之后的轮次先读它，
	// 落后很多的成员不再逐条领取它之前的消息。SummaryBy 是写它的成员的名字。
	Summary    string     `json:"summary,omitempty"`
	SummarySeq int64      `json:"summarySeq,omitempty"`
	SummaryBy  string     `json:"summaryBy,omitempty"`
	SummaryAt  *time.Time `json:"summaryAt,omitempty"`
}

// MaxSummary 是讨论摘要的长度上限（UTF-8 字节）：每一轮的上下文都带着它。
const MaxSummary = 4000

// SummarySkip：成员没接收过、被摘要覆盖的消息多于这个数时，上下文里用摘要代替它们。
const SummarySkip = 20

type Member struct {
	ID             string     `json:"id"`
	Discussion     string     `json:"discussion"`
	Name           string     `json:"name"`
	Role           string     `json:"role,omitempty"`
	Agent          string     `json:"agent"`
	Model          string     `json:"model,omitempty"`
	Effort         string     `json:"effort,omitempty"`
	WorkspaceID    string     `json:"workspaceId"`
	WorkspaceLabel string     `json:"workspaceLabel,omitempty"`
	ConfigVersion  int64      `json:"configVersion"`
	TaskID         string     `json:"taskId,omitempty"` // 当前绑定的专用 worker 任务
	Worker         string     `json:"worker,omitempty"`
	WorkerConfig   int64      `json:"workerConfig,omitempty"` // 绑定的 worker 启动时的配置版本
	Paused         string     `json:"paused,omitempty"`       // 队列暂停的原因
	RecvSeq        int64      `json:"recvSeq"`
	RemovedAt      *time.Time `json:"removedAt,omitempty"`
	Rev            int64      `json:"rev"`
	CreatedAt      time.Time  `json:"createdAt"`
	// Owner 是 Agent 拥有者的安装身份指纹，空是讨论房主（第一阶段的成员）；只有拥有者能调用、配置它
	Owner      string `json:"owner,omitempty"`
	Generation int64  `json:"generation,omitempty"` // 拥有者加入讨论的代际（房主自己为 0）
	// Access 是除了拥有者还有谁能 @ 它、让它执行：空是只有拥有者，AccessAll 是讨论里所有人，
	// 否则是逗号分隔、排好序的指纹。只有拥有者能改；拥有者的电脑按本机确认过的这份核对，不信房主那份
	Access string `json:"access,omitempty"`
}

// AccessAll 是 Member.Access 的「讨论里所有人」。
const AccessAll = "all"

// Allows 报告 fp 能不能 @ 这个 Agent、让它执行。host 是讨论房主的指纹（房主自己的 Agent Owner 为空）。
func (m Member) Allows(fp, host string) bool {
	if fp == cmpOr(m.Owner, host) {
		return true
	}
	if m.Access == AccessAll {
		return fp != ""
	}
	return fp != "" && slices.Contains(strings.Split(m.Access, ","), fp)
}

// NormalizeAccess 把访问设置整理成存的样子：所有人、空（只有拥有者），或者去重、排好序的指纹（去掉拥有者自己）。
func NormalizeAccess(access, owner string) string {
	if access == AccessAll || access == "" {
		return access
	}
	var fps []string
	for _, fp := range strings.Split(access, ",") {
		if fp = strings.TrimSpace(fp); fp != "" && fp != owner && fp != AccessAll && !slices.Contains(fps, fp) {
			fps = append(fps, fp)
		}
	}
	slices.Sort(fps)
	return strings.Join(fps, ",")
}

// Attachment 是消息附件的快照：文件复制在项目的 .posse/discussions/<讨论>/attachments/ 下，Path 相对项目目录。
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	Type string `json:"type,omitempty"`
	SHA  string `json:"sha,omitempty"` // 内容的 SHA-256（跨机下载时校验）
}

type DiscMessage struct {
	ID           string       `json:"id"`
	Discussion   string       `json:"discussion"`
	Seq          int64        `json:"seq"`
	Kind         string       `json:"kind"`
	AuthorMember string       `json:"authorMember,omitempty"`
	AuthorName   string       `json:"authorName,omitempty"`
	AuthorAgent  string       `json:"authorAgent,omitempty"`
	Body         string       `json:"body"`
	Quote        string       `json:"quote,omitempty"`
	Target       string       `json:"target,omitempty"`
	TargetName   string       `json:"targetName,omitempty"`
	Execution    string       `json:"execution,omitempty"`
	Question     string       `json:"question,omitempty"` // 问题和回答对应的任务消息 ID
	Report       string       `json:"report,omitempty"`
	Outcome      string       `json:"outcome,omitempty"`
	Attachments  []Attachment `json:"attachments,omitempty"`
	ClientID     string       `json:"clientId,omitempty"`
	KeyAt        *time.Time   `json:"keyAt,omitempty"`
	Rev          int64        `json:"rev"`
	CreatedAt    time.Time    `json:"createdAt"`
	// AuthorID 是发言的人（安装身份指纹）；空是讨论房主本机的用户（第一阶段的消息）
	AuthorID    string `json:"authorId,omitempty"`
	Mention     string `json:"mention,omitempty"` // @ 的人（指纹）：只是提醒，不触发 Agent
	MentionName string `json:"mentionName,omitempty"`
	KeyBy       string `json:"keyBy,omitempty"` // 标关键的人
	// Choices 是问题附带的可选答案：界面画成按钮，点了就用这段文字回答；也可以不选、自己写
	Choices []string `json:"choices,omitempty"`
}

type Execution struct {
	ID             string     `json:"id"`
	Discussion     string     `json:"discussion"`
	Member         string     `json:"member"`
	Message        string     `json:"message"`
	RetryOf        string     `json:"retryOf,omitempty"`
	State          string     `json:"state"`
	Reason         string     `json:"reason,omitempty"`
	TaskID         string     `json:"taskId,omitempty"`
	Worker         string     `json:"worker,omitempty"`
	Agent          string     `json:"agent"`
	Model          string     `json:"model,omitempty"`
	Effort         string     `json:"effort,omitempty"`
	Role           string     `json:"role,omitempty"`
	WorkspaceID    string     `json:"workspaceId"`
	WorkspaceLabel string     `json:"workspaceLabel,omitempty"`
	ConfigVersion  int64      `json:"configVersion"`
	Confirmed      bool       `json:"confirmed,omitempty"`
	ContextLen     int        `json:"contextLen"`
	Acked          int        `json:"acked"`
	Reply          string     `json:"reply,omitempty"`
	Rev            int64      `json:"rev"`
	CreatedAt      time.Time  `json:"createdAt"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	EndedAt        *time.Time `json:"endedAt,omitempty"`
	Owner          string     `json:"owner,omitempty"` // 执行设备（拥有者的安装身份），空是房主
	Generation     int64      `json:"generation,omitempty"`
	// Root 是这条链最初那一轮用户请求的执行（成员互相 @ 出来的轮次才有），Depth 是转了几层（用户的请求是 0）
	Root  string `json:"root,omitempty"`
	Depth int    `json:"depth,omitempty"`
}

// --- 扫描

const discCols = `key,host,local_id,title,description,project_id,project_name,project_path,workspace_id,status,rev,msg_seq,changed,created_at,archived_at,mirror,shared,summary,summary_seq,summary_by,summary_at`

func scanDiscussion(row interface{ Scan(...any) error }) (Discussion, error) {
	var d Discussion
	var created, summaryAt int64
	var archived sql.NullInt64
	err := row.Scan(&d.Key, &d.Host, &d.LocalID, &d.Title, &d.Description, &d.ProjectID, &d.ProjectName, &d.ProjectPath, &d.WorkspaceID,
		&d.Status, &d.Rev, &d.MsgSeq, &d.Changed, &created, &archived, &d.Mirror, &d.Shared, &d.Summary, &d.SummarySeq, &d.SummaryBy, &summaryAt)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if summaryAt > 0 {
		at := fromMS(summaryAt)
		d.SummaryAt = &at
	}
	d.CreatedAt = fromMS(created)
	d.ArchivedAt = optTime(archived)
	return d, err
}

const memberCols = `id,discussion,name,role,agent,model,effort,workspace_id,workspace_label,config_version,task_id,worker,worker_config,paused,recv_seq,removed_at,rev,created_at,owner,generation,access`

func scanMember(row interface{ Scan(...any) error }) (Member, error) {
	var m Member
	var created int64
	var removed sql.NullInt64
	err := row.Scan(&m.ID, &m.Discussion, &m.Name, &m.Role, &m.Agent, &m.Model, &m.Effort, &m.WorkspaceID, &m.WorkspaceLabel, &m.ConfigVersion,
		&m.TaskID, &m.Worker, &m.WorkerConfig, &m.Paused, &m.RecvSeq, &removed, &m.Rev, &created, &m.Owner, &m.Generation, &m.Access)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.CreatedAt = fromMS(created)
	m.RemovedAt = optTime(removed)
	return m, err
}

const dmsgCols = `id,discussion,seq,kind,author_member,author_name,author_agent,body,quote,target,target_name,execution,question,report,outcome,attachments,client_id,key_at,rev,created_at,author_id,mention,mention_name,key_by,choices`

func scanDiscMessage(row interface{ Scan(...any) error }) (DiscMessage, error) {
	var m DiscMessage
	var created int64
	var key sql.NullInt64
	var atts, choices string
	err := row.Scan(&m.ID, &m.Discussion, &m.Seq, &m.Kind, &m.AuthorMember, &m.AuthorName, &m.AuthorAgent, &m.Body, &m.Quote, &m.Target, &m.TargetName,
		&m.Execution, &m.Question, &m.Report, &m.Outcome, &atts, &m.ClientID, &key, &m.Rev, &created, &m.AuthorID, &m.Mention, &m.MentionName, &m.KeyBy, &choices)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if atts != "" {
		json.Unmarshal([]byte(atts), &m.Attachments)
	}
	if choices != "" {
		json.Unmarshal([]byte(choices), &m.Choices)
	}
	m.CreatedAt = fromMS(created)
	m.KeyAt = optTime(key)
	return m, err
}

const execCols = `id,discussion,member,message,retry_of,state,reason,task_id,worker,agent,model,effort,role,workspace_id,workspace_label,config_version,confirmed,length(CAST(context AS BLOB)),acked,reply,rev,created_at,started_at,ended_at,owner,generation,root,depth`

func scanExecution(row interface{ Scan(...any) error }) (Execution, error) {
	var x Execution
	var created int64
	var started, ended sql.NullInt64
	err := row.Scan(&x.ID, &x.Discussion, &x.Member, &x.Message, &x.RetryOf, &x.State, &x.Reason, &x.TaskID, &x.Worker, &x.Agent, &x.Model, &x.Effort, &x.Role,
		&x.WorkspaceID, &x.WorkspaceLabel, &x.ConfigVersion, &x.Confirmed, &x.ContextLen, &x.Acked, &x.Reply, &x.Rev, &created, &started, &ended, &x.Owner, &x.Generation, &x.Root, &x.Depth)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	x.CreatedAt = fromMS(created)
	x.StartedAt, x.EndedAt = optTime(started), optTime(ended)
	return x, err
}

func optTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func collect[T any](q querier, scan func(interface{ Scan(...any) error }) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// withTx 在一个事务里跑 fn，fn 返回错误就回滚。
func (s *Store) withTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// bump 推进全局变更序号和这个讨论的版本，返回新版本；同一事务里改动的行都记这个版本。
func bump(tx querier, key string) (int64, error) {
	var seq, rev int64
	if err := tx.QueryRow(`INSERT INTO disc_meta(k,v) VALUES('seq',1) ON CONFLICT(k) DO UPDATE SET v=v+1 RETURNING v`).Scan(&seq); err != nil {
		return 0, err
	}
	err := tx.QueryRow(`UPDATE discussions SET rev=rev+1, changed=? WHERE key=? RETURNING rev`, seq, key).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return rev, err
}

// DiscussionSeq 是全局变更序号：界面看它没变就不重取列表。
func (s *Store) DiscussionSeq() (int64, error) {
	var v int64
	err := s.db.QueryRow(`SELECT v FROM disc_meta WHERE k='seq'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

// --- 讨论

func (s *Store) CreateDiscussion(d Discussion) error {
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO discussions(key,host,local_id,title,description,project_id,project_name,project_path,workspace_id,status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			d.Key, d.Host, d.LocalID, d.Title, d.Description, d.ProjectID, d.ProjectName, d.ProjectPath, d.WorkspaceID, DiscussionActive, ms(d.CreatedAt)); err != nil {
			if isUnique(err) {
				return fmt.Errorf("%w: discussion id collision", ErrConflict)
			}
			return err
		}
		_, err := bump(tx, d.Key)
		return err
	})
}

// UpdateDiscussion 改标题、描述和默认工作区；归档的讨论只读。
func (s *Store) UpdateDiscussion(key, title, desc, workspaceID string) error {
	return s.withTx(func(tx *sql.Tx) error {
		if err := activeDiscussion(tx, key); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE discussions SET title=?, description=?, workspace_id=? WHERE key=?`, title, desc, workspaceID, key); err != nil {
			return err
		}
		_, err := bump(tx, key)
		return err
	})
}

func (s *Store) Discussion(key string) (Discussion, error) {
	return scanDiscussion(s.db.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, key))
}

// FindDiscussion 按 roomKey 或讨论 ID 找。
func (s *Store) FindDiscussion(sel string) (Discussion, error) {
	return scanDiscussion(s.db.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=? OR local_id=? LIMIT 1`, sel, sel))
}

func (s *Store) Discussions() ([]Discussion, error) {
	return collect(s.db, scanDiscussion, `SELECT `+discCols+` FROM discussions ORDER BY created_at`)
}

func activeDiscussion(tx querier, key string) error {
	var status string
	err := tx.QueryRow(`SELECT status FROM discussions WHERE key=?`, key).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != DiscussionActive {
		return ErrArchived
	}
	return nil
}

// DiscussionSummary 是列表里的一行：最近一条消息、待回答的问题、进行中和排队的请求。
type DiscussionSummary struct {
	Discussion
	Last      *DiscMessage `json:"last,omitempty"`
	Questions int          `json:"questions"`
	Active    int          `json:"active"`
	Pending   int          `json:"pending"`
	Members   int          `json:"members"`
	// 局域网协作：加入的讨论的状态（pending、active、rejected、removed、left），房主待批准的申请数
	Joined   string `json:"joined,omitempty"`
	Requests int    `json:"requests,omitempty"`
	// MentionSeq 是最近一条 @ 本机这个人的消息的序号：窗口据此在列表里标「有人 @ 你」、发通知，不只看最后一条
	MentionSeq int64 `json:"mentionSeq,omitempty"`
}

func (s *Store) DiscussionSummaries() ([]DiscussionSummary, error) {
	ds, err := s.Discussions()
	if err != nil {
		return nil, err
	}
	out := make([]DiscussionSummary, 0, len(ds))
	for _, d := range ds {
		x := DiscussionSummary{Discussion: d}
		if m, err := scanDiscMessage(s.db.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? ORDER BY seq DESC LIMIT 1`, d.Key)); err == nil {
			// 人发的消息存着发送时的名字：预览和消息区一样按身份取人员表里最新的
			if m.AuthorID != "" && m.AuthorMember == "" {
				var name string
				if s.db.QueryRow(`SELECT name FROM collab_people WHERE room=? AND fp=?`, d.Key, m.AuthorID).Scan(&name) == nil && name != "" {
					m.AuthorName = name
				}
			}
			x.Last = &m
		}
		s.db.QueryRow(`SELECT COUNT(*) FROM disc_messages q WHERE q.discussion=? AND q.kind='question'
			AND NOT EXISTS (SELECT 1 FROM disc_messages a WHERE a.discussion=q.discussion AND a.question=q.question AND a.kind='answer')
			AND EXISTS (SELECT 1 FROM disc_executions x WHERE x.id=q.execution AND x.state='waiting' AND x.owner IN ('', ?))`, d.Key, s.Self()).Scan(&x.Questions)
		s.db.QueryRow(`SELECT COUNT(*) FROM disc_executions WHERE discussion=? AND state IN `+inList(activeStates), d.Key).Scan(&x.Active)
		s.db.QueryRow(`SELECT COUNT(*) FROM disc_executions WHERE discussion=? AND state IN `+inList(pendingStates), d.Key).Scan(&x.Pending)
		s.db.QueryRow(`SELECT COUNT(*) FROM disc_members WHERE discussion=? AND removed_at IS NULL`, d.Key).Scan(&x.Members)
		if self := s.Self(); self != "" {
			s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM disc_messages WHERE discussion=? AND mention=?`, d.Key, self).Scan(&x.MentionSeq)
		}
		if d.Mirror {
			s.db.QueryRow(`SELECT state FROM collab_joined WHERE room=?`, d.Key).Scan(&x.Joined)
		} else if d.Shared {
			s.db.QueryRow(`SELECT COUNT(*) FROM collab_requests WHERE room=? AND state='pending'`, d.Key).Scan(&x.Requests)
		}
		out = append(out, x)
	}
	return out, nil
}

// SetArchived 归档或恢复。归档要求没有进行中、排队、等确认、受阻的请求，检查和改状态在同一事务里，
// 和发送、入队互斥（单连接上的事务天然串行）。恢复不重跑旧请求。
func (s *Store) SetArchived(key string, archived bool, at time.Time) error {
	return s.withTx(func(tx *sql.Tx) error {
		d, err := scanDiscussion(tx.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, key))
		if err != nil {
			return err
		}
		if archived {
			if d.Status == DiscussionArchived {
				return nil
			}
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM disc_executions WHERE discussion=? AND state IN `+inList(append(append([]string{}, pendingStates...), activeStates...)), key).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrBusyDiscuss
			}
			_, err = tx.Exec(`UPDATE discussions SET status=?, archived_at=? WHERE key=?`, DiscussionArchived, ms(at), key)
		} else {
			if d.Status == DiscussionActive {
				return nil
			}
			_, err = tx.Exec(`UPDATE discussions SET status=?, archived_at=NULL WHERE key=?`, DiscussionActive, key)
		}
		if err != nil {
			return err
		}
		_, err = bump(tx, key)
		return err
	})
}

// --- 成员

func (s *Store) AddMember(m Member) error {
	return s.withTx(func(tx *sql.Tx) error { return addMember(tx, m, "") })
}

// AddMemberOnce 是成员电脑经房主加自己的 Agent，client 是这次添加的请求 ID：上次房主加好了、应答却丢了，
// 拥有者用同一个 ID 重发时返回已经加好的那个（dup），不再加一个。同一个 ID 的 Agent 已经移除、属于这个人之前的资格，
// 或者设置和这次的不一样时报冲突：不复活、不覆盖。
func (s *Store) AddMemberOnce(m Member, client string) (id string, dup bool, err error) {
	err = s.withTx(func(tx *sql.Tx) error {
		if client != "" {
			old, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE discussion=? AND owner=? AND client_id=?`, m.Discussion, m.Owner, client))
			switch {
			case err == nil:
				if old.RemovedAt != nil || old.Generation != m.Generation {
					return ErrAddGone
				}
				// 和新加一样核对这个人这一代资格还有效（房主自己的 Agent 不查）
				var host string
				if err := tx.QueryRow(`SELECT host FROM discussions WHERE key=?`, m.Discussion).Scan(&host); err != nil {
					return err
				}
				if m.Owner != "" && m.Owner != host {
					if err := checkPerson(tx, m.Discussion, m.Owner, m.Generation); err != nil {
						return err
					}
				}
				if ownConfig(old) != ownConfig(m) {
					return ErrAddChanged
				}
				id, dup = old.ID, true
				return nil
			case !errors.Is(err, ErrNotFound):
				return err
			}
		}
		id = m.ID
		return addMember(tx, m, client)
	})
	return id, dup, err
}

func addMember(tx *sql.Tx, m Member, client string) error {
	if err := activeDiscussion(tx, m.Discussion); err != nil {
		return err
	}
	d, err := scanDiscussion(tx.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, m.Discussion))
	if err != nil {
		return err
	}
	if d.Mirror {
		return fmt.Errorf("%w: agents in a joined discussion are added through its host", ErrConflict)
	}
	if m.Owner != "" && m.Owner != d.Host {
		if err := checkPerson(tx, d.Key, m.Owner, m.Generation); err != nil {
			return err
		}
	}
	if err := checkAccess(tx, m.Discussion, m.Access); err != nil {
		return err
	}
	rev, err := bump(tx, m.Discussion)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO disc_members(id,discussion,name,role,agent,model,effort,workspace_id,workspace_label,config_version,rev,created_at,owner,generation,client_id,access) VALUES(?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,?)`,
		m.ID, m.Discussion, m.Name, m.Role, m.Agent, m.Model, m.Effort, m.WorkspaceID, m.WorkspaceLabel, rev, ms(m.CreatedAt), m.Owner, m.Generation, client, m.Access)
	return err
}

func (s *Store) Member(id string) (Member, error) {
	return scanMember(s.db.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, id))
}

func (s *Store) Members(key string) ([]Member, error) {
	return collect(s.db, scanMember, `SELECT `+memberCols+` FROM disc_members WHERE discussion=? ORDER BY created_at, rowid`, key)
}

// MemberByTask 是绑定着这个任务的成员。
func (s *Store) MemberByTask(taskID string) (Member, error) {
	return scanMember(s.db.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE task_id=? AND task_id!=''`, taskID))
}

// ActiveMembers 是所有没移除的成员（调度器逐个看队列）。
func (s *Store) ActiveMembers() ([]Member, error) {
	// 别人电脑上的 Agent 由它的拥有者领取执行，这里不调度；加入的讨论里拥有者为空的是房主的，也不调度
	return collect(s.db, scanMember, `SELECT `+memberCols+` FROM disc_members WHERE removed_at IS NULL
		AND (owner = ? OR owner = '' AND discussion NOT IN (SELECT key FROM discussions WHERE mirror=1)) ORDER BY created_at`, s.Self())
}

// UpdateMember 改成员配置。名字、职责只影响之后的显示和新 worker 的提示；agent、模型、思考深度、工作区、职责
// 变了就把配置版本加一，已有 worker 继续用旧配置，下一个新 worker 才用新的。
func (s *Store) UpdateMember(m Member) (Member, error) {
	var out Member
	err := s.withTx(func(tx *sql.Tx) error {
		old, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, m.ID))
		if err != nil {
			return err
		}
		if old.RemovedAt != nil {
			return ErrMemberGone
		}
		if err := activeDiscussion(tx, old.Discussion); err != nil {
			return err
		}
		version := old.ConfigVersion
		if old.Agent != m.Agent || old.Model != m.Model || old.Effort != m.Effort || old.WorkspaceID != m.WorkspaceID || old.Role != m.Role {
			version++
		}
		rev, err := bump(tx, old.Discussion)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE disc_members SET name=?, role=?, agent=?, model=?, effort=?, workspace_id=?, workspace_label=?, config_version=?, access=?, rev=? WHERE id=?`,
			m.Name, m.Role, m.Agent, m.Model, m.Effort, m.WorkspaceID, m.WorkspaceLabel, version, m.Access, rev, m.ID); err != nil {
			return err
		}
		out, err = scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, m.ID))
		return err
	})
	return out, err
}

func memberBusy(tx querier, memberID string) (bool, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM disc_executions WHERE member=? AND state IN `+inList(append(append([]string{}, pendingStates...), activeStates...)), memberID).Scan(&n)
	return n > 0, err
}

// RemoveMember 移除成员：有进行中或排队的请求时拒绝。保留历史署名，解开 worker 绑定，返回原来绑定的任务和终端，由调用方关掉。
func (s *Store) RemoveMember(id string, at time.Time) (Member, error) {
	var old Member
	err := s.withTx(func(tx *sql.Tx) error {
		var err error
		if old, err = scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, id)); err != nil {
			return err
		}
		if old.RemovedAt != nil {
			return nil
		}
		if err := activeDiscussion(tx, old.Discussion); err != nil {
			return err
		}
		if busy, err := memberBusy(tx, id); err != nil {
			return err
		} else if busy {
			return ErrBusyMember
		}
		rev, err := bump(tx, old.Discussion)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE disc_members SET removed_at=?, task_id='', worker='', paused='', rev=? WHERE id=?`, ms(at), rev, id)
		return err
	})
	return old, err
}

// UpdateMemberApplyNow 在一个事务里：确认成员没有进行中、排队的请求，改配置，解开 worker 绑定。
// 返回改之前的成员（调用方关掉它绑定的终端）和改之后的成员。有请求时什么都不改。
func (s *Store) UpdateMemberApplyNow(m Member) (old, out Member, err error) {
	err = s.withTx(func(tx *sql.Tx) error {
		if old, err = scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, m.ID)); err != nil {
			return err
		}
		if old.RemovedAt != nil {
			return ErrMemberGone
		}
		if err := activeDiscussion(tx, old.Discussion); err != nil {
			return err
		}
		if busy, err := memberBusy(tx, m.ID); err != nil {
			return err
		} else if busy {
			return ErrBusyMember
		}
		version := old.ConfigVersion
		if old.Agent != m.Agent || old.Model != m.Model || old.Effort != m.Effort || old.WorkspaceID != m.WorkspaceID || old.Role != m.Role {
			version++
		}
		rev, err := bump(tx, old.Discussion)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE disc_members SET name=?, role=?, agent=?, model=?, effort=?, workspace_id=?, workspace_label=?, config_version=?, access=?,
			task_id='', worker='', worker_config=0, paused='', rev=? WHERE id=?`,
			m.Name, m.Role, m.Agent, m.Model, m.Effort, m.WorkspaceID, m.WorkspaceLabel, version, m.Access, rev, m.ID); err != nil {
			return err
		}
		out, err = scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, m.ID))
		return err
	})
	return
}

// ReleaseWorker 解开成员和 worker 的绑定（立即应用新配置、重新启动前），要求没有进行中、排队的请求，
// 除非 force（重新启动并继续：队列留着，接着由新 worker 做）。返回原来绑定的成员。
func (s *Store) ReleaseWorker(id string, force bool) (Member, error) {
	var old Member
	err := s.withTx(func(tx *sql.Tx) error {
		var err error
		if old, err = scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, id)); err != nil {
			return err
		}
		if old.RemovedAt != nil {
			return ErrMemberGone
		}
		if err := activeDiscussion(tx, old.Discussion); err != nil {
			return err
		}
		var n int
		states := activeStates
		if !force {
			states = append(append([]string{}, pendingStates...), activeStates...)
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM disc_executions WHERE member=? AND state IN `+inList(states), id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrBusyMember
		}
		rev, err := bump(tx, old.Discussion)
		if err != nil {
			return err
		}
		// 重新启动不解除配置没确认的暂停：那要拥有者检查、保存设置
		if _, err := tx.Exec(`UPDATE disc_members SET task_id='', worker='', worker_config=0, paused=CASE WHEN paused IN (?,?) THEN paused ELSE '' END, rev=? WHERE id=?`,
			ConfigConflict, NeedsConfirm, rev, id); err != nil {
			return err
		}
		// 受阻的请求回到队列，由新 worker 接着做
		_, err = tx.Exec(`UPDATE disc_executions SET state='queued', reason='', rev=? WHERE member=? AND state='blocked'`, rev, id)
		return err
	})
	return old, err
}

// SetMemberPaused 暂停或恢复成员的队列（reason 为空是恢复）。
func (s *Store) SetMemberPaused(id, reason string) error {
	return s.withTx(func(tx *sql.Tx) error {
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, id))
		if err != nil {
			return err
		}
		if m.Paused == reason {
			return nil
		}
		rev, err := bump(tx, m.Discussion)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE disc_members SET paused=?, rev=? WHERE id=?`, reason, rev, id)
		return err
	})
}

// --- 消息

// PostInput 是用户发一条消息。Target 不为空时同一事务里为它排一轮执行。
type PostInput struct {
	Discussion  string
	ClientID    string // 客户端请求 ID：重复发送返回第一次的结果
	Body        string
	Quote       string
	Target      string
	Attachments []Attachment
	At          time.Time
	ExecID      string
	// 局域网协作：发言的人（指纹，空是房主本机的用户）、他的名字和成员代际，@ 的人（只提醒）
	Author     string
	AuthorName string
	Generation int64
	Mention    string
	Uploads    []string // 成员上传好的附件 ID（房主一侧）
	// From 不为空时这条是成员 @ 成员：From 是发起的那一轮执行，作者记成它的成员，必须带 Target
	From string
}

// PostMessage 保存消息，Target 不为空时同时建一轮排队的执行。ClientID 重复时返回已有的消息和执行，dup 为 true。
func (s *Store) PostMessage(in PostInput) (msg DiscMessage, exec *Execution, dup bool, err error) {
	err = s.withTx(func(tx *sql.Tx) error {
		if in.Author != "" && in.ClientID != "" && !strings.HasPrefix(in.ClientID, in.Author+":") {
			// 别人的客户端请求 ID 按人分开：两个人用了同一个 ID 也不会拿到对方的消息
			in.ClientID = in.Author + ":" + in.ClientID
		}
		if in.ClientID != "" {
			old, err := scanDiscMessage(tx.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND client_id=?`, in.Discussion, in.ClientID))
			if err == nil {
				msg, dup = old, true
				if old.Execution != "" {
					if x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, old.Execution)); err == nil {
						exec = &x
					}
				}
				return nil
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		if err := activeDiscussion(tx, in.Discussion); err != nil {
			return err
		}
		d, err := scanDiscussion(tx.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, in.Discussion))
		if err != nil {
			return err
		}
		if d.Mirror {
			return fmt.Errorf("%w: messages in a joined discussion are sent to its host", ErrConflict)
		}
		author := in.Author
		if author == "" {
			author = d.Host
		}
		if author != d.Host {
			if err := checkPerson(tx, d.Key, author, in.Generation); err != nil {
				return err
			}
		}
		if len(in.Uploads) > 0 {
			atts, err := takeUploads(tx, d.Key, author, in.Uploads)
			if err != nil {
				return err
			}
			in.Attachments = append(in.Attachments, atts...)
		}
		var mentionName string
		if in.Mention != "" {
			if mentionName, err = personName(tx, d, in.Mention); err != nil {
				return err
			}
		}
		if in.Quote != "" {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM disc_messages WHERE id=? AND discussion=?`, in.Quote, in.Discussion).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("%w: quoted message", ErrNotFound)
			}
		}
		// 成员 @ 成员：发起的那一轮要在进行中，在发言人名下；从它算出这条链转到第几层
		var from Execution
		var fromMember Member
		if in.From != "" {
			if from, err = scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=? AND discussion=?`, in.From, in.Discussion)); err != nil {
				return err
			}
			if !ExecActive(from.State) {
				return fmt.Errorf("%w: execution is %s", ErrStaleRound, from.State)
			}
			if fromMember, err = scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, from.Member)); err != nil {
				return err
			}
			if cmpOr(fromMember.Owner, d.Host) != author {
				return ErrNotOwner
			}
			if in.Target == "" || in.Target == from.Member {
				return fmt.Errorf("%w: a member's request needs another member as its target", ErrConflict)
			}
			root := cmpOr(from.Root, from.ID)
			var rounds int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM disc_executions WHERE (root=? OR id=?) AND retry_of=''`, root, root).Scan(&rounds); err != nil {
				return err
			}
			if from.Depth+1 > MaxChainDepth || rounds+1 > MaxChainRounds {
				return ErrChainLimit
			}
		}
		var target Member
		if in.Target != "" {
			var err error
			target, err = scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=? AND discussion=?`, in.Target, in.Discussion))
			if err != nil {
				return err
			}
			if target.RemovedAt != nil {
				return ErrMemberGone
			}
			// 只能 @ 自己的 Agent，和拥有者在访问设置里放开给你的
			if !target.Allows(author, d.Host) {
				return ErrNotOwner
			}
		}
		rev, err := bump(tx, in.Discussion)
		if err != nil {
			return err
		}
		msg = DiscMessage{ID: NewID("dm_") + NewID(""), Discussion: in.Discussion, Kind: DiscUser, Body: in.Body, Quote: in.Quote,
			Target: target.ID, TargetName: target.Name, Attachments: in.Attachments, ClientID: in.ClientID, Rev: rev, CreatedAt: in.At,
			AuthorID: author, AuthorName: in.AuthorName, Mention: in.Mention, MentionName: mentionName}
		if from.ID != "" {
			msg.Kind, msg.AuthorMember, msg.AuthorName, msg.AuthorAgent = DiscDelegate, fromMember.ID, fromMember.Name, fromMember.Agent
		}
		if target.ID != "" {
			x := Execution{ID: in.ExecID, Discussion: in.Discussion, Member: target.ID, Message: msg.ID, State: ExecQueued,
				Agent: target.Agent, Model: target.Model, Effort: target.Effort, Role: target.Role, WorkspaceID: target.WorkspaceID,
				WorkspaceLabel: target.WorkspaceLabel, ConfigVersion: target.ConfigVersion, Rev: rev, CreatedAt: in.At,
				Owner: target.Owner, Generation: target.Generation}
			if from.ID != "" {
				x.Root, x.Depth = cmpOr(from.Root, from.ID), from.Depth+1
			}
			if x.ID == "" {
				x.ID = NewID("x_") + NewID("")
			}
			if err := insertExecution(tx, x); err != nil {
				return err
			}
			msg.Execution = x.ID
			exec = &x
		}
		return insertDiscMessage(tx, &msg)
	})
	return
}

func insertExecution(tx querier, x Execution) error {
	_, err := tx.Exec(`INSERT INTO disc_executions(id,discussion,member,message,retry_of,state,reason,agent,model,effort,role,workspace_id,workspace_label,config_version,rev,created_at,owner,generation,root,depth)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		x.ID, x.Discussion, x.Member, x.Message, x.RetryOf, x.State, x.Reason, x.Agent, x.Model, x.Effort, x.Role, x.WorkspaceID, x.WorkspaceLabel, x.ConfigVersion, x.Rev, ms(x.CreatedAt), x.Owner, x.Generation, x.Root, x.Depth)
	if isUnique(err) {
		return fmt.Errorf("%w: execution for this message exists", ErrConflict)
	}
	return err
}

// insertDiscMessage 分配讨论内的序号并写入；source 不为空时按它去重（重复投影返回 ErrConflict）。
func insertDiscMessage(tx querier, m *DiscMessage) error {
	return insertDiscMessageSrc(tx, m, "")
}

func insertDiscMessageSrc(tx querier, m *DiscMessage, source string) error {
	if err := tx.QueryRow(`UPDATE discussions SET msg_seq=msg_seq+1 WHERE key=? RETURNING msg_seq`, m.Discussion).Scan(&m.Seq); err != nil {
		return err
	}
	return insertDiscMessageAt(tx, m, source)
}

// insertDiscMessageAt 按 m.Seq 写入（加入者缓存房主的消息时用房主分配的序号）。
func insertDiscMessageAt(tx querier, m *DiscMessage, source string) error {
	var atts string
	if len(m.Attachments) > 0 {
		b, _ := json.Marshal(m.Attachments)
		atts = string(b)
	}
	var key any
	if m.KeyAt != nil {
		key = ms(*m.KeyAt)
	}
	var choices string
	if len(m.Choices) > 0 {
		b, _ := json.Marshal(m.Choices)
		choices = string(b)
	}
	_, err := tx.Exec(`INSERT INTO disc_messages(id,discussion,seq,kind,author_member,author_name,author_agent,body,quote,target,target_name,execution,question,report,outcome,attachments,client_id,source,rev,created_at,author_id,mention,mention_name,key_at,key_by,choices)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.Discussion, m.Seq, m.Kind, m.AuthorMember, m.AuthorName, m.AuthorAgent, m.Body, m.Quote, m.Target, m.TargetName, m.Execution, m.Question,
		m.Report, m.Outcome, atts, m.ClientID, source, m.Rev, ms(m.CreatedAt), m.AuthorID, m.Mention, m.MentionName, key, m.KeyBy, choices)
	if isUnique(err) {
		return fmt.Errorf("%w: duplicate message", ErrConflict)
	}
	return err
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// MessageByClientID 找这个客户端请求 ID 已经写下的消息和它的执行。
func (s *Store) MessageByClientID(key, clientID string) (DiscMessage, *Execution, error) {
	m, err := scanDiscMessage(s.db.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND client_id=?`, key, clientID))
	if err != nil {
		return m, nil, err
	}
	if m.Execution == "" {
		return m, nil, nil
	}
	x, err := s.Execution(m.Execution)
	if err != nil {
		return m, nil, nil
	}
	return m, &x, nil
}

func (s *Store) DiscMessage(id string) (DiscMessage, error) {
	return scanDiscMessage(s.db.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE id=?`, id))
}

// DiscMessages 按序号分页：before > 0 时取它之前的 limit 条，否则取最新的 limit 条；结果按序号升序。
func (s *Store) DiscMessages(key string, before int64, limit int) ([]DiscMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + dmsgCols + ` FROM disc_messages WHERE discussion=?`
	args := []any{key}
	if before > 0 {
		q += ` AND seq<?`
		args = append(args, before)
	}
	out, err := collect(s.db, scanDiscMessage, q+` ORDER BY seq DESC LIMIT ?`, append(args, limit)...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}

// DiscMessagesAround 取以 seq 为中心的一页（定位到远处的搜索结果、引用、关键消息），按序号升序。
func (s *Store) DiscMessagesAround(key string, seq int64, limit int) ([]DiscMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	lo := max(1, seq-int64(limit/2))
	return collect(s.db, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND seq>=? ORDER BY seq LIMIT ?`, key, lo, limit)
}

// DiscMessagesAfter 取序号大于 after 的一页（往后翻），按序号升序。
func (s *Store) DiscMessagesAfter(key string, after int64, limit int) ([]DiscMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	return collect(s.db, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND seq>? ORDER BY seq LIMIT ?`, key, after, limit)
}

// DiscMessageRange 取序号在 [from, to] 的消息，最多 limit 条。
func (s *Store) DiscMessageRange(key string, from, to int64, limit int) ([]DiscMessage, error) {
	return collect(s.db, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND seq>=? AND seq<=? ORDER BY seq LIMIT ?`, key, from, to, limit)
}

// DiscChanges 是版本 since 之后改过的消息、执行和成员：增量接口不能只按最新消息 ID，状态和关键标记也会变。
func (s *Store) DiscChanges(key string, since int64) ([]DiscMessage, []Execution, []Member, error) {
	msgs, err := collect(s.db, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND rev>? ORDER BY seq`, key, since)
	if err != nil {
		return nil, nil, nil, err
	}
	xs, err := collect(s.db, scanExecution, `SELECT `+execCols+` FROM disc_executions WHERE discussion=? AND rev>? ORDER BY created_at, rowid`, key, since)
	if err != nil {
		return nil, nil, nil, err
	}
	members, err := collect(s.db, scanMember, `SELECT `+memberCols+` FROM disc_members WHERE discussion=? AND rev>? ORDER BY created_at, rowid`, key, since)
	return msgs, xs, members, err
}

// KeyMessages 是讨论里标成关键的消息。
func (s *Store) KeyMessages(key string) ([]DiscMessage, error) {
	return collect(s.db, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND key_at IS NOT NULL ORDER BY seq`, key)
}

// SetKeyMessage 是房主本机的用户标记或取消关键消息；归档后只读。
func (s *Store) SetKeyMessage(id string, on bool, at time.Time) error {
	return s.SetKeyMessageBy(id, on, "", 0, at)
}

// SetKeyMessageBy 标记或取消关键消息并记下操作的人（by 为空是房主本机的用户）。群成员都能标记；
// 取消只由标记的人或房主做。别人的请求在同一事务里复核成员资格。
func (s *Store) SetKeyMessageBy(id string, on bool, by string, gen int64, at time.Time) error {
	return s.withTx(func(tx *sql.Tx) error {
		m, err := scanDiscMessage(tx.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE id=?`, id))
		if err != nil {
			return err
		}
		if err := activeDiscussion(tx, m.Discussion); err != nil {
			return err
		}
		d, err := scanDiscussion(tx.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, m.Discussion))
		if err != nil {
			return err
		}
		if d.Mirror {
			return fmt.Errorf("%w: key marks in a joined discussion are set on its host", ErrConflict)
		}
		if by == "" {
			by = d.Host
		}
		if by != d.Host {
			if err := checkPerson(tx, d.Key, by, gen); err != nil {
				return err
			}
		}
		if (m.KeyAt != nil) == on {
			return nil
		}
		if !on && by != d.Host && m.KeyBy != "" && m.KeyBy != by {
			return fmt.Errorf("%w: only the person who marked it or the host can unmark it", ErrNotOwner)
		}
		rev, err := bump(tx, m.Discussion)
		if err != nil {
			return err
		}
		var v any
		keyBy := ""
		if on {
			v, keyBy = ms(at), by
		}
		_, err = tx.Exec(`UPDATE disc_messages SET key_at=?, key_by=?, rev=? WHERE id=?`, v, keyBy, rev, id)
		return err
	})
}

// SearchDiscussion 在一个讨论的消息正文和附件名里找，新的在前，最多 limit 条。
func (s *Store) SearchDiscussion(key, query string, limit int) ([]DiscMessage, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	pat := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
	return collect(s.db, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND (body LIKE ? ESCAPE '\' OR attachments LIKE ? ESCAPE '\' OR author_name LIKE ? ESCAPE '\')
		ORDER BY seq DESC LIMIT ?`, key, pat, pat, pat, limit)
}

// --- 执行

func (s *Store) Execution(id string) (Execution, error) {
	return scanExecution(s.db.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, id))
}

// ExecutionsFor 是这些消息上的执行，加上讨论里还没结束的（进行中、排队、等确认、受阻）：
// 打开讨论只取读进来的那几页消息涉及的执行，不全量加载。
func (s *Store) ExecutionsFor(key string, msgIDs []string) ([]Execution, error) {
	q := `SELECT ` + execCols + ` FROM disc_executions WHERE discussion=? AND (state IN ` + inList(append(append([]string{}, activeStates...), pendingStates...))
	args := []any{key}
	if len(msgIDs) > 0 {
		q += ` OR message IN (?` + strings.Repeat(",?", len(msgIDs)-1) + `)`
		for _, id := range msgIDs {
			args = append(args, id)
		}
	}
	return collect(s.db, scanExecution, q+`) ORDER BY created_at, rowid`, args...)
}

// DiscWorkers 是讨论里出现过的 worker 终端：成员绑定的和执行记过的。
func (s *Store) DiscWorkers(key string) ([]string, error) {
	rows, err := s.db.Query(`SELECT worker FROM disc_members WHERE discussion=? AND worker!='' UNION SELECT worker FROM disc_executions WHERE discussion=? AND worker!=''`, key, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Executions 是讨论里的执行；ids 不为空时只取这些。
func (s *Store) Executions(key string) ([]Execution, error) {
	return collect(s.db, scanExecution, `SELECT `+execCols+` FROM disc_executions WHERE discussion=? ORDER BY created_at, rowid`, key)
}

// MemberQueue 是成员还没结束的执行（进行中的和排队的），按入队先后。
func (s *Store) MemberQueue(memberID string) ([]Execution, error) {
	return collect(s.db, scanExecution, `SELECT `+execCols+` FROM disc_executions WHERE member=? AND state IN `+
		inList(append(append([]string{}, activeStates...), pendingStates...))+` ORDER BY created_at, rowid`, memberID)
}

// ActiveExecutions 是所有正在进行的轮次（同目录并发检查、启动对账）。
func (s *Store) ActiveExecutions() ([]Execution, error) {
	return collect(s.db, scanExecution, `SELECT `+execCols+` FROM disc_executions WHERE state IN `+inList(activeStates)+` ORDER BY created_at`)
}

// ActiveExecutionForTask 是这个任务正在进行的轮次。
func (s *Store) ActiveExecutionForTask(taskID string) (Execution, error) {
	return scanExecution(s.db.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE task_id=? AND state IN `+inList(activeStates)+` LIMIT 1`, taskID))
}

func setExec(tx querier, key, id, from string, sets string, args ...any) (bool, error) {
	rev, err := bump(tx, key)
	if err != nil {
		return false, err
	}
	q := `UPDATE disc_executions SET ` + sets + `, rev=? WHERE id=?`
	args = append(args, rev, id)
	if from != "" {
		q += ` AND state IN ` + from
	}
	res, err := tx.Exec(q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	// 加入的讨论里自己 Agent 的执行：状态变化写进 outbox，由协作连接补发给房主
	return true, emitExecState(tx, key, id)
}

// changeExec 在讨论没归档时把执行从 from 中的状态改成 to；返回是否改了。
func (s *Store) changeExec(id string, from []string, to, reason string, requireActive bool) (bool, error) {
	changed := false
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, id))
		if err != nil {
			return err
		}
		if requireActive {
			if err := activeDiscussion(tx, x.Discussion); err != nil {
				return err
			}
		}
		end := ""
		var args []any
		if !ExecActive(to) && !ExecPending(to) {
			end = ", ended_at=?"
			args = append(args, ms(time.Now()))
		}
		changed, err = setExec(tx, x.Discussion, id, inList(from), "state=?, reason=?"+end, append([]any{to, reason}, args...)...)
		if err != nil {
			return err
		}
		if !changed {
			return ErrConflict // 回滚 bump
		}
		return nil
	})
	if errors.Is(err, ErrConflict) && !changed {
		return false, nil
	}
	return changed, err
}

// CancelExecution 取消还没开始的请求，源消息不删。
func (s *Store) CancelExecution(id string) (bool, error) {
	ok, err := s.changeExec(id, pendingStates, ExecCancelled, "", false)
	if err != nil || !ok {
		return ok, err
	}
	// 并进这一轮的后续请求（领取后又回到队列时留下的）跟着取消，不留成没人做的「已合并」
	return ok, s.withTx(func(tx *sql.Tx) error {
		var key string
		var n int
		if err := tx.QueryRow(`SELECT discussion, (SELECT COUNT(*) FROM disc_executions f WHERE f.state=? AND f.reason=x.id) FROM disc_executions x WHERE id=?`, ExecMerged, id).Scan(&key, &n); err != nil || n == 0 {
			return err
		}
		rev, err := bump(tx, key)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE disc_executions SET state=?, reason='', rev=? WHERE discussion=? AND state=? AND reason=?`, ExecCancelled, rev, key, ExecMerged, id)
		return err
	})
}

// HoldExecution 派发前发现同一目录有别的讨论 worker 在执行，先停下等用户确认。
func (s *Store) HoldExecution(id, reason string) (bool, error) {
	return s.changeExec(id, []string{ExecQueued}, ExecHeld, reason, false)
}

// ConfirmExecution 用户明确继续：回到队列，记下已确认（这一轮不再检查同目录并发）。
func (s *Store) ConfirmExecution(id string) (bool, error) {
	changed := false
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, id))
		if err != nil {
			return err
		}
		if err := activeDiscussion(tx, x.Discussion); err != nil {
			return err
		}
		changed, err = setExec(tx, x.Discussion, id, "('held')", "state='queued', reason='', confirmed=1")
		if err == nil && !changed {
			return ErrConflict
		}
		return err
	})
	if errors.Is(err, ErrConflict) {
		return false, nil
	}
	return changed, err
}

// ReleaseHold 让等确认的请求回到队列（冲突已经没有了），不记成用户确认过。
func (s *Store) ReleaseHold(id string) (bool, error) {
	return s.changeExec(id, []string{ExecHeld}, ExecQueued, "", true)
}

// BlockExecution 上一轮的 worker 已不在：标成受阻，暂停成员队列，等用户「重新启动并继续」。
func (s *Store) BlockExecution(id, memberID, reason string) (bool, error) {
	changed := false
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, id))
		if err != nil {
			return err
		}
		if changed, err = setExec(tx, x.Discussion, id, "('queued')", "state='blocked', reason=?", reason); err != nil || !changed {
			if err == nil {
				err = ErrConflict
			}
			return err
		}
		_, err = tx.Exec(`UPDATE disc_members SET paused=?, rev=(SELECT rev FROM discussions WHERE key=?) WHERE id=?`, reason, x.Discussion, memberID)
		return err
	})
	if errors.Is(err, ErrConflict) && !changed {
		return false, nil
	}
	return changed, err
}

// RetryExecution 为失败、中断、停止、取消的执行另排一轮（同一源消息，记下 retry_of），不自动重试。
func (s *Store) RetryExecution(id, newID string, at time.Time) (Execution, error) {
	var nx Execution
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, id))
		if err != nil {
			return err
		}
		if err := activeDiscussion(tx, x.Discussion); err != nil {
			return err
		}
		switch x.State {
		case ExecFailed, ExecInterrupted, ExecStopped, ExecCancelled:
		default:
			return fmt.Errorf("%w: execution is %s", ErrConflict, x.State)
		}
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, x.Member))
		if err != nil {
			return err
		}
		if m.RemovedAt != nil {
			return ErrMemberGone
		}
		rev, err := bump(tx, x.Discussion)
		if err != nil {
			return err
		}
		nx = Execution{ID: newID, Discussion: x.Discussion, Member: m.ID, Message: x.Message, RetryOf: x.ID, State: ExecQueued,
			Agent: m.Agent, Model: m.Model, Effort: m.Effort, Role: m.Role, WorkspaceID: m.WorkspaceID, WorkspaceLabel: m.WorkspaceLabel,
			ConfigVersion: m.ConfigVersion, Rev: rev, CreatedAt: at, Owner: m.Owner, Generation: m.Generation, Root: x.Root, Depth: x.Depth}
		// 一轮只能重试一次（retry_of 唯一）：重复点、CLI 重跑都不会多排
		if err := insertExecution(tx, nx); errors.Is(err, ErrConflict) {
			return ErrRetried
		} else if err != nil {
			return err
		}
		// 并进旧一轮的后续请求跟到新的一轮上
		_, err = tx.Exec(`UPDATE disc_executions SET reason=?, rev=? WHERE discussion=? AND state=? AND reason=?`, nx.ID, rev, x.Discussion, ExecMerged, x.ID)
		return err
	})
	return nx, err
}

// --- 派发

// ContextSource 是生成上下文快照要的讨论内容，由 store 在事务里取。
type ContextSource struct {
	Discussion  Discussion
	ProjectPath string // 项目现在的目录（项目移除了就用快照）
	Member      Member
	Members     []Member
	Request     DiscMessage
	Quote       *DiscMessage
	// More 是并进这一轮的后续请求（同一个人连着发的），MoreQuotes 是它们引用的消息
	More       []DiscMessage
	MoreQuotes []DiscMessage
	Key        []DiscMessage
	Unreceived []DiscMessage // 成员还没接收过的、在请求之前的消息
	Exec       Execution
	// Skipped 不为 0 时，成员没接收过的消息里序号到 SkippedTo 为止的 Skipped 条由讨论摘要代替，不在 Unreceived 里
	Skipped   int
	SkippedTo int64
}

// RenderContext 把 ContextSource 写成固定的上下文正文，并记下每条消息在正文里结束的位置（推进接收位置用）。
type RenderContext func(ContextSource) (text string, ends []ContextEnd)

// ContextEnd 是快照里一条消息在正文里结束的字节位置。
type ContextEnd struct {
	Seq int64 `json:"s"`
	End int   `json:"e"`
}

// ClaimInput 是领取一轮：queued → starting（要新 worker）或 running（沿用已有 worker）。
type ClaimInput struct {
	Exec   string
	TaskID string // 预分配的任务 ID（新 worker）或已绑定的任务
	Reuse  bool   // 沿用成员已绑定的 worker：同一事务里重新打开任务、追加提醒
	Note   Message
	Render RenderContext
	At     time.Time
}

// ClaimExecution 条件领取：只有 state=queued、讨论没归档、成员没有别的进行中轮次、没暂停时才成功。
// 同一时刻两代常驻进程都来领，只有一个改得动这一行。成功时冻结上下文快照。
func (s *Store) ClaimExecution(in ClaimInput) (Execution, bool, error) {
	var out Execution
	claimed := false
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, in.Exec))
		if err != nil {
			return err
		}
		if x.State != ExecQueued {
			return nil
		}
		if err := activeDiscussion(tx, x.Discussion); err != nil {
			return err
		}
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, x.Member))
		if err != nil {
			return err
		}
		if m.RemovedAt != nil || m.Paused != "" {
			return nil
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM disc_executions WHERE member=? AND state IN `+inList(activeStates), m.ID).Scan(&n); err != nil || n > 0 {
			return err
		}
		cfg, worker, role := m, "", m.Role
		state := ExecStarting
		if in.Reuse {
			if m.TaskID != in.TaskID {
				return nil
			}
			state, worker = ExecRunning, m.Worker
			// 沿用的 worker 用的是它启动时的配置：快照照实记上一轮那一份
			if prev, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE task_id=? ORDER BY started_at DESC LIMIT 1`, in.TaskID)); err == nil {
				cfg.Agent, cfg.Model, cfg.Effort, cfg.Role, cfg.WorkspaceID, cfg.WorkspaceLabel, cfg.ConfigVersion =
					prev.Agent, prev.Model, prev.Effort, prev.Role, prev.WorkspaceID, prev.WorkspaceLabel, prev.ConfigVersion
				role = prev.Role
			}
			// 上一轮 done 之后才能接着用：任务回到运行中，提醒和轮次在同一事务里
			res, err := tx.Exec(`UPDATE tasks SET status='running', ended_at=NULL WHERE id=? AND status IN ('done','failed')`, in.TaskID)
			if err != nil {
				return err
			}
			if k, _ := res.RowsAffected(); k == 0 {
				return nil
			}
			if err := addMessage(tx, in.Note); err != nil {
				return err
			}
		}
		// 到这里这一轮确定能领：把紧跟着的同一个人的请求并进来，再冻结上下文。后面改不动这一行时整个回滚，并进来的不算
		if err := mergeFollowers(tx, x, in.At); err != nil {
			return err
		}
		src, err := contextSource(tx, x, m)
		if err != nil {
			return err
		}
		src.Member.Role = role
		text, ends := in.Render(src)
		idx, _ := json.Marshal(ends)
		ok, err := setExec(tx, x.Discussion, x.ID, "('queued')",
			"state=?, task_id=?, worker=?, agent=?, model=?, effort=?, role=?, workspace_id=?, workspace_label=?, config_version=?, context=?, context_index=?, context_from=?, context_upto=?, acked=0, started_at=?",
			state, in.TaskID, worker, cfg.Agent, cfg.Model, cfg.Effort, cfg.Role, cfg.WorkspaceID, cfg.WorkspaceLabel, cfg.ConfigVersion,
			text, string(idx), m.RecvSeq, src.lastRequest(), ms(in.At))
		if err != nil {
			return err
		}
		if !ok {
			return errNoChange
		}
		claimed = true
		out, err = scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, x.ID))
		return err
	})
	if errors.Is(err, errNoChange) {
		err = nil
	}
	return out, claimed, err
}

// lastRequest 是这一轮最后一条请求的序号（并进来的排在后面）。
func (src ContextSource) lastRequest() int64 {
	if n := len(src.More); n > 0 {
		return src.More[n-1].Seq
	}
	return src.Request.Seq
}

// mergeFollowers 在领取 x 时把紧跟在它后面、同一个发言人发给同一成员、还在排队的请求并进这一轮：
// 它们转 merged（reason 记 x），正文作为这一轮的后续请求一起交给成员，由这一轮的一条回复一并回答。
// 只并用户直接发的（不并重试、成员 @ 成员的）；加入的讨论不并：那些轮次的状态由房主记，房主不认这个状态。
func mergeFollowers(tx querier, x Execution, at time.Time) error {
	if x.RetryOf != "" || x.Root != "" {
		return nil
	}
	if mirror, err := isMirror(tx, x.Discussion); err != nil || mirror {
		return err
	}
	head, err := scanDiscMessage(tx.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE id=?`, x.Message))
	if err != nil || head.Kind != DiscUser {
		return err
	}
	queue, err := collect(tx, scanExecution, `SELECT `+execCols+` FROM disc_executions WHERE member=? AND state IN `+inList(pendingStates)+` AND id!=? ORDER BY created_at, rowid`, x.Member, x.ID)
	if err != nil {
		return err
	}
	var ids []string
	for _, f := range queue {
		// 只并紧跟着的：中间隔着别人的请求、等确认的请求就停，不打乱顺序
		if f.State != ExecQueued || f.RetryOf != "" || f.Root != "" || f.Owner != x.Owner {
			break
		}
		fm, err := scanDiscMessage(tx.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE id=?`, f.Message))
		if err != nil {
			return err
		}
		if fm.Kind != DiscUser || fm.AuthorID != head.AuthorID || fm.Seq < head.Seq {
			break
		}
		ids = append(ids, f.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	rev, err := bump(tx, x.Discussion)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE disc_executions SET state=?, reason=?, ended_at=?, rev=? WHERE id=? AND state='queued'`, ExecMerged, x.ID, ms(at), rev, id); err != nil {
			return err
		}
	}
	return nil
}

func contextSource(tx querier, x Execution, m Member) (ContextSource, error) {
	src := ContextSource{Member: m, Exec: x}
	var err error
	if src.Discussion, err = scanDiscussion(tx.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, x.Discussion)); err != nil {
		return src, err
	}
	src.ProjectPath = src.Discussion.ProjectPath
	var p string
	if tx.QueryRow(`SELECT path FROM projects WHERE id=?`, src.Discussion.ProjectID).Scan(&p) == nil {
		src.ProjectPath = p
	}
	if src.Members, err = collect(tx, scanMember, `SELECT `+memberCols+` FROM disc_members WHERE discussion=? AND removed_at IS NULL ORDER BY created_at, rowid`, x.Discussion); err != nil {
		return src, err
	}
	if src.Request, err = scanDiscMessage(tx.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE id=?`, x.Message)); err != nil {
		return src, err
	}
	if src.Request.Quote != "" {
		if q, err := scanDiscMessage(tx.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE id=?`, src.Request.Quote)); err == nil {
			src.Quote = &q
		}
	}
	if src.More, err = collect(tx, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE id IN
		(SELECT message FROM disc_executions WHERE discussion=? AND state=? AND reason=?) ORDER BY seq`, x.Discussion, ExecMerged, x.ID); err != nil {
		return src, err
	}
	for _, r := range src.More {
		if r.Quote == "" {
			continue
		}
		if q, err := scanDiscMessage(tx.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE id=?`, r.Quote)); err == nil {
			src.MoreQuotes = append(src.MoreQuotes, q)
		}
	}
	last := src.lastRequest()
	if src.Key, err = collect(tx, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND key_at IS NOT NULL AND seq<? ORDER BY seq`, x.Discussion, last); err != nil {
		return src, err
	}
	from := m.RecvSeq
	if d := src.Discussion; d.Summary != "" && d.SummarySeq > from {
		// 摘要覆盖的那一段落后太多：用摘要代替，成员要细节时按序号补读
		upto := min(d.SummarySeq, src.Request.Seq-1)
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM disc_messages WHERE discussion=? AND seq>? AND seq<=?`, x.Discussion, from, upto).Scan(&n); err != nil {
			return src, err
		}
		if n > SummarySkip {
			src.Skipped, src.SkippedTo, from = n, upto, upto
		}
	}
	// 并进来的请求之间别人说的话照常给（摘要只代替第一条请求之前的）
	src.Unreceived, err = collect(tx, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND seq>? AND seq<? ORDER BY seq`, x.Discussion, from, last)
	return src, err
}

// --- 摘要

// SummaryInput 是成员在自己的一轮里写讨论摘要。
type SummaryInput struct {
	RoundCheck
	Body string
	At   time.Time
}

// SetSummary 保存成员写的摘要：核对轮次，摘要覆盖到这一轮的请求为止。加入的讨论交给房主（事件）。
func (s *Store) SetSummary(in SummaryInput) (Discussion, error) {
	var d Discussion
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := checkRound(tx, in.RoundCheck, ExecRunning, ExecWaiting)
		if err != nil {
			return err
		}
		if x.Acked < x.ContextLen {
			return fmt.Errorf("%w: read %d of %d bytes", ErrNotAcked, x.Acked, x.ContextLen)
		}
		if mirror, err := isMirror(tx, x.Discussion); err != nil {
			return err
		} else if mirror {
			if !hostHas(tx, x.Discussion, FeatureSummary) {
				return fmt.Errorf("%w: the host of this discussion runs an older Posse that has no summaries", ErrConflict)
			}
			if err := addOutbox(tx, x.Discussion, x.ID, OwnerEvent{Kind: EventSummary, Body: in.Body, At: ms(in.At)}); err != nil {
				return err
			}
		} else if err := writeSummary(tx, x, in.Body, in.At); err != nil {
			return err
		}
		d, err = scanDiscussion(tx.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, x.Discussion))
		return err
	})
	return d, err
}

// writeSummary 把摘要写到讨论上（房主一侧）：覆盖到执行 x 的请求消息为止，不往回退。
func writeSummary(tx querier, x Execution, body string, at time.Time) error {
	var seq int64
	if err := tx.QueryRow(`SELECT seq FROM disc_messages WHERE id=?`, x.Message).Scan(&seq); err != nil {
		return err
	}
	var name string
	if err := tx.QueryRow(`SELECT name FROM disc_members WHERE id=?`, x.Member).Scan(&name); err != nil {
		return err
	}
	rev, err := bump(tx, x.Discussion)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE discussions SET summary=?, summary_seq=MAX(summary_seq, ?), summary_by=?, summary_at=?, rev=? WHERE key=?`,
		body, seq, name, ms(at), rev, x.Discussion)
	return err
}

// ClearSummary 去掉讨论的摘要（用户的操作，只在房主一侧）。
func (s *Store) ClearSummary(key string) error {
	return s.withTx(func(tx *sql.Tx) error {
		if err := activeDiscussion(tx, key); err != nil {
			return err
		}
		if mirror, err := isMirror(tx, key); err != nil {
			return err
		} else if mirror {
			return fmt.Errorf("%w: only the host can clear the summary", ErrConflict)
		}
		rev, err := bump(tx, key)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE discussions SET summary='', summary_seq=0, summary_by='', summary_at=0, rev=? WHERE key=?`, rev, key)
		return err
	})
}

// BindWorker 把新拉起的 worker 绑到 starting 的轮次上：同一事务里登记任务、轮次转 running、成员绑定 worker。
// 轮次已经不是 starting（被对账标成失败）时返回 ErrStaleRound，调用方关掉刚拉起的终端。
func (s *Store) BindWorker(execID string, t Task) error {
	return s.withTx(func(tx *sql.Tx) error {
		x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, execID))
		if err != nil {
			return err
		}
		if x.State != ExecStarting || x.TaskID != t.ID {
			return ErrStaleRound
		}
		if err := addTask(tx, t); err != nil {
			return err
		}
		ok, err := setExec(tx, x.Discussion, x.ID, "('starting')", "state='running', worker=?", t.Worker)
		if err != nil {
			return err
		}
		if !ok {
			return ErrStaleRound
		}
		_, err = tx.Exec(`UPDATE disc_members SET task_id=?, worker=?, worker_config=?, rev=(SELECT rev FROM discussions WHERE key=?) WHERE id=?`,
			t.ID, t.Worker, x.ConfigVersion, x.Discussion, x.Member)
		return err
	})
}

// StartFailed 拉起 worker 失败：starting → failed，写明原因，暂停成员队列（不自动循环重试）。
// 拉起前就知道没启动（交接中、讨论暂停派发）时 requeue 为 true，回到队列。
func (s *Store) StartFailed(execID, reason string, requeue bool) error {
	return s.withTx(func(tx *sql.Tx) error {
		x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, execID))
		if err != nil {
			return err
		}
		if requeue {
			_, err = setExec(tx, x.Discussion, x.ID, "('starting','running')", "state='queued', task_id='', worker='', context='', context_index='', acked=0, started_at=NULL")
			return err
		}
		ok, err := setExec(tx, x.Discussion, x.ID, "('starting','running')", "state='failed', reason=?, ended_at=?", reason, ms(time.Now()))
		if err != nil || !ok {
			return err
		}
		_, err = tx.Exec(`UPDATE disc_members SET paused=?, rev=(SELECT rev FROM discussions WHERE key=?) WHERE id=?`, "start failed", x.Discussion, x.Member)
		return err
	})
}

// --- 读取上下文

// ContextPage 是一页上下文正文。
type ContextPage struct {
	Execution string `json:"execution"`
	Text      string `json:"text"`
	From      int    `json:"from"`
	To        int    `json:"to"`
	Total     int    `json:"total"`
	Complete  bool   `json:"complete"` // 读到这一页就读完了
	Acked     int    `json:"acked"`
}

// ReadContext 从 from 字节处读最多 limit 字节（落在 UTF-8 字符边界上）。
func (s *Store) ReadContext(execID string, from, limit int) (ContextPage, Execution, error) {
	var text string
	x, err := scanExecution(s.db.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, execID))
	if err != nil {
		return ContextPage{}, x, err
	}
	if err := s.db.QueryRow(`SELECT context FROM disc_executions WHERE id=?`, execID).Scan(&text); err != nil {
		return ContextPage{}, x, err
	}
	from = max(0, min(from, len(text)))
	for from > 0 && from < len(text) && !utf8.RuneStart(text[from]) {
		from--
	}
	to := min(len(text), from+limit)
	if to < len(text) {
		for to > from && !utf8.RuneStart(text[to]) {
			to--
		}
		if to == from {
			// 一个字符都放不下的极小 limit：至少给一个完整字符
			_, n := utf8.DecodeRuneInString(text[from:])
			to = from + n
		}
	}
	return ContextPage{Execution: x.ID, Text: text[from:to], From: from, To: to, Total: len(text), Complete: to == len(text), Acked: x.Acked}, x, nil
}

// AckContext 确认 CLI 已取得 [from, to)：只有和已确认部分相连时才推进；全部连续覆盖的消息推进成员的接收位置。
// 重复确认是幂等的。确认只说明 CLI 拿到了内容，不说明模型理解了。
func (s *Store) AckContext(execID string, from, to int) (int, error) {
	acked := 0
	err := s.withTx(func(tx *sql.Tx) error {
		var key, member, idx string
		var cfrom, total int64
		if err := tx.QueryRow(`SELECT discussion, member, acked, context_index, context_from, length(CAST(context AS BLOB)) FROM disc_executions WHERE id=?`, execID).
			Scan(&key, &member, &acked, &idx, &cfrom, &total); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if from > acked || to <= acked {
			return nil
		}
		acked = min(to, int(total))
		rev, err := bump(tx, key)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE disc_executions SET acked=?, rev=? WHERE id=?`, acked, rev, execID); err != nil {
			return err
		}
		var ends []ContextEnd
		json.Unmarshal([]byte(idx), &ends)
		// 快照里的消息按序号排好的前缀都确认了，接收位置才推到那里；读到较新的关键消息不跨过未读区间
		recv := int64(-1)
		bySeq := map[int64]int{}
		var seqs []int64
		for _, e := range ends {
			if old, ok := bySeq[e.Seq]; !ok || e.End > old {
				if !ok {
					seqs = append(seqs, e.Seq)
				}
				bySeq[e.Seq] = e.End
			}
		}
		sortInt64(seqs)
		for _, sq := range seqs {
			if bySeq[sq] > acked {
				break
			}
			recv = sq
		}
		if recv > cfrom {
			_, err = tx.Exec(`UPDATE disc_members SET recv_seq=MAX(recv_seq, ?), rev=? WHERE id=?`, recv, rev, member)
		}
		return err
	})
	return acked, err
}

func sortInt64(v []int64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// --- worker 汇报

// RoundCheck 是汇报时要对上的：任务、worker 和当前轮次。
type RoundCheck struct {
	Exec   string
	TaskID string
	Worker string // 为空不检查（用户在终端之外手动汇报）
}

func checkRound(tx querier, rc RoundCheck, states ...string) (Execution, error) {
	x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, rc.Exec))
	if errors.Is(err, ErrNotFound) {
		return x, ErrStaleRound
	}
	if err != nil {
		return x, err
	}
	if x.TaskID != rc.TaskID || rc.Worker != "" && x.Worker != rc.Worker {
		return x, ErrStaleRound
	}
	for _, st := range states {
		if x.State == st {
			return x, nil
		}
	}
	return x, ErrStaleRound
}

// FinishInput 是成员汇报这一轮。
type FinishInput struct {
	RoundCheck
	Failed  bool
	Body    string // 完整回复，显示在讨论里
	Summary string
	Report  string
	TaskMsg Message // 任务的 done 消息
	At      time.Time
}

// FinishExecution 在同一事务里：核对轮次（任务、worker、running）、确认上下文已读完、结束任务、写任务 done 消息、
// 写讨论回复、轮次转 done/failed。迟到或重复的汇报对不上当前轮次，返回 ErrStaleRound，不会结束下一轮。
func (s *Store) FinishExecution(in FinishInput) (DiscMessage, error) {
	var reply DiscMessage
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := checkRound(tx, in.RoundCheck, ExecRunning, ExecWaiting)
		outcome := TaskDone
		if in.Failed {
			outcome = TaskFailed
		}
		if errors.Is(err, ErrStaleRound) && (x.State == ExecDone || x.State == ExecFailed) && x.TaskID == in.TaskID &&
			(in.Worker == "" || x.Worker == in.Worker) && strings.HasPrefix(x.Reply, "sha256:") {
			// 加入的讨论：回复在 outbox 里等房主，执行记的是内容摘要
			if x.Reply == replyDigest(in.Body, outcome) {
				return ErrAlreadyReported
			}
			return fmt.Errorf("%w: different reply for a reported execution", ErrConflict)
		}
		if errors.Is(err, ErrStaleRound) && (x.State == ExecDone || x.State == ExecFailed) && x.TaskID == in.TaskID &&
			(in.Worker == "" || x.Worker == in.Worker) && x.Reply != "" {
			// 这一轮已经汇报过：内容一样是重跑（响应丢了），当作成功；不一样拒绝，不动任务（可能已在做下一轮）
			old, err := scanDiscMessage(tx.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE id=?`, x.Reply))
			if err != nil {
				return err
			}
			if old.Body == in.Body && old.Outcome == outcome && old.Report == in.Report {
				reply = old
				return ErrAlreadyReported
			}
			return fmt.Errorf("%w: different reply for a reported execution", ErrConflict)
		}
		if err != nil {
			return err
		}
		if x.Acked < x.ContextLen {
			return fmt.Errorf("%w: read %d of %d bytes", ErrNotAcked, x.Acked, x.ContextLen)
		}
		status := TaskDone
		if in.Failed {
			status = TaskFailed
		}
		res, err := tx.Exec(`UPDATE tasks SET status=?, summary=?, report=?, ended_at=? WHERE id=? AND status='running'`, status, in.Summary, in.Report, ms(in.At), in.TaskID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrStaleRound
		}
		if err := addMessage(tx, in.TaskMsg); err != nil {
			return err
		}
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, x.Member))
		if err != nil {
			return err
		}
		rev, err := bump(tx, x.Discussion)
		if err != nil {
			return err
		}
		if mirror, err := isMirror(tx, x.Discussion); err != nil {
			return err
		} else if mirror {
			// 加入的讨论：回复交给房主分配序号、写进讨论，本机只记执行结束和内容摘要（重跑时比对）
			state := ExecDone
			if in.Failed {
				state = ExecFailed
			}
			if _, err := tx.Exec(`UPDATE disc_executions SET state=?, reply=?, ended_at=?, rev=? WHERE id=?`, state, replyDigest(in.Body, status), ms(in.At), rev, x.ID); err != nil {
				return err
			}
			return addOutbox(tx, x.Discussion, x.ID, OwnerEvent{Kind: EventReply, Body: in.Body, Outcome: status, At: ms(in.At)})
		}
		reply = DiscMessage{ID: NewID("dm_") + NewID(""), Discussion: x.Discussion, Kind: DiscReply, AuthorMember: m.ID, AuthorName: m.Name, AuthorAgent: m.Agent,
			Body: in.Body, Execution: x.ID, Report: in.Report, Outcome: status, Rev: rev, CreatedAt: in.At}
		if err := insertDiscMessageSrc(tx, &reply, in.TaskMsg.ID); err != nil {
			return err
		}
		state := ExecDone
		if in.Failed {
			state = ExecFailed
		}
		_, err = tx.Exec(`UPDATE disc_executions SET state=?, reply=?, ended_at=?, rev=? WHERE id=?`, state, reply.ID, ms(in.At), rev, x.ID)
		return err
	})
	return reply, err
}

// AskInput 是成员在这一轮里提问。
type AskInput struct {
	RoundCheck
	TaskMsg Message  // 任务的 question 消息
	Choices []string // 可选答案（已规整）
	At      time.Time
}

// AskExecution 记下问题：任务 question 消息和讨论问题在同一事务里，轮次转 waiting。
func (s *Store) AskExecution(in AskInput) (DiscMessage, error) {
	var q DiscMessage
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := checkRound(tx, in.RoundCheck, ExecRunning, ExecWaiting)
		if err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(`SELECT status FROM tasks WHERE id=?`, in.TaskID).Scan(&status); err != nil || status != TaskRunning {
			return ErrStaleRound
		}
		if err := addMessage(tx, in.TaskMsg); err != nil {
			return err
		}
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, x.Member))
		if err != nil {
			return err
		}
		rev, err := bump(tx, x.Discussion)
		if err != nil {
			return err
		}
		if mirror, err := isMirror(tx, x.Discussion); err != nil {
			return err
		} else if mirror {
			if _, err := tx.Exec(`UPDATE disc_executions SET state='waiting', rev=? WHERE id=?`, rev, x.ID); err != nil {
				return err
			}
			ev := OwnerEvent{Kind: EventQuestion, Question: in.TaskMsg.ID, Body: in.TaskMsg.Body, Choices: in.Choices, At: ms(in.At)}
			if len(in.Choices) > 0 && !hostHas(tx, x.Discussion, FeatureChoices) {
				// 旧版房主不认 choices（事件摘要会对不上）：选项写进正文，照常能答
				ev.Choices, ev.Body = nil, in.TaskMsg.Body+"\n\n"+ChoiceList(in.Choices)
			}
			return addOutbox(tx, x.Discussion, x.ID, ev)
		}
		q = DiscMessage{ID: NewID("dm_") + NewID(""), Discussion: x.Discussion, Kind: DiscQuestion, AuthorMember: m.ID, AuthorName: m.Name, AuthorAgent: m.Agent,
			Body: in.TaskMsg.Body, Execution: x.ID, Question: in.TaskMsg.ID, Choices: in.Choices, Rev: rev, CreatedAt: in.At}
		if err := insertDiscMessageSrc(tx, &q, in.TaskMsg.ID); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE disc_executions SET state='waiting', rev=? WHERE id=?`, rev, x.ID)
		return err
	})
	return q, err
}

// QuestionByTaskMessage 是任务问题投影到讨论里的那条消息。
func (s *Store) QuestionByTaskMessage(taskMsgID string) (DiscMessage, error) {
	return scanDiscMessage(s.db.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE question=? AND kind='question'`, taskMsgID))
}

// AnswerInput 是用户回答一个讨论问题（群里或任务页）。
type AnswerInput struct {
	Question string // 任务的 question 消息 ID
	Body     string
	ClientID string
	TaskMsg  Message // 任务的 answer 消息
	At       time.Time
}

// AnswerQuestion 回答问题：任务 answer 消息和讨论回答在同一事务里，唯一约束保证一个问题只接受一次回答，
// 群里和任务页同时回答只有一边成功（另一边 ErrConflict）。普通群消息不会被当作答案。
func (s *Store) AnswerQuestion(in AnswerInput) (DiscMessage, error) {
	var a DiscMessage
	err := s.withTx(func(tx *sql.Tx) error {
		if handled, err := answerMirror(tx, in); handled || err != nil {
			return err
		}
		q, err := scanDiscMessage(tx.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE question=? AND kind='question'`, in.Question))
		if err != nil {
			return err
		}
		if err := activeDiscussion(tx, q.Discussion); err != nil {
			return err
		}
		x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, q.Execution))
		if err != nil {
			return err
		}
		if x.State != ExecWaiting && x.State != ExecRunning {
			return fmt.Errorf("%w: execution is %s", ErrStaleRound, x.State)
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM messages WHERE reply_to=? AND kind='answer'`, in.Question).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: question already answered", ErrConflict)
		}
		rev, err := bump(tx, q.Discussion)
		if err != nil {
			return err
		}
		a = DiscMessage{ID: NewID("dm_") + NewID(""), Discussion: q.Discussion, Kind: DiscAnswer, Body: in.Body, Quote: q.ID, Execution: x.ID,
			Question: in.Question, ClientID: in.ClientID, Rev: rev, CreatedAt: in.At}
		if err := insertDiscMessage(tx, &a); err != nil {
			return fmt.Errorf("%w: question already answered", ErrConflict)
		}
		if err := addMessage(tx, in.TaskMsg); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE messages SET read_at=? WHERE id=? AND read_at IS NULL`, ms(in.At), in.Question); err != nil {
			return err
		}
		// 还有别的没回答的问题时仍是 waiting
		var open int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM disc_messages q WHERE q.execution=? AND q.kind='question'
			AND NOT EXISTS (SELECT 1 FROM disc_messages a WHERE a.discussion=q.discussion AND a.question=q.question AND a.kind='answer')`, x.ID).Scan(&open); err != nil {
			return err
		}
		if open == 0 {
			_, err = tx.Exec(`UPDATE disc_executions SET state='running', rev=? WHERE id=? AND state='waiting'`, rev, x.ID)
		}
		return err
	})
	return a, err
}

// --- 终结

// EndTask 结束一个还在运行的任务（worker 退出、用户关闭、常驻进程重启后找不到 worker）：
// 同一事务里改任务状态、写任务消息（exit 等，msg 为 nil 不写），并把这个任务上进行中的轮次标成 execState。
// interrupt 为 true 时暂停成员队列（后续请求等用户「重新启动并继续」），否则（用户停止）解开绑定，下一轮新开 worker。
// 返回任务是否真的从 running 改了。
func (s *Store) EndTask(taskID, status string, msg *Message, execState, reason string, interrupt bool, at time.Time) (bool, error) {
	changed := false
	err := s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE tasks SET status=?, summary='', report='', ended_at=? WHERE id=? AND status='running'`, status, ms(at), taskID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		changed = n > 0
		if changed && msg != nil {
			if err := addMessage(tx, *msg); err != nil {
				return err
			}
		}
		return endRounds(tx, taskID, execState, reason, interrupt, at)
	})
	return changed, err
}

// endRounds 把任务上进行中的轮次结束掉，并按 interrupt 暂停或解绑成员。
func endRounds(tx querier, taskID, execState, reason string, interrupt bool, at time.Time) error {
	// 别人电脑上的执行、还没拉起 worker 的执行在本机没有任务：空任务 ID 不能拿来批量匹配
	if taskID == "" {
		return nil
	}
	xs, err := collect(tx, scanExecution, `SELECT `+execCols+` FROM disc_executions WHERE task_id=? AND state IN `+inList(activeStates), taskID)
	if err != nil {
		return err
	}
	for _, x := range xs {
		if _, err := setExec(tx, x.Discussion, x.ID, inList(activeStates), "state=?, reason=?, ended_at=?", execState, reason, ms(at)); err != nil {
			return err
		}
		if interrupt {
			_, err = tx.Exec(`UPDATE disc_members SET paused=?, rev=(SELECT rev FROM discussions WHERE key=?) WHERE id=?`, reason, x.Discussion, x.Member)
		} else {
			_, err = tx.Exec(`UPDATE disc_members SET task_id='', worker='', worker_config=0, rev=(SELECT rev FROM discussions WHERE key=?) WHERE id=? AND task_id=?`, x.Discussion, x.Member, taskID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// UnbindTask 解开绑定着这个任务的成员（用户关了 worker）：下一轮新开 worker，不算受阻。
func (s *Store) UnbindTask(taskID string) error {
	return s.withTx(func(tx *sql.Tx) error {
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE task_id=? AND task_id!=''`, taskID))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		rev, err := bump(tx, m.Discussion)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE disc_members SET task_id='', worker='', worker_config=0, rev=? WHERE id=?`, rev, m.ID)
		return err
	})
}

// ReconcileResult 是启动对账的结果。
type ReconcileResult struct {
	Interrupted int
	Failed      int
	Paused      int
}

// ReconcileExecutions 在常驻进程启动（交接过来的在拿到锁之后）时对账：
//   - starting 而任务没登记或 worker 不在：启动失败，不自动重试；任务已登记、worker 交接过来了：转 running
//   - running、waiting 而任务不在运行（重启时已记成 lost）：中断
//   - restarted 为 true（普通重启，不是交接）时暂停还有排队请求的成员，等用户确认
//
// alive 判断 worker 终端是否还在这个进程里。
func (s *Store) ReconcileExecutions(alive func(worker string) bool, restarted bool, at time.Time) (ReconcileResult, error) {
	var r ReconcileResult
	self := s.Self()
	err := s.withTx(func(tx *sql.Tx) error {
		// 只对本机的执行对账：别人电脑上的轮次（房主那边的成员执行、成员缓存里房主的执行）由它的拥有者负责
		xs, err := collect(tx, scanExecution, `SELECT `+execCols+` FROM disc_executions WHERE state IN `+inList(activeStates)+` AND owner IN ('', ?)`, self)
		if err != nil {
			return err
		}
		for _, x := range xs {
			var status, worker string
			terr := tx.QueryRow(`SELECT status, worker FROM tasks WHERE id=?`, x.TaskID).Scan(&status, &worker)
			switch {
			case x.State == ExecStarting && terr == nil && status == TaskRunning && alive(worker):
				if _, err := setExec(tx, x.Discussion, x.ID, "('starting')", "state='running', worker=?", worker); err != nil {
					return err
				}
				if _, err := tx.Exec(`UPDATE disc_members SET task_id=?, worker=?, worker_config=?, rev=(SELECT rev FROM discussions WHERE key=?) WHERE id=?`, x.TaskID, worker, x.ConfigVersion, x.Discussion, x.Member); err != nil {
					return err
				}
			case x.State == ExecStarting:
				if _, err := setExec(tx, x.Discussion, x.ID, "('starting')", "state='failed', reason=?, ended_at=?", "worker start was interrupted", ms(at)); err != nil {
					return err
				}
				if _, err := tx.Exec(`UPDATE disc_members SET paused=?, rev=(SELECT rev FROM discussions WHERE key=?) WHERE id=?`, "worker start was interrupted", x.Discussion, x.Member); err != nil {
					return err
				}
				// 拉起了一半的专用终端没人认领：任务记 lost，留给用户清理
				if terr == nil && status == TaskRunning {
					tx.Exec(`UPDATE tasks SET status='lost', ended_at=? WHERE id=?`, ms(at), x.TaskID)
				}
				r.Failed++
			case terr == nil && status == TaskRunning && alive(worker):
			default:
				if terr == nil && status == TaskRunning {
					if _, err := tx.Exec(`UPDATE tasks SET status='lost', ended_at=? WHERE id=?`, ms(at), x.TaskID); err != nil {
						return err
					}
				}
				if err := endRounds(tx, x.TaskID, ExecInterrupted, "daemon restarted", true, at); err != nil {
					return err
				}
				r.Interrupted++
			}
		}
		if !restarted {
			return nil
		}
		members, err := collect(tx, scanMember, `SELECT `+memberCols+` FROM disc_members m WHERE removed_at IS NULL AND paused='' AND owner IN ('', ?)
			AND EXISTS (SELECT 1 FROM disc_executions x WHERE x.member=m.id AND x.state='queued')`, self)
		if err != nil {
			return err
		}
		for _, m := range members {
			rev, err := bump(tx, m.Discussion)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE disc_members SET paused='daemon restarted', rev=? WHERE id=?`, rev, m.ID); err != nil {
				return err
			}
			r.Paused++
		}
		return nil
	})
	return r, err
}

// --- 附件清理

// ArchivedAttachments 是已归档讨论的附件（相对项目目录的路径），清理前列给用户看。
func (s *Store) ArchivedAttachments(key string) ([]Attachment, error) {
	d, err := s.Discussion(key)
	if err != nil {
		return nil, err
	}
	if d.Status != DiscussionArchived {
		return nil, fmt.Errorf("%w: only archived discussions", ErrConflict)
	}
	msgs, err := collect(s.db, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND attachments!='' ORDER BY seq`, key)
	if err != nil {
		return nil, err
	}
	var out []Attachment
	for _, m := range msgs {
		out = append(out, m.Attachments...)
	}
	return out, nil
}

// AttachmentDir 是讨论附件在项目里的目录（相对项目目录）。
func AttachmentDir(localID string) string {
	return filepath.Join(".posse", "discussions", localID, "attachments")
}
