package store

// 局域网协作（第二阶段，docs/features/discussions.md 的「局域网多人」）：房主的讨论是权威记录，
// 加入者在自己库里留一份缓存（discussions.mirror=1），只有自己 Agent 的执行进度和私有配置在本机写。
//
// 房主一侧：collab_people（谁加入了哪个房间、第几代成员资格）、collab_invites（单次邀请，只存凭据哈希）、
// collab_requests（加入申请）、collab_events（成员电脑交来的执行事件，按执行和序号去重）、collab_uploads（分块上传的附件）。
// 加入者一侧：collab_joined（加入状态和同步游标）、collab_intents（本机发出的 @ 自己 Agent 的授权意图）、
// collab_accepts（本机接受账本：领取过的执行）、collab_outbox（待交给房主的执行事件，至少一次补发）。
// 所有校验都在事务里：撤销、代际、归属和写入一起提交。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const collabSchema = `
CREATE TABLE IF NOT EXISTS collab_people (
	room TEXT NOT NULL,
	fp TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	generation INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL,
	host INTEGER NOT NULL DEFAULT 0,
	rev INTEGER NOT NULL DEFAULT 0,
	joined_at INTEGER NOT NULL DEFAULT 0,
	ended_at INTEGER,
	PRIMARY KEY(room, fp)
);
CREATE TABLE IF NOT EXISTS collab_invites (
	id TEXT PRIMARY KEY,
	room TEXT NOT NULL,
	secret_hash TEXT NOT NULL UNIQUE,
	expires_at INTEGER NOT NULL,
	request TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS collab_requests (
	id TEXT PRIMARY KEY,
	room TEXT NOT NULL,
	fp TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	invite TEXT NOT NULL UNIQUE,
	addr TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	generation INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	decided_at INTEGER
);
CREATE TABLE IF NOT EXISTS collab_events (
	exec TEXT NOT NULL,
	seq INTEGER NOT NULL,
	room TEXT NOT NULL,
	digest TEXT NOT NULL,
	PRIMARY KEY(exec, seq)
);
CREATE TABLE IF NOT EXISTS collab_uploads (
	id TEXT PRIMARY KEY,
	room TEXT NOT NULL,
	fp TEXT NOT NULL,
	name TEXT NOT NULL,
	size INTEGER NOT NULL,
	sha TEXT NOT NULL,
	path TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	used INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS collab_uploads_sha ON collab_uploads(room, sha);
CREATE TABLE IF NOT EXISTS collab_joined (
	room TEXT PRIMARY KEY,
	host TEXT NOT NULL,
	host_name TEXT NOT NULL DEFAULT '',
	addrs TEXT NOT NULL DEFAULT '',
	request TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	generation INTEGER NOT NULL DEFAULT 0,
	cursor INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	secret TEXT NOT NULL DEFAULT '',
	att_seq INTEGER NOT NULL DEFAULT 0,
	joined_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS collab_intents (
	room TEXT NOT NULL,
	client_id TEXT NOT NULL,
	member TEXT NOT NULL,
	config_version INTEGER NOT NULL DEFAULT 0,
	digest TEXT NOT NULL,
	config TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	PRIMARY KEY(room, client_id)
);
CREATE TABLE IF NOT EXISTS collab_accepts (
	exec TEXT PRIMARY KEY,
	room TEXT NOT NULL,
	generation INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL,
	reason TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS collab_outbox (
	exec TEXT NOT NULL,
	seq INTEGER NOT NULL,
	room TEXT NOT NULL,
	kind TEXT NOT NULL,
	payload TEXT NOT NULL,
	digest TEXT NOT NULL,
	state TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	PRIMARY KEY(exec, seq)
);
CREATE INDEX IF NOT EXISTS collab_outbox_room ON collab_outbox(room, state);
-- host: one agent per request ID of one person in one discussion (AddMemberOnce)
CREATE UNIQUE INDEX IF NOT EXISTS disc_members_client ON disc_members(discussion, owner, client_id) WHERE client_id <> '';
-- one discussion per create request ID (CreateDiscussion); local only, not synced or shown
CREATE UNIQUE INDEX IF NOT EXISTS discussions_client ON discussions(client_id) WHERE client_id <> '';
-- member: the worktree an add request created, reused when the same request is retried
CREATE TABLE IF NOT EXISTS collab_add_workspaces (
	room TEXT NOT NULL,
	client TEXT NOT NULL,
	workspace_id TEXT NOT NULL,
	worktree TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY(room, client)
);
`

// 成员资格的状态
const (
	PersonActive  = "active"
	PersonRemoved = "removed" // 房主移除
	PersonLeft    = "left"    // 本人退出
)

// 加入申请的状态（房主）和加入状态（加入者）
const (
	RequestPending  = "pending"
	RequestApproved = "approved"
	RequestRejected = "rejected"
	JoinedActive    = "active"
)

// 接受账本和 outbox 的状态
const (
	AcceptAccepted = "accepted" // 领取过，本机队列负责执行
	AcceptFetching = "fetching" // 领取过，正在下载请求里的附件；下完转 accepted 才进本机队列
	AcceptRefused  = "refused"  // 本机没有对应的授权意图：不执行
	AcceptRevoked  = "revoked"  // 访问被撤销：本机结果只能本地查看
	OutboxPending  = "pending"
	OutboxSent     = "sent"
	OutboxRejected = "rejected"
)

// DefaultCollabQuota 是房主接收成员上传附件的总配额（所有共享讨论合计）。
const DefaultCollabQuota = 1 << 30

// Person 是一个加入了讨论的人（房主自己也有一行，host 为真）。
type Person struct {
	Room       string     `json:"room"`
	FP         string     `json:"fp"`
	Name       string     `json:"name"`
	Generation int64      `json:"generation"`
	State      string     `json:"state"`
	Host       bool       `json:"host,omitempty"`
	Rev        int64      `json:"rev"`
	JoinedAt   time.Time  `json:"joinedAt"`
	EndedAt    *time.Time `json:"endedAt,omitempty"`
	// Avatar 是头像的 SHA-256（空是没有），ProfileRev 是这个人自己的资料版本（旧版不带，记 0）
	Avatar     string `json:"avatar,omitempty"`
	ProfileRev int64  `json:"profileRev,omitempty"`
	// Role 为 RoleGuest 时这个人只读：能看消息、下载附件，不能发言、标关键、带 Agent。空是普通成员
	Role string `json:"role,omitempty"`
}

// RoleGuest 是只读访客（邀请时定，换成普通成员要新邀请）。
const RoleGuest = "guest"

// ErrGuest 是只读访客做了写操作。
var ErrGuest = errors.New("you joined this discussion as a viewer and cannot post, mark messages or add agents; ask the host for a full invitation")

const personCols = `room,fp,name,generation,state,host,rev,joined_at,ended_at,avatar,profile_rev,role`

func scanPerson(row interface{ Scan(...any) error }) (Person, error) {
	var p Person
	var joined int64
	var ended sql.NullInt64
	err := row.Scan(&p.Room, &p.FP, &p.Name, &p.Generation, &p.State, &p.Host, &p.Rev, &joined, &ended, &p.Avatar, &p.ProfileRev, &p.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.JoinedAt = fromMS(joined)
	p.EndedAt = optTime(ended)
	return p, err
}

// JoinRequest 是一份加入申请。
type JoinRequest struct {
	ID         string     `json:"id"`
	Room       string     `json:"room"`
	FP         string     `json:"fp"`
	Name       string     `json:"name"`
	Invite     string     `json:"invite"`
	Addr       string     `json:"addr,omitempty"`
	State      string     `json:"state"`
	Generation int64      `json:"generation,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	DecidedAt  *time.Time `json:"decidedAt,omitempty"`
	Role       string     `json:"role,omitempty"` // 邀请给的身份：RoleGuest 是只读访客
}

const requestCols = `id,room,fp,name,invite,addr,state,generation,created_at,decided_at,role`

func scanRequest(row interface{ Scan(...any) error }) (JoinRequest, error) {
	var r JoinRequest
	var created int64
	var decided sql.NullInt64
	err := row.Scan(&r.ID, &r.Room, &r.FP, &r.Name, &r.Invite, &r.Addr, &r.State, &r.Generation, &created, &decided, &r.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.CreatedAt = fromMS(created)
	r.DecidedAt = optTime(decided)
	return r, err
}

// Joined 是加入者一侧的一个房间。
type Joined struct {
	Room       string    `json:"room"`
	Host       string    `json:"host"`
	HostName   string    `json:"hostName"`
	Addrs      []string  `json:"addrs"`
	Request    string    `json:"request"`
	State      string    `json:"state"` // pending, active, rejected, removed, left
	Generation int64     `json:"generation"`
	Cursor     int64     `json:"cursor"`
	Error      string    `json:"error,omitempty"`
	Secret     string    `json:"-"` // 申请还没有结果时留着邀请凭据，重交同一份申请；有结果就清掉
	JoinedAt   time.Time `json:"joinedAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

const joinedCols = `room,host,host_name,addrs,request,state,generation,cursor,error,secret,joined_at,updated_at`

func scanJoined(row interface{ Scan(...any) error }) (Joined, error) {
	var j Joined
	var addrs string
	var joined, updated int64
	err := row.Scan(&j.Room, &j.Host, &j.HostName, &addrs, &j.Request, &j.State, &j.Generation, &j.Cursor, &j.Error, &j.Secret, &joined, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	json.Unmarshal([]byte(addrs), &j.Addrs)
	j.JoinedAt, j.UpdatedAt = fromMS(joined), fromMS(updated)
	return j, err
}

// OwnerEvent 是成员电脑上自己 Agent 的执行事件，按 (execution, seq) 至少一次交给房主。
type OwnerEvent struct {
	Kind     string `json:"kind"`
	State    string `json:"state,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Question string `json:"question,omitempty"` // 拥有者本机的任务问题 ID
	Body     string `json:"body,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	At       int64  `json:"at,omitempty"`
	// Choices 是问题的可选答案。房主有 FeatureChoices 才带：旧版房主解不出这个字段，摘要会对不上
	Choices []string `json:"choices,omitempty"`
}

// 房主在 hello 里报的能力：加入者按它决定能不能用新加的事件字段和方法，没报的按旧版对待。
const (
	FeatureChoices = "choices"
	FeatureSummary = "summary"
	// FeatureDelegate：post 认 from（成员 @ 成员）。旧版房主会把它当成这个人自己发的普通请求
	FeatureDelegate = "delegate"
	// FeatureGuest：房主能发只读访客的邀请，并挡住访客的写操作
	FeatureGuest = "guest"
)

// HostFeatures 是这一版房主支持的能力。
var HostFeatures = []string{FeatureChoices, FeatureSummary, FeatureDelegate, FeatureGuest}

// hostHas 报告加入的讨论的房主（上次连上时）有没有这项能力。
func hostHas(tx querier, room, feature string) bool {
	var fs string
	tx.QueryRow(`SELECT features FROM collab_joined WHERE room=?`, room).Scan(&fs)
	return slices.Contains(strings.Split(fs, ","), feature)
}

// HostHas 报告加入的讨论的房主（上次连上时）有没有这项能力。
func (s *Store) HostHas(room, feature string) bool { return hostHas(s.db, room, feature) }

// SetHostFeatures 记下房主这次 hello 报的能力。
func (s *Store) SetHostFeatures(room string, features []string) error {
	_, err := s.db.Exec(`UPDATE collab_joined SET features=? WHERE room=?`, strings.Join(features, ","), room)
	return err
}

// MaxChoices、MaxChoiceLen 是一个问题最多带的选项数和每个选项的长度（字符）。
const (
	MaxChoices   = 6
	MaxChoiceLen = 80
)

// NormalizeChoices 去掉空的、重复的选项；太多或太长时报错。
func NormalizeChoices(in []string) ([]string, error) {
	var out []string
	for _, c := range in {
		c = strings.Join(strings.Fields(c), " ")
		if c == "" || slices.Contains(out, c) {
			continue
		}
		if utf8.RuneCountInString(c) > MaxChoiceLen {
			return nil, fmt.Errorf("a choice is longer than %d characters; keep choices short and put details in the question", MaxChoiceLen)
		}
		out = append(out, c)
	}
	if len(out) > MaxChoices {
		return nil, fmt.Errorf("at most %d choices per question", MaxChoices)
	}
	return out, nil
}

// ChoiceList 把选项写成正文里的一段（没有按钮的地方用）。
func ChoiceList(choices []string) string {
	var b strings.Builder
	b.WriteString("Options:")
	for i, c := range choices {
		fmt.Fprintf(&b, "\n%d. %s", i+1, c)
	}
	return b.String()
}

// 执行事件的种类
const (
	EventState    = "state"
	EventQuestion = "question"
	EventAnswer   = "answer"
	EventReply    = "reply"
	// EventSummary 是成员的 Agent 写的讨论摘要（房主有 FeatureSummary 才发）
	EventSummary = "summary"
)

// OutboxItem 是一条待交给房主的事件。
type OutboxItem struct {
	Exec    string     `json:"exec"`
	Seq     int64      `json:"seq"`
	Room    string     `json:"room"`
	Event   OwnerEvent `json:"event"`
	Payload string     `json:"-"`
	Digest  string     `json:"digest"`
	State   string     `json:"state"`
	Error   string     `json:"error,omitempty"`
}

// --- 小工具

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func digest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func replyDigest(body, outcome string) string { return "sha256:" + digest(body, outcome) }

// IntentDigest 是授权意图核对的请求摘要：正文、引用、附件和目标成员。
func IntentDigest(body, quote, target string, atts []Attachment) string {
	ids := make([]string, len(atts))
	for i, a := range atts {
		ids[i] = a.ID
	}
	return digest(body, quote, target, strings.Join(ids, ","))
}

// RoomDir 是加入者在 $POSSE_HOME/collab 下给这个房间的目录名（房间键的哈希，不直接拼远端输入）。
func RoomDir(room string) string {
	sum := sha256.Sum256([]byte(room))
	return hex.EncodeToString(sum[:16])
}

// MirrorAttachmentPath 是加入者缓存的附件相对 $POSSE_HOME 的路径。
func MirrorAttachmentPath(room, id, name string) string {
	return filepath.Join("collab", RoomDir(room), "attachments", SafeName(id), SafeName(name))
}

// SafeName 去掉路径分隔、控制字符和 ..，远端给的名字不能跑出目录。
func SafeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return "_"
	}
	if strings.HasPrefix(name, ".") {
		name = "_" + name[1:]
	}
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

func isMirror(tx querier, key string) (bool, error) {
	var m bool
	err := tx.QueryRow(`SELECT mirror FROM discussions WHERE key=?`, key).Scan(&m)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return m, err
}

// checkPerson 是房主对别人请求的逐请求复核：成员资格有效、代际和凭据的一致。
func checkPerson(tx querier, room, fp string, gen int64) error {
	p, err := scanPerson(tx.QueryRow(`SELECT `+personCols+` FROM collab_people WHERE room=? AND fp=?`, room, fp))
	if errors.Is(err, ErrNotFound) {
		return ErrRevoked
	}
	if err != nil {
		return err
	}
	if p.State == PersonActive && p.Generation == gen && p.Role == RoleGuest {
		return ErrGuest
	}
	if p.State != PersonActive || p.Generation != gen {
		return ErrRevoked
	}
	return nil
}

func personName(tx querier, d Discussion, fp string) (string, error) {
	p, err := scanPerson(tx.QueryRow(`SELECT `+personCols+` FROM collab_people WHERE room=? AND fp=?`, d.Key, fp))
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("%w: person %s", ErrNotFound, short(fp))
	}
	if err != nil {
		return "", err
	}
	if p.State != PersonActive {
		return "", fmt.Errorf("%w: %s is no longer in the discussion", ErrConflict, p.Name)
	}
	return p.Name, nil
}

func short(fp string) string {
	if len(fp) > 12 {
		return fp[:12]
	}
	return fp
}

// --- 加入者：outbox

// emitExecState 在加入的讨论里把自己执行的状态变化写进 outbox（和上一条状态事件一样就不写）。
func emitExecState(tx querier, key, id string) error {
	mirror, err := isMirror(tx, key)
	if err != nil || !mirror {
		return err
	}
	var state, reason string
	if err := tx.QueryRow(`SELECT state, reason FROM disc_executions WHERE id=?`, id).Scan(&state, &reason); err != nil {
		return err
	}
	return addOutbox(tx, key, id, OwnerEvent{Kind: EventState, State: state, Reason: reason, At: ms(time.Now())})
}

// addOutbox 追加一条执行事件；只有本机接受过的执行才发（撤销后不再补发）。
func addOutbox(tx querier, room, exec string, ev OwnerEvent) error {
	var st string
	err := tx.QueryRow(`SELECT state FROM collab_accepts WHERE exec=?`, exec).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) || err == nil && st != AcceptAccepted {
		return nil
	}
	if err != nil {
		return err
	}
	if ev.Kind == EventState {
		var last string
		if tx.QueryRow(`SELECT payload FROM collab_outbox WHERE exec=? AND kind='state' ORDER BY seq DESC LIMIT 1`, exec).Scan(&last) == nil {
			var old OwnerEvent
			json.Unmarshal([]byte(last), &old)
			if old.State == ev.State && old.Reason == ev.Reason {
				return nil
			}
		}
	}
	b, _ := json.Marshal(ev)
	var seq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM collab_outbox WHERE exec=?`, exec).Scan(&seq); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO collab_outbox(exec,seq,room,kind,payload,digest,state,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		exec, seq, room, ev.Kind, string(b), EventDigest(ev), OutboxPending, ms(time.Now()))
	return err
}

// EventDigest 是事件负载的规范摘要（房主按它判断同一编号的重发和冲突）。
func EventDigest(ev OwnerEvent) string {
	ev.At = 0
	b, _ := json.Marshal(ev)
	return digest(string(b))
}

// answerMirror 是加入的讨论里拥有者回答自己 Agent 的问题：记任务回答，事件交给房主写进讨论。
// 不是加入的讨论（找不到对应的本机任务问题，或执行不在加入的讨论里）时 handled 为 false。
func answerMirror(tx querier, in AnswerInput) (bool, error) {
	var taskID string
	if err := tx.QueryRow(`SELECT task_id FROM messages WHERE id=? AND kind='question'`, in.Question).Scan(&taskID); err != nil {
		return false, nil
	}
	x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE task_id=? ORDER BY created_at DESC LIMIT 1`, taskID))
	if err != nil {
		return false, nil
	}
	if mirror, err := isMirror(tx, x.Discussion); err != nil || !mirror {
		return false, err
	}
	if err := activeDiscussion(tx, x.Discussion); err != nil {
		return true, err
	}
	if x.State != ExecWaiting && x.State != ExecRunning {
		return true, fmt.Errorf("%w: execution is %s", ErrStaleRound, x.State)
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM messages WHERE reply_to=? AND kind='answer'`, in.Question).Scan(&n); err != nil {
		return true, err
	}
	if n > 0 {
		return true, fmt.Errorf("%w: question already answered", ErrConflict)
	}
	if err := addMessage(tx, in.TaskMsg); err != nil {
		return true, err
	}
	if _, err := tx.Exec(`UPDATE messages SET read_at=? WHERE id=? AND read_at IS NULL`, ms(in.At), in.Question); err != nil {
		return true, err
	}
	var open int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM messages q WHERE q.task_id=? AND q.kind='question'
		AND NOT EXISTS (SELECT 1 FROM messages a WHERE a.reply_to=q.id AND a.kind='answer')`, taskID).Scan(&open); err != nil {
		return true, err
	}
	rev, err := bump(tx, x.Discussion)
	if err != nil {
		return true, err
	}
	if open == 0 {
		if _, err := tx.Exec(`UPDATE disc_executions SET state='running', rev=? WHERE id=? AND state='waiting'`, rev, x.ID); err != nil {
			return true, err
		}
	}
	return true, addOutbox(tx, x.Discussion, x.ID, OwnerEvent{Kind: EventAnswer, Question: in.Question, Body: in.Body, At: ms(in.At)})
}

// --- 房主：共享、邀请、申请、成员资格

// SetShared 打开或关闭局域网共享。关闭不撤销任何成员，重新打开后仍有效的成员不用再批准。
// 打开时确保房主自己在成员表里（显示名可改）。
func (s *Store) SetShared(room string, on bool, host Profile, at time.Time) error {
	return s.withTx(func(tx *sql.Tx) error {
		d, err := scanDiscussion(tx.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, room))
		if err != nil {
			return err
		}
		if d.Mirror {
			return fmt.Errorf("%w: only the host can share a discussion", ErrNotOwner)
		}
		if on && d.Status != DiscussionActive {
			return ErrArchived
		}
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE discussions SET shared=? WHERE key=?`, on, room); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO collab_people(room,fp,name,generation,state,host,rev,joined_at,avatar,profile_rev) VALUES(?,?,?,0,'active',1,?,?,?,?)
			ON CONFLICT(room,fp) DO UPDATE SET name=excluded.name, rev=excluded.rev, avatar=excluded.avatar, profile_rev=excluded.profile_rev`,
			room, d.Host, host.Name, rev, ms(at), host.Avatar, host.Rev)
		return err
	})
}

// Profile 是一个人的名字和头像，Rev 是它的版本。
type Profile struct {
	Name   string `json:"name"`
	Avatar string `json:"avatar,omitempty"`
	Rev    int64  `json:"rev,omitempty"`
}

// SetOwnProfile 把本机身份的新资料写进所有讨论里自己那一行：房主的讨论按版本增量同步给成员；
// 加入的讨论先改本机这份（房主收下之前别人还看到旧的，界面看 ProfileSent 标出没同步）。
func (s *Store) SetOwnProfile(fp string, p Profile) error {
	return s.withTx(func(tx *sql.Tx) error {
		rows, err := collect(tx, scanPerson, `SELECT `+personCols+` FROM collab_people WHERE fp=? AND profile_rev<?`, fp, p.Rev)
		if err != nil {
			return err
		}
		for _, r := range rows {
			rev, err := bump(tx, r.Room)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE collab_people SET name=?, avatar=?, profile_rev=?, rev=? WHERE room=? AND fp=?`, p.Name, p.Avatar, p.Rev, rev, r.Room, fp); err != nil {
				return err
			}
		}
		return nil
	})
}

// ApplyProfile 是房主收下成员交来的资料：只改连接身份自己那一行、成员资格是当前这一代，
// 版本不比已有的新就不动（重发、乱序到达的旧版本不覆盖新的）。返回是否改了。
func (s *Store) ApplyProfile(room, fp string, gen int64, p Profile) (bool, error) {
	changed := false
	err := s.withTx(func(tx *sql.Tx) error {
		cur, err := scanPerson(tx.QueryRow(`SELECT `+personCols+` FROM collab_people WHERE room=? AND fp=?`, room, fp))
		if err != nil {
			return err
		}
		if cur.State != PersonActive || cur.Generation != gen {
			return ErrRevoked
		}
		if p.Rev <= cur.ProfileRev {
			return nil
		}
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE collab_people SET name=?, avatar=?, profile_rev=?, rev=? WHERE room=? AND fp=?`, p.Name, p.Avatar, p.Rev, rev, room, fp)
		changed = err == nil
		return err
	})
	return changed, err
}

// MemberProfileRev 是房主记着的某成员的资料版本；不是当前这一代的有效成员回 ErrRevoked。
// 房主收资料时先看它，旧版本不落盘。
func (s *Store) MemberProfileRev(room, fp string, gen int64) (int64, error) {
	cur, err := scanPerson(s.db.QueryRow(`SELECT `+personCols+` FROM collab_people WHERE room=? AND fp=?`, room, fp))
	if err != nil {
		return 0, err
	}
	if cur.State != PersonActive || cur.Generation != gen {
		return 0, ErrRevoked
	}
	return cur.ProfileRev, nil
}

// AvatarsInUse 是所有讨论里有人在用的头像摘要。
func (s *Store) AvatarsInUse() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT avatar FROM collab_people WHERE avatar!=''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// AvatarInRoom 看这份头像是不是讨论里某个人在用的：成员只能取同一讨论里的头像。
func (s *Store) AvatarInRoom(room, sha string) bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM collab_people WHERE room=? AND avatar=?`, room, sha).Scan(&n)
	return n > 0
}

// RoomAvatars 是讨论里各人在用的头像摘要。
func (s *Store) RoomAvatars(room string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT avatar FROM collab_people WHERE room=? AND avatar!=''`, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ProfileSent 是已经交给这个讨论房主的资料版本。
func (s *Store) ProfileSent(room string) int64 {
	var v int64
	s.db.QueryRow(`SELECT profile_sent FROM collab_joined WHERE room=?`, room).Scan(&v)
	return v
}

// SetProfileSent 记下房主已收下（或已有更新的）资料版本。
func (s *Store) SetProfileSent(room string, rev int64) error {
	_, err := s.db.Exec(`UPDATE collab_joined SET profile_sent=MAX(profile_sent, ?) WHERE room=?`, rev, room)
	return err
}

// SharedRooms 是开着共享的讨论（房主一侧）。
func (s *Store) SharedRooms() ([]Discussion, error) {
	return collect(s.db, scanDiscussion, `SELECT `+discCols+` FROM discussions WHERE shared=1 AND mirror=0 ORDER BY created_at`)
}

// PruneInvites 删掉 before 之前就过期、一直没被用过的邀请，返回删了几条。用过的被 collab_requests 引用，不动。
func (s *Store) PruneInvites(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM collab_invites WHERE request='' AND expires_at < ?`, ms(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// collabRoomTables 是按房间存的协作表，讨论不在了这些行就没用了。collab_uploads 不在里面：
// 行里记着临时文件，由 StaleUploads、UnusedUploads 连文件一起收拾。
var collabRoomTables = []string{
	"collab_people", "collab_invites", "collab_requests", "collab_events", "collab_joined",
	"collab_intents", "collab_accepts", "collab_outbox", "collab_add_workspaces",
}

// collabCleanBatch 是清理时一条语句改的行数：每批自己一个事务，不长时间占着写锁。批与批之间歇
// collabCleanPause，让排着队的写入先走：交接时新旧两个常驻进程同时开着这个库，首尾相接的话另一个连接
// 抢不到写锁（实测 20 万行时被饿到 3 秒）。
const collabCleanBatch = 500

var collabCleanPause = 50 * time.Millisecond

// cleanPause 在两批之间歇一下；ctx 结束了返回它的错误，清理停下，下次接着清。
func cleanPause(ctx context.Context) error {
	t := time.NewTimer(collabCleanPause)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// PruneCollabOrphans 删掉讨论已经不在了的房间在各协作表里的行，返回删了几行。
// 讨论只有 DropMirror 会删（它同时删这些行），这里清它以前留下的。
func (s *Store) PruneCollabOrphans(ctx context.Context) (int64, error) {
	var total int64
	for _, t := range collabRoomTables {
		for {
			res, err := s.db.Exec(`DELETE FROM `+t+` WHERE rowid IN (SELECT rowid FROM `+t+` WHERE room NOT IN (SELECT key FROM discussions) LIMIT ?)`, collabCleanBatch)
			if err != nil {
				return total, err
			}
			n, _ := res.RowsAffected()
			total += n
			if n < collabCleanBatch {
				break
			}
			if err := cleanPause(ctx); err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

// endedExecStates 是执行的终态：不再有新的执行事件。
var endedExecStates = []string{ExecDone, ExecFailed, ExecInterrupted, ExecStopped, ExecCancelled, ExecMerged, ExecRevoked}

// ClearOutboxPayloads 清掉 outbox 里用不着了的事件内容（回复正文这些），行、序号、摘要和状态都留着，返回清了几行。
// 只清：已交出或被拒收的（pending 的要补发，读的是内容）、所属执行在 before 之前就结束了的。每个执行最近一条
// 状态事件的内容也留着：addOutbox 拿它判断新的状态事件是不是重复。下一条序号按 MAX(seq) 取，和内容无关。
func (s *Store) ClearOutboxPayloads(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	for {
		res, err := s.db.Exec(`UPDATE collab_outbox SET payload='' WHERE rowid IN (
			SELECT o.rowid FROM collab_outbox o JOIN disc_executions x ON x.id=o.exec
			WHERE o.state<>'pending' AND o.payload<>'' AND x.state IN `+inList(endedExecStates)+` AND x.ended_at < ?
				AND NOT (o.kind='state' AND o.seq=(SELECT MAX(seq) FROM collab_outbox l WHERE l.exec=o.exec AND l.kind='state'))
			LIMIT ?)`, ms(before), collabCleanBatch)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < collabCleanBatch {
			return total, nil
		}
		if err := cleanPause(ctx); err != nil {
			return total, err
		}
	}
}

// CreateInvite 记下一份邀请：只存凭据哈希，十分钟（按房主时钟）有效，只能用一次。
func (s *Store) CreateInvite(room, id, secretHash, role string, expires, at time.Time) error {
	return s.withTx(func(tx *sql.Tx) error {
		d, err := scanDiscussion(tx.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, room))
		if err != nil {
			return err
		}
		if d.Mirror {
			return fmt.Errorf("%w: only the host can invite", ErrNotOwner)
		}
		if !d.Shared {
			return fmt.Errorf("%w: turn on LAN sharing first", ErrConflict)
		}
		if d.Status != DiscussionActive {
			return ErrArchived
		}
		_, err = tx.Exec(`INSERT INTO collab_invites(id,room,secret_hash,expires_at,created_at,role) VALUES(?,?,?,?,?,?)`, id, room, secretHash, ms(expires), ms(at), role)
		return err
	})
}

// 加入申请的错误
var (
	ErrInviteInvalid = errors.New("invitation is not valid")
	ErrInviteExpired = errors.New("invitation has expired")
	ErrInviteUsed    = errors.New("invitation was already used")
)

// SubmitJoin 用邀请提交加入申请：同一申请 ID 重交返回原申请（不再消费邀请），邀请被别的申请用过时拒绝。
// 消费邀请是条件更新，并发的两份申请只有一份成功。
func (s *Store) SubmitJoin(room, secretHash, reqID, fp, name, addr string, now time.Time) (JoinRequest, error) {
	var out JoinRequest
	err := s.withTx(func(tx *sql.Tx) error {
		if r, err := scanRequest(tx.QueryRow(`SELECT `+requestCols+` FROM collab_requests WHERE id=?`, reqID)); err == nil {
			if r.FP != fp || r.Room != room {
				return ErrInviteInvalid
			}
			out = r
			return nil
		}
		var invID, invRoom, used string
		var expires int64
		var role string
		err := tx.QueryRow(`SELECT id, room, request, expires_at, role FROM collab_invites WHERE secret_hash=?`, secretHash).Scan(&invID, &invRoom, &used, &expires, &role)
		if errors.Is(err, sql.ErrNoRows) || err == nil && invRoom != room {
			return ErrInviteInvalid
		}
		if err != nil {
			return err
		}
		if used != "" {
			return ErrInviteUsed
		}
		if now.UnixMilli() > expires {
			return ErrInviteExpired
		}
		d, err := scanDiscussion(tx.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, room))
		if err != nil {
			return err
		}
		if !d.Shared || d.Status != DiscussionActive {
			return ErrInviteInvalid
		}
		if fp == d.Host {
			return fmt.Errorf("%w: this is the host's own installation", ErrConflict)
		}
		if p, err := scanPerson(tx.QueryRow(`SELECT `+personCols+` FROM collab_people WHERE room=? AND fp=?`, room, fp)); err == nil && p.State == PersonActive {
			return fmt.Errorf("%w: already a member", ErrConflict)
		}
		res, err := tx.Exec(`UPDATE collab_invites SET request=? WHERE id=? AND request=''`, reqID, invID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrInviteUsed
		}
		out = JoinRequest{ID: reqID, Room: room, FP: fp, Name: name, Invite: invID, Addr: addr, State: RequestPending, CreatedAt: now, Role: role}
		if _, err := tx.Exec(`INSERT INTO collab_requests(id,room,fp,name,invite,addr,state,created_at,role) VALUES(?,?,?,?,?,?,?,?,?)`,
			out.ID, room, fp, name, invID, addr, RequestPending, ms(now), role); err != nil {
			if isUnique(err) {
				return ErrInviteUsed
			}
			return err
		}
		_, err = bump(tx, room)
		return err
	})
	return out, err
}

func (s *Store) JoinRequest(id string) (JoinRequest, error) {
	return scanRequest(s.db.QueryRow(`SELECT `+requestCols+` FROM collab_requests WHERE id=?`, id))
}

// JoinRequests 是房间的申请（pending 为真时只要待处理的）。
func (s *Store) JoinRequests(room string, pending bool) ([]JoinRequest, error) {
	q := `SELECT ` + requestCols + ` FROM collab_requests WHERE room=?`
	if pending {
		q += ` AND state='pending'`
	}
	return collect(s.db, scanRequest, q+` ORDER BY created_at`, room)
}

// ApproveRequest 批准申请：同一事务里绑定申请时的指纹、成员代际加一、申请记成已批准。
// 已经处理过的申请返回原结果（重复批准不再加代）。
func (s *Store) ApproveRequest(id string, at time.Time) (JoinRequest, error) {
	var out JoinRequest
	err := s.withTx(func(tx *sql.Tx) error {
		r, err := scanRequest(tx.QueryRow(`SELECT `+requestCols+` FROM collab_requests WHERE id=?`, id))
		if err != nil {
			return err
		}
		if r.State != RequestPending {
			out = r
			if r.State == RequestRejected {
				return fmt.Errorf("%w: request was rejected", ErrConflict)
			}
			return nil
		}
		if err := activeDiscussion(tx, r.Room); err != nil {
			return err
		}
		var gen int64
		if err := tx.QueryRow(`SELECT COALESCE(MAX(generation),0)+1 FROM collab_people WHERE room=? AND fp=?`, r.Room, r.FP).Scan(&gen); err != nil {
			return err
		}
		rev, err := bump(tx, r.Room)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO collab_people(room,fp,name,generation,state,host,rev,joined_at,role) VALUES(?,?,?,?,'active',0,?,?,?)
			ON CONFLICT(room,fp) DO UPDATE SET name=excluded.name, generation=excluded.generation, state='active', rev=excluded.rev, joined_at=excluded.joined_at, ended_at=NULL,
				avatar='', profile_rev=0, role=excluded.role`,
			r.Room, r.FP, r.Name, gen, rev, ms(at), r.Role); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE collab_requests SET state='approved', generation=?, decided_at=? WHERE id=?`, gen, ms(at), id); err != nil {
			return err
		}
		r.State, r.Generation = RequestApproved, gen
		out = r
		return nil
	})
	return out, err
}

// RejectRequest 拒绝申请（邀请已经用掉了，对方要新邀请）。
func (s *Store) RejectRequest(id string, at time.Time) (JoinRequest, error) {
	var out JoinRequest
	err := s.withTx(func(tx *sql.Tx) error {
		r, err := scanRequest(tx.QueryRow(`SELECT `+requestCols+` FROM collab_requests WHERE id=?`, id))
		if err != nil {
			return err
		}
		out = r
		if r.State != RequestPending {
			return nil
		}
		if _, err := bump(tx, r.Room); err != nil {
			return err
		}
		out.State = RequestRejected
		_, err = tx.Exec(`UPDATE collab_requests SET state='rejected', decided_at=? WHERE id=?`, ms(at), id)
		return err
	})
	return out, err
}

func (s *Store) People(room string) ([]Person, error) {
	return collect(s.db, scanPerson, `SELECT `+personCols+` FROM collab_people WHERE room=? ORDER BY host DESC, joined_at`, room)
}

func (s *Store) Person(room, fp string) (Person, error) {
	return scanPerson(s.db.QueryRow(`SELECT `+personCols+` FROM collab_people WHERE room=? AND fp=?`, room, fp))
}

// RevokePerson 移除成员（state=removed）或本人退出（left）：同一事务里撤销资格、把他的 Agent 标成已离开、
// 没结束的执行标成访问已撤销（不代表他电脑上的进程停了）、待处理的申请拒绝。历史署名不变。
func (s *Store) RevokePerson(room, fp, state string, at time.Time) (Person, error) {
	var out Person
	err := s.withTx(func(tx *sql.Tx) error {
		p, err := scanPerson(tx.QueryRow(`SELECT `+personCols+` FROM collab_people WHERE room=? AND fp=?`, room, fp))
		if err != nil {
			return err
		}
		if p.Host {
			return fmt.Errorf("%w: the host cannot leave its own discussion", ErrConflict)
		}
		out = p
		if p.State != PersonActive {
			return nil
		}
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE collab_people SET state=?, ended_at=?, rev=? WHERE room=? AND fp=?`, state, ms(at), rev, room, fp); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE disc_members SET removed_at=?, rev=? WHERE discussion=? AND owner=? AND removed_at IS NULL`, ms(at), rev, room, fp); err != nil {
			return err
		}
		reason := "access revoked"
		if state == PersonLeft {
			reason = "owner left the discussion"
		}
		live := append(append([]string{}, pendingStates...), activeStates...)
		if _, err := tx.Exec(`UPDATE disc_executions SET state='revoked', reason=?, ended_at=?, rev=? WHERE discussion=? AND owner=? AND state IN `+inList(live),
			reason, ms(at), rev, room, fp); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE collab_requests SET state='rejected', decided_at=? WHERE room=? AND fp=? AND state='pending'`, ms(at), room, fp); err != nil {
			return err
		}
		out.State = state
		return nil
	})
	return out, err
}

// --- 房主：成员电脑交来的操作

// HostClaim 是拥有者领取一轮：queued → claimed，核对归属和代际；同一拥有者重复领取返回原结果。
func (s *Store) HostClaim(room, fp string, gen int64, execID string) (Execution, error) {
	var out Execution
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := ownerExec(tx, room, fp, gen, execID)
		if err != nil {
			return err
		}
		out = x
		switch x.State {
		case ExecClaimed:
			return nil
		case ExecQueued:
		default:
			return fmt.Errorf("%w: execution is %s", ErrConflict, x.State)
		}
		if err := activeDiscussion(tx, room); err != nil {
			return err
		}
		if _, err := setExec(tx, room, x.ID, "('queued')", "state='claimed'"); err != nil {
			return err
		}
		out.State = ExecClaimed
		return nil
	})
	return out, err
}

// ownerExec 取一轮执行并核对：调用方是有效成员、执行归他、代际一致。
func ownerExec(tx querier, room, fp string, gen int64, execID string) (Execution, error) {
	if err := checkPerson(tx, room, fp, gen); err != nil {
		return Execution{}, err
	}
	x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=? AND discussion=?`, execID, room))
	if err != nil {
		return x, err
	}
	if x.Owner != fp {
		return x, ErrNotOwner
	}
	if x.Generation != gen || x.State == ExecRevoked {
		return x, ErrRevoked
	}
	return x, nil
}

// OwnerCancel 是拥有者取消房主还没交给他的请求（queued）。领取过的由他本机取消，状态随事件交来。
func (s *Store) OwnerCancel(room, fp string, gen int64, execID string) (Execution, error) {
	var out Execution
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := ownerExec(tx, room, fp, gen, execID)
		if err != nil {
			return err
		}
		if x.State != ExecQueued {
			return fmt.Errorf("%w: execution is %s", ErrConflict, x.State)
		}
		if _, err := setExec(tx, room, x.ID, "('queued')", "state='cancelled', ended_at=?", ms(time.Now())); err != nil {
			return err
		}
		out, err = scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, x.ID))
		return err
	})
	return out, err
}

// OwnerRetry 是拥有者另发起一轮（记 retry_of）。abandon 为真时放弃一轮已领取、可能开始过的执行
// （本机账本丢了、核实过副作用）：旧轮次记中断但保留，迟到的结果仍补到它上面，不会完成新的一轮。
func (s *Store) OwnerRetry(room, fp string, gen int64, execID, newID string, abandon bool, at time.Time) (Execution, error) {
	var nx Execution
	err := s.withTx(func(tx *sql.Tx) error {
		x, err := ownerExec(tx, room, fp, gen, execID)
		if err != nil {
			return err
		}
		if err := activeDiscussion(tx, room); err != nil {
			return err
		}
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, x.Member))
		if err != nil {
			return err
		}
		if m.RemovedAt != nil {
			return ErrMemberGone
		}
		switch {
		case abandon && (x.State == ExecClaimed || ExecActive(x.State) || x.State == ExecHeld || x.State == ExecBlocked):
			if _, err := setExec(tx, room, x.ID, inList([]string{x.State}), "state='interrupted', reason=?, ended_at=?", abandonedReason, ms(at)); err != nil {
				return err
			}
		case x.State == ExecFailed || x.State == ExecInterrupted || x.State == ExecStopped || x.State == ExecCancelled:
		default:
			return fmt.Errorf("%w: execution is %s", ErrConflict, x.State)
		}
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		nx = Execution{ID: newID, Discussion: room, Member: m.ID, Message: x.Message, RetryOf: x.ID, State: ExecQueued,
			Agent: m.Agent, Model: m.Model, Effort: m.Effort, Role: m.Role, ConfigVersion: m.ConfigVersion, Rev: rev, CreatedAt: at,
			Owner: m.Owner, Generation: m.Generation, Root: x.Root, Depth: x.Depth}
		if err := insertExecution(tx, nx); errors.Is(err, ErrConflict) {
			return ErrRetried
		} else {
			return err
		}
	})
	return nx, err
}

const abandonedReason = "abandoned by the owner; a late result is still recorded here"

// checkAccess 核对访问设置里的人都是这个讨论里出现过的人（指纹），不收拼错、乱填的。
func checkAccess(tx querier, room, access string) error {
	if access == "" || access == AccessAll {
		return nil
	}
	if NormalizeAccess(access, "") != access {
		return fmt.Errorf("%w: access is not normalized", ErrConflict)
	}
	for _, fp := range strings.Split(access, ",") {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM collab_people WHERE room=? AND fp=?`, room, fp).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: person %s", ErrNotFound, short(fp))
		}
	}
	return nil
}

// OwnerMemberUpdate 是拥有者改自己 Agent 的公开配置（名字、职责、agent、模型、思考深度、访问）。
func (s *Store) OwnerMemberUpdate(room, fp string, gen int64, m Member) (Member, error) {
	var out Member
	err := s.withTx(func(tx *sql.Tx) error {
		if err := checkPerson(tx, room, fp, gen); err != nil {
			return err
		}
		old, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=? AND discussion=?`, m.ID, room))
		if err != nil {
			return err
		}
		if old.Owner != fp {
			return ErrNotOwner
		}
		if old.RemovedAt != nil {
			return ErrMemberGone
		}
		if err := activeDiscussion(tx, room); err != nil {
			return err
		}
		version := old.ConfigVersion
		if old.Agent != m.Agent || old.Model != m.Model || old.Effort != m.Effort || old.Role != m.Role {
			version++
		}
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		if err := checkAccess(tx, room, m.Access); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE disc_members SET name=?, role=?, agent=?, model=?, effort=?, config_version=?, access=?, rev=? WHERE id=?`,
			m.Name, m.Role, m.Agent, m.Model, m.Effort, version, m.Access, rev, m.ID); err != nil {
			return err
		}
		out, err = scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, m.ID))
		return err
	})
	return out, err
}

// OwnerMemberRemove 是拥有者移除自己的 Agent：有没结束的请求时拒绝。
func (s *Store) OwnerMemberRemove(room, fp string, gen int64, id string, at time.Time) error {
	return s.withTx(func(tx *sql.Tx) error {
		if err := checkPerson(tx, room, fp, gen); err != nil {
			return err
		}
		old, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=? AND discussion=?`, id, room))
		if err != nil {
			return err
		}
		if old.Owner != fp {
			return ErrNotOwner
		}
		if old.RemovedAt != nil {
			return nil
		}
		if busy, err := memberBusy(tx, id); err != nil {
			return err
		} else if busy {
			return ErrBusyMember
		}
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE disc_members SET removed_at=?, rev=? WHERE id=?`, ms(at), rev, id)
		return err
	})
}

// HostEvent 是房主收到的一条成员执行事件。
type HostEvent struct {
	Room   string
	FP     string
	Gen    int64
	Exec   string
	Seq    int64
	Digest string
	Event  OwnerEvent
}

// ApplyOwnerEvent 幂等地接收成员电脑交来的执行事件：同一 (执行, 序号) 摘要一样是重发，返回 nil；
// 摘要不一样是冲突。撤销、代际不符、归属不符是终态拒收，不能靠去重放进来。
func (s *Store) ApplyOwnerEvent(ev HostEvent) error {
	return s.withTx(func(tx *sql.Tx) error {
		x, err := ownerExec(tx, ev.Room, ev.FP, ev.Gen, ev.Exec)
		if err != nil {
			return err
		}
		var old string
		if err := tx.QueryRow(`SELECT digest FROM collab_events WHERE exec=? AND seq=?`, ev.Exec, ev.Seq).Scan(&old); err == nil {
			if old == ev.Digest {
				return nil
			}
			return fmt.Errorf("%w: event %d of %s has different content", ErrConflict, ev.Seq, ev.Exec)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if EventDigest(ev.Event) != ev.Digest {
			return fmt.Errorf("%w: event digest does not match its content", ErrConflict)
		}
		if _, err := tx.Exec(`INSERT INTO collab_events(exec,seq,room,digest) VALUES(?,?,?,?)`, ev.Exec, ev.Seq, ev.Room, ev.Digest); err != nil {
			return err
		}
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, x.Member))
		if err != nil {
			return err
		}
		ended := x.State == ExecDone || x.State == ExecFailed || x.State == ExecStopped || x.State == ExecCancelled ||
			x.State == ExecInterrupted
		abandoned := x.State == ExecInterrupted && x.Reason == abandonedReason
		at := ev.Event.At
		if at == 0 {
			at = ms(time.Now())
		}
		rev, err := bump(tx, ev.Room)
		if err != nil {
			return err
		}
		e := ev.Event
		switch e.Kind {
		case EventState:
			if ended {
				return nil // 结束之后的状态迟到了：记下事件，不改结果
			}
			switch e.State {
			case ExecQueued, ExecHeld, ExecBlocked, ExecStarting, ExecRunning, ExecWaiting:
				sets := `state=?, reason=?, rev=?`
				args := []any{e.State, e.Reason, rev}
				if (e.State == ExecStarting || e.State == ExecRunning) && x.StartedAt == nil {
					sets += `, started_at=?`
					args = append(args, ms(time.Now()))
				}
				_, err = tx.Exec(`UPDATE disc_executions SET `+sets+` WHERE id=?`, append(args, x.ID)...)
			case ExecFailed, ExecInterrupted, ExecStopped, ExecCancelled:
				_, err = tx.Exec(`UPDATE disc_executions SET state=?, reason=?, ended_at=?, rev=? WHERE id=?`, e.State, e.Reason, ms(time.Now()), rev, x.ID)
			default:
				return fmt.Errorf("%w: unexpected state %q", ErrConflict, e.State)
			}
			return err
		case EventQuestion:
			if e.Question == "" || strings.TrimSpace(e.Body) == "" {
				return fmt.Errorf("%w: empty question", ErrConflict)
			}
			choices, err := NormalizeChoices(e.Choices)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrConflict, err)
			}
			q := DiscMessage{ID: NewID("dm_") + NewID(""), Discussion: ev.Room, Kind: DiscQuestion, AuthorMember: m.ID, AuthorName: m.Name, AuthorAgent: m.Agent,
				Body: e.Body, Execution: x.ID, Question: e.Question, Choices: choices, Rev: rev, CreatedAt: fromMS(at)}
			if err := insertDiscMessage(tx, &q); err != nil {
				return err
			}
			if !ended {
				_, err = tx.Exec(`UPDATE disc_executions SET state='waiting', rev=? WHERE id=?`, rev, x.ID)
			}
			return err
		case EventAnswer:
			q, err := scanDiscMessage(tx.QueryRow(`SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND question=? AND kind='question'`, ev.Room, e.Question))
			if err != nil {
				return fmt.Errorf("%w: question %s", ErrNotFound, e.Question)
			}
			name, _ := personName(tx, Discussion{Key: ev.Room}, ev.FP)
			a := DiscMessage{ID: NewID("dm_") + NewID(""), Discussion: ev.Room, Kind: DiscAnswer, Body: e.Body, Quote: q.ID, Execution: x.ID,
				Question: e.Question, Rev: rev, CreatedAt: fromMS(at), AuthorID: ev.FP, AuthorName: name}
			if err := insertDiscMessage(tx, &a); err != nil {
				return fmt.Errorf("%w: question already answered", ErrConflict)
			}
			var open int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM disc_messages q WHERE q.execution=? AND q.kind='question'
				AND NOT EXISTS (SELECT 1 FROM disc_messages a WHERE a.discussion=q.discussion AND a.question=q.question AND a.kind='answer')`, x.ID).Scan(&open); err != nil {
				return err
			}
			if open == 0 && !ended {
				_, err = tx.Exec(`UPDATE disc_executions SET state='running', rev=? WHERE id=? AND state='waiting'`, rev, x.ID)
			}
			return err
		case EventSummary:
			if strings.TrimSpace(e.Body) == "" || len(e.Body) > MaxSummary {
				return fmt.Errorf("%w: summary is empty or too long", ErrConflict)
			}
			return writeSummary(tx, x, e.Body, fromMS(at))
		case EventReply:
			if x.Reply != "" {
				return fmt.Errorf("%w: execution %s already has a reply", ErrConflict, x.ID)
			}
			if ended && !abandoned {
				return fmt.Errorf("%w: execution is %s", ErrConflict, x.State)
			}
			if strings.TrimSpace(e.Body) == "" || len(e.Body) > 256<<10 {
				return fmt.Errorf("%w: reply body is empty or too long", ErrConflict)
			}
			outcome := TaskDone
			state := ExecDone
			if e.Outcome == TaskFailed {
				outcome, state = TaskFailed, ExecFailed
			}
			r := DiscMessage{ID: NewID("dm_") + NewID(""), Discussion: ev.Room, Kind: DiscReply, AuthorMember: m.ID, AuthorName: m.Name, AuthorAgent: m.Agent,
				Body: e.Body, Execution: x.ID, Outcome: outcome, Rev: rev, CreatedAt: fromMS(at)}
			if err := insertDiscMessage(tx, &r); err != nil {
				return err
			}
			// 放弃过的旧轮次：结果补在它自己身上（状态照实），不会完成替代它的那一轮
			_, err = tx.Exec(`UPDATE disc_executions SET state=?, reply=?, ended_at=?, rev=? WHERE id=?`, state, r.ID, ms(time.Now()), rev, x.ID)
			return err
		}
		return fmt.Errorf("%w: unknown event %q", ErrConflict, e.Kind)
	})
}

// --- 房主：附件上传

// CollabQuota 是房主接收成员附件的总配额（字节）。
func (s *Store) CollabQuota() int64 {
	var v int64
	if s.db.QueryRow(`SELECT v FROM disc_meta WHERE k='collab_quota'`).Scan(&v) != nil || v <= 0 {
		return DefaultCollabQuota
	}
	return v
}

func (s *Store) SetCollabQuota(v int64) error {
	_, err := s.db.Exec(`INSERT INTO disc_meta(k,v) VALUES('collab_quota',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, v)
	return err
}

// CollabUsage 是已经发布的成员附件占的空间。
func (s *Store) CollabUsage() int64 {
	var v int64
	s.db.QueryRow(`SELECT COALESCE(SUM(size),0) FROM collab_uploads WHERE state='published'`).Scan(&v)
	return v
}

// Upload 是一份成员上传的附件。
type Upload struct {
	ID    string
	Room  string
	FP    string
	Name  string
	Size  int64
	SHA   string
	Path  string // 相对项目目录
	State string
	Used  bool
}

func scanUpload(row interface{ Scan(...any) error }) (Upload, error) {
	var u Upload
	var created int64
	err := row.Scan(&u.ID, &u.Room, &u.FP, &u.Name, &u.Size, &u.SHA, &u.Path, &u.State, &u.Used, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

const uploadCols = `id,room,fp,name,size,sha,path,state,used,created_at`

// 上传的错误
var (
	ErrQuota      = errors.New("shared attachment quota is full")
	ErrUploadBusy = errors.New("too many uploads in progress")
)

// BeginUpload 登记一份上传：先查配额（已发布的加上这份）和这个人同时在传的数量，超了拒绝，不删旧内容腾地方。
func (s *Store) BeginUpload(u Upload, maxConcurrent int, at time.Time) error {
	return s.withTx(func(tx *sql.Tx) error {
		if err := activeDiscussion(tx, u.Room); err != nil {
			return err
		}
		var used, n int64
		// 在传的也算进配额（预留）：两个并发上传各自通过检查后合计不会超
		if err := tx.QueryRow(`SELECT COALESCE(SUM(size),0) FROM collab_uploads WHERE state='published' OR state='uploading' AND created_at>?`, ms(at.Add(-30*time.Minute))).Scan(&used); err != nil {
			return err
		}
		quota := int64(DefaultCollabQuota)
		var v int64
		if tx.QueryRow(`SELECT v FROM disc_meta WHERE k='collab_quota'`).Scan(&v) == nil && v > 0 {
			quota = v
		}
		if used+u.Size > quota {
			return ErrQuota
		}
		// 半小时还没传完的当作放弃，不占名额
		if err := tx.QueryRow(`SELECT COUNT(*) FROM collab_uploads WHERE fp=? AND state='uploading' AND created_at>?`, u.FP, ms(at.Add(-30*time.Minute))).Scan(&n); err != nil {
			return err
		}
		if n >= int64(maxConcurrent) {
			return ErrUploadBusy
		}
		_, err := tx.Exec(`INSERT INTO collab_uploads(`+uploadCols+`) VALUES(?,?,?,?,?,?,?,'uploading',0,?)`, u.ID, u.Room, u.FP, u.Name, u.Size, u.SHA, u.Path, ms(at))
		return err
	})
}

func (s *Store) Upload(id string) (Upload, error) {
	return scanUpload(s.db.QueryRow(`SELECT `+uploadCols+` FROM collab_uploads WHERE id=?`, id))
}

// FinishUpload 把校验过的上传记成已发布（文件已经原子地改名到位）。
func (s *Store) FinishUpload(id string) error {
	_, err := s.db.Exec(`UPDATE collab_uploads SET state='published' WHERE id=? AND state='uploading'`, id)
	return err
}

// AbortUpload 丢掉没传完的上传。
func (s *Store) AbortUpload(id string) error {
	_, err := s.db.Exec(`DELETE FROM collab_uploads WHERE id=? AND state='uploading'`, id)
	return err
}

// PublishedBySHA 是房间里内容相同的已发布附件（去重只复用内容，附件记录各自独立）。
func (s *Store) PublishedBySHA(room, sha string) (Upload, error) {
	return scanUpload(s.db.QueryRow(`SELECT `+uploadCols+` FROM collab_uploads WHERE room=? AND sha=? AND state='published' LIMIT 1`, room, sha))
}

// takeUploads 在发消息的事务里取走这个人已发布、还没用过的上传，变成消息的附件。
func takeUploads(tx querier, room, fp string, ids []string) ([]Attachment, error) {
	var out []Attachment
	for _, id := range ids {
		u, err := scanUpload(tx.QueryRow(`SELECT `+uploadCols+` FROM collab_uploads WHERE id=? AND room=? AND fp=?`, id, room, fp))
		if err != nil {
			return nil, fmt.Errorf("%w: upload %s", ErrNotFound, id)
		}
		if u.State != "published" || u.Used {
			return nil, fmt.Errorf("%w: upload %s is not ready or already used", ErrConflict, id)
		}
		if _, err := tx.Exec(`UPDATE collab_uploads SET used=1 WHERE id=?`, id); err != nil {
			return nil, err
		}
		out = append(out, Attachment{ID: u.ID, Name: u.Name, Path: u.Path, Size: u.Size, SHA: u.SHA, Type: mimeType(u.Name)})
	}
	return out, nil
}

// RoomAttachment 是房间里某条消息上的附件（按附件 ID 下载时用，不接受任意路径）。
func (s *Store) RoomAttachment(room, id string) (Attachment, error) {
	pat := `%"id":"` + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(id) + `"%`
	msgs, err := collect(s.db, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND attachments LIKE ? ESCAPE '\' LIMIT 5`, room, pat)
	if err != nil {
		return Attachment{}, err
	}
	for _, m := range msgs {
		for _, a := range m.Attachments {
			if a.ID == id {
				return a, nil
			}
		}
	}
	return Attachment{}, ErrNotFound
}

// --- 房主：增量同步

// SyncDiscussion 是讨论本身同步给成员的部分（不带项目路径、默认工作区这些本机信息）。
type SyncDiscussion struct {
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Status      string     `json:"status"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
	MsgSeq      int64      `json:"msgSeq"`
	Shared      bool       `json:"shared"`
	CreatedAt   time.Time  `json:"createdAt"`
	Summary     string     `json:"summary,omitempty"`
	SummarySeq  int64      `json:"summarySeq,omitempty"`
	SummaryBy   string     `json:"summaryBy,omitempty"`
	SummaryAt   *time.Time `json:"summaryAt,omitempty"`
}

// SyncBatch 是一次增量：游标 since 之后改过的记录。Rev 是应用完之后的游标；More 为真时还有后续。
type SyncBatch struct {
	Rev        int64          `json:"rev"`
	More       bool           `json:"more"`
	Discussion SyncDiscussion `json:"discussion"`
	People     []Person       `json:"people,omitempty"`
	Members    []Member       `json:"members,omitempty"`
	Messages   []DiscMessage  `json:"messages,omitempty"`
	Executions []Execution    `json:"executions,omitempty"`
}

// SyncSince 取给成员的增量：按版本取，一批最多约 limit 条消息（同一版本的不拆开）。
// 只带共享的内容：成员的工作区、终端、任务、上下文快照、房主本机路径都去掉；房主本机的用户写成房主的身份。
func (s *Store) SyncSince(room string, since int64, limit int) (SyncBatch, error) {
	var b SyncBatch
	err := s.withTx(func(tx *sql.Tx) error {
		d, err := scanDiscussion(tx.QueryRow(`SELECT `+discCols+` FROM discussions WHERE key=?`, room))
		if err != nil {
			return err
		}
		if since > d.Rev {
			return fmt.Errorf("%w: cursor %d is ahead of the host (%d)", ErrConflict, since, d.Rev)
		}
		var hostName string
		tx.QueryRow(`SELECT name FROM collab_people WHERE room=? AND fp=?`, room, d.Host).Scan(&hostName)
		b.Discussion = SyncDiscussion{Title: d.Title, Description: d.Description, Status: d.Status, ArchivedAt: d.ArchivedAt, MsgSeq: d.MsgSeq, Shared: d.Shared, CreatedAt: d.CreatedAt,
			Summary: d.Summary, SummarySeq: d.SummarySeq, SummaryBy: d.SummaryBy, SummaryAt: d.SummaryAt}
		upto := d.Rev
		var cut int64
		if limit > 0 && tx.QueryRow(`SELECT rev FROM disc_messages WHERE discussion=? AND rev>? ORDER BY rev LIMIT 1 OFFSET ?`, room, since, limit-1).Scan(&cut) == nil && cut < upto {
			upto = cut
		}
		// 截断点之后只有成员、执行、人的改动也算还有：游标停在截断点，房主不会为同一版本再推一次
		b.More = upto < d.Rev
		b.Rev = upto
		if b.People, err = collect(tx, scanPerson, `SELECT `+personCols+` FROM collab_people WHERE room=? AND rev>? AND rev<=?`, room, since, upto); err != nil {
			return err
		}
		if b.Members, err = collect(tx, scanMember, `SELECT `+memberCols+` FROM disc_members WHERE discussion=? AND rev>? AND rev<=? ORDER BY created_at, rowid`, room, since, upto); err != nil {
			return err
		}
		if b.Messages, err = collect(tx, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND rev>? AND rev<=? ORDER BY seq`, room, since, upto); err != nil {
			return err
		}
		if b.Executions, err = collect(tx, scanExecution, `SELECT `+execCols+` FROM disc_executions WHERE discussion=? AND rev>? AND rev<=? ORDER BY created_at, rowid`, room, since, upto); err != nil {
			return err
		}
		for i := range b.Members {
			m := &b.Members[i]
			m.Owner = cmpOr(m.Owner, d.Host)
			m.WorkspaceID, m.WorkspaceLabel, m.TaskID, m.Worker, m.WorkerConfig, m.Paused, m.RecvSeq = "", "", "", "", 0, "", 0
		}
		for i := range b.Messages {
			m := &b.Messages[i]
			if m.Kind == DiscUser || m.Kind == DiscAnswer || m.Kind == DiscDelegate {
				m.AuthorID = cmpOr(m.AuthorID, d.Host)
				if m.AuthorID == d.Host && m.AuthorName == "" {
					m.AuthorName = hostName
				}
			}
			if m.KeyAt != nil {
				m.KeyBy = cmpOr(m.KeyBy, d.Host)
			}
			m.ClientID = clientIDFor(m, d.Host)
			// 房主 worker 写的报告是房主本机的路径，没发布成附件就不给别人
			m.Report = ""
			for j := range m.Attachments {
				m.Attachments[j].Path = ""
			}
		}
		for i := range b.Executions {
			x := &b.Executions[i]
			x.Owner = cmpOr(x.Owner, d.Host)
			x.TaskID, x.Worker, x.WorkspaceID, x.WorkspaceLabel, x.ContextLen, x.Acked = "", "", "", "", 0, 0
		}
		return nil
	})
	return b, err
}

// clientIDFor：成员发的消息存的是「指纹:客户端请求 ID」，原样同步，作者本人按前缀认回自己的（对账、去重）；
// 房主本机用户的不同步。
func clientIDFor(m *DiscMessage, host string) string {
	if m.AuthorID == host || !strings.HasPrefix(m.ClientID, m.AuthorID+":") {
		return ""
	}
	return m.ClientID
}

// --- 加入者

// CreateMirror 建加入的讨论的本机缓存和加入记录（已经有的话只更新加入记录：重新加入沿用缓存）。
func (s *Store) CreateMirror(d Discussion, j Joined) error {
	return s.withTx(func(tx *sql.Tx) error {
		addrs, _ := json.Marshal(j.Addrs)
		if _, err := tx.Exec(`INSERT INTO discussions(key,host,local_id,title,description,project_id,project_name,project_path,workspace_id,status,created_at,mirror)
			VALUES(?,?,?,?,?,'',?,?,'',?,?,1) ON CONFLICT(key) DO NOTHING`,
			d.Key, d.Host, d.LocalID, d.Title, d.Description, d.ProjectName, d.ProjectPath, DiscussionActive, ms(d.CreatedAt)); err != nil {
			if isUnique(err) {
				return fmt.Errorf("%w: discussion id collision", ErrConflict)
			}
			return err
		}
		var mirror bool
		if err := tx.QueryRow(`SELECT mirror FROM discussions WHERE key=?`, d.Key).Scan(&mirror); err != nil {
			return err
		}
		if !mirror {
			return fmt.Errorf("%w: this is your own discussion", ErrConflict)
		}
		if _, err := tx.Exec(`INSERT INTO collab_joined(`+joinedCols+`) VALUES(?,?,?,?,?,?,0,0,'',?,?,?)
			ON CONFLICT(room) DO UPDATE SET host_name=excluded.host_name, addrs=excluded.addrs, request=excluded.request, state=excluded.state, error='',
			secret=excluded.secret, updated_at=excluded.updated_at, profile_sent=0`,
			j.Room, j.Host, j.HostName, string(addrs), j.Request, j.State, j.Secret, ms(j.JoinedAt), ms(j.JoinedAt)); err != nil {
			return err
		}
		_, err := bump(tx, d.Key)
		return err
	})
}

func (s *Store) Joined(room string) (Joined, error) {
	return scanJoined(s.db.QueryRow(`SELECT `+joinedCols+` FROM collab_joined WHERE room=?`, room))
}

func (s *Store) JoinedRooms() ([]Joined, error) {
	return collect(s.db, scanJoined, `SELECT `+joinedCols+` FROM collab_joined ORDER BY joined_at`)
}

// SetJoined 改加入状态（申请结果、代际、地址、最近的错误）。会推进讨论版本，让窗口看到变化。
func (s *Store) SetJoined(room, state string, gen int64, addrs []string, errText string) error {
	return s.withTx(func(tx *sql.Tx) error {
		j, err := scanJoined(tx.QueryRow(`SELECT `+joinedCols+` FROM collab_joined WHERE room=?`, room))
		if err != nil {
			return err
		}
		if addrs == nil {
			addrs = j.Addrs
		}
		if j.State == state && j.Generation == gen && j.Error == errText && strings.Join(j.Addrs, ",") == strings.Join(addrs, ",") {
			return nil
		}
		b, _ := json.Marshal(addrs)
		secret := j.Secret
		if state != RequestPending {
			secret = ""
		}
		if _, err := tx.Exec(`UPDATE collab_joined SET state=?, generation=?, addrs=?, error=?, secret=?, updated_at=? WHERE room=?`, state, gen, string(b), errText, secret, ms(time.Now()), room); err != nil {
			return err
		}
		_, err = bump(tx, room)
		return err
	})
}

var errNoChange = errors.New("no change")

// ApplySync 把房主的一批增量写进本机缓存：讨论、成员资格、Agent 公开配置、消息、执行。
// 自己的 Agent 的私有配置（工作区、worker、接收位置）不被覆盖；本机接受过的执行以本机进度为准，
// 房主撤销时只把账本记成撤销（本机已开始的进程照旧收尾，结果不再交上去）。
func (s *Store) ApplySync(room, self string, b SyncBatch) (bool, error) {
	err := s.withTx(func(tx *sql.Tx) error {
		j, err := scanJoined(tx.QueryRow(`SELECT `+joinedCols+` FROM collab_joined WHERE room=?`, room))
		if err != nil {
			return err
		}
		if b.Rev < j.Cursor {
			return fmt.Errorf("%w: host cursor went back from %d to %d", ErrConflict, j.Cursor, b.Rev)
		}
		if b.Rev == j.Cursor && len(b.People)+len(b.Members)+len(b.Messages)+len(b.Executions) == 0 {
			return errNoChange
		}
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		sd := b.Discussion
		var archived any
		if sd.ArchivedAt != nil {
			archived = ms(*sd.ArchivedAt)
		}
		var summaryAt int64
		if sd.SummaryAt != nil {
			summaryAt = ms(*sd.SummaryAt)
		}
		if _, err := tx.Exec(`UPDATE discussions SET title=?, description=?, status=?, archived_at=?, shared=?, summary=?, summary_seq=?, summary_by=?, summary_at=? WHERE key=? AND mirror=1`,
			sd.Title, sd.Description, sd.Status, archived, sd.Shared, sd.Summary, sd.SummarySeq, sd.SummaryBy, summaryAt, room); err != nil {
			return err
		}
		for _, p := range b.People {
			var ended any
			if p.EndedAt != nil {
				ended = ms(*p.EndedAt)
			}
			// 自己那一行本机可能先改了（SetOwnProfile）：房主那份的资料版本旧，名字和头像留本机的
			if _, err := tx.Exec(`INSERT INTO collab_people(`+personCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(room,fp) DO UPDATE SET generation=excluded.generation, state=excluded.state, host=excluded.host, role=excluded.role,
				rev=excluded.rev, joined_at=excluded.joined_at, ended_at=excluded.ended_at,
				name=CASE WHEN excluded.profile_rev>=collab_people.profile_rev THEN excluded.name ELSE collab_people.name END,
				avatar=CASE WHEN excluded.profile_rev>=collab_people.profile_rev THEN excluded.avatar ELSE collab_people.avatar END,
				profile_rev=MAX(excluded.profile_rev, collab_people.profile_rev)`,
				room, p.FP, p.Name, p.Generation, p.State, p.Host, rev, ms(p.JoinedAt), ended, p.Avatar, p.ProfileRev, p.Role); err != nil {
				return err
			}
		}
		for _, m := range b.Members {
			var removed any
			if m.RemovedAt != nil {
				removed = ms(*m.RemovedAt)
			}
			var own, paused, owner string
			var gen int64
			found := tx.QueryRow(`SELECT own_config, paused, owner, generation FROM disc_members WHERE id=? AND discussion=?`, m.ID, room).
				Scan(&own, &paused, &owner, &gen) == nil
			if found && owner == self && (m.Owner != self || m.Generation != gen) {
				// 房主想把本机的 Agent 改成别人的（或空）、换代际：拥有者和代际都不会变，不接受，暂停到拥有者检查设置；
				// 否则改了归属的那份就绕开了本机确认过的配置和访问设置
				if paused == "" {
					paused = ConfigConflict
				}
				if _, err := tx.Exec(`UPDATE disc_members SET paused=?, rev=? WHERE id=?`, paused, rev, m.ID); err != nil {
					return err
				}
				continue
			}
			if found && own == "" && m.Owner == self {
				// 自己的 Agent 在本机没有确认过的配置（旧版本加的）：不猜哪份可信，照房主那份显示，但暂停到拥有者确认保存
				if paused == "" {
					paused = NeedsConfirm
				}
				if _, err := tx.Exec(`UPDATE disc_members SET name=?, role=?, agent=?, model=?, effort=?, config_version=?, removed_at=COALESCE(removed_at, ?), owner=?, generation=?, access=?, paused=?, rev=? WHERE id=?`,
					m.Name, m.Role, m.Agent, m.Model, m.Effort, m.ConfigVersion, removed, m.Owner, m.Generation, m.Access, paused, rev, m.ID); err != nil {
					return err
				}
				continue
			}
			if found && own != "" && m.Owner == self {
				// 自己的 Agent：配置以本机拥有者设下的为准，房主那份对不上时不照搬，暂停并说明，拥有者重新保存设置即可
				if ownConfig(m) != own {
					if paused == "" {
						paused = ConfigConflict
					}
				} else if paused == ConfigConflict {
					paused = ""
				}
				// 本机的 Agent 删了就是删了：同步不能把删除标记清掉让它复活（重新添加是新的 ID）
				if _, err := tx.Exec(`UPDATE disc_members SET config_version=?, removed_at=COALESCE(removed_at, ?), paused=?, rev=? WHERE id=?`,
					m.ConfigVersion, removed, paused, rev, m.ID); err != nil {
					return err
				}
				continue
			}
			res, err := tx.Exec(`UPDATE disc_members SET name=?, role=?, agent=?, model=?, effort=?, config_version=?, removed_at=?, owner=?, generation=?, access=?, rev=?
				WHERE id=? AND discussion=?`, m.Name, m.Role, m.Agent, m.Model, m.Effort, m.ConfigVersion, removed, m.Owner, m.Generation, m.Access, rev, m.ID, room)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				// 自己的 Agent 第一次同步下来：在拥有者确认（加成员成功后马上 SetOwnConfig）之前不执行
				paused := ""
				if m.Owner == self {
					paused = NeedsConfirm
				}
				if _, err := tx.Exec(`INSERT INTO disc_members(id,discussion,name,role,agent,model,effort,workspace_id,config_version,removed_at,rev,created_at,owner,generation,paused,access)
					VALUES(?,?,?,?,?,?,?,'',?,?,?,?,?,?,?,?)`, m.ID, room, m.Name, m.Role, m.Agent, m.Model, m.Effort, m.ConfigVersion, removed, rev, ms(m.CreatedAt), m.Owner, m.Generation, paused, m.Access); err != nil {
					return err
				}
			}
		}
		for _, m := range b.Messages {
			var key any
			if m.KeyAt != nil {
				key = ms(*m.KeyAt)
			}
			res, err := tx.Exec(`UPDATE disc_messages SET key_at=?, key_by=?, rev=? WHERE id=? AND discussion=?`, key, m.KeyBy, rev, m.ID, room)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				continue
			}
			for i := range m.Attachments {
				m.Attachments[i].Path = MirrorAttachmentPath(room, m.Attachments[i].ID, m.Attachments[i].Name)
				m.Attachments[i].Name = SafeName(m.Attachments[i].Name)
			}
			// 自己发的消息恢复成本机的客户端请求 ID（按它对账、去重），别人的不留
			cid := ""
			if p := self + ":"; strings.HasPrefix(m.ClientID, p) {
				cid = strings.TrimPrefix(m.ClientID, p)
			}
			m.ClientID = cid
			m.Discussion, m.Rev = room, rev
			if err := insertDiscMessageAt(tx, &m, ""); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE discussions SET msg_seq=MAX(msg_seq, ?) WHERE key=?`, m.Seq, room); err != nil {
				return err
			}
		}
		for _, x := range b.Executions {
			var acc string
			tx.QueryRow(`SELECT state FROM collab_accepts WHERE exec=?`, x.ID).Scan(&acc)
			if acc != "" && x.Owner == self {
				if x.State == ExecRevoked && acc != AcceptRevoked {
					if _, err := tx.Exec(`UPDATE collab_accepts SET state='revoked', reason=? WHERE exec=?`, x.Reason, x.ID); err != nil {
						return err
					}
					if _, err := tx.Exec(`UPDATE collab_outbox SET state='rejected', error=? WHERE exec=? AND state='pending'`, "access revoked", x.ID); err != nil {
						return err
					}
					// 还没开始的不再开始；已经开始的本机照旧收尾
					if _, err := tx.Exec(`UPDATE disc_executions SET state='revoked', reason=?, rev=? WHERE id=? AND state IN `+inList(pendingStates), x.Reason, rev, x.ID); err != nil {
						return err
					}
				}
				continue
			}
			var started, ended any
			if x.StartedAt != nil {
				started = ms(*x.StartedAt)
			}
			if x.EndedAt != nil {
				ended = ms(*x.EndedAt)
			}
			res, err := tx.Exec(`UPDATE disc_executions SET state=?, reason=?, agent=?, model=?, effort=?, role=?, config_version=?, reply=?, started_at=?, ended_at=?, owner=?, generation=?, root=?, depth=?, rev=?
				WHERE id=? AND discussion=?`, x.State, x.Reason, x.Agent, x.Model, x.Effort, x.Role, x.ConfigVersion, x.Reply, started, ended, x.Owner, x.Generation, x.Root, x.Depth, rev, x.ID, room)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				x.Discussion, x.Rev, x.WorkspaceID, x.WorkspaceLabel = room, rev, "", ""
				if err := insertExecution(tx, x); err != nil {
					return err
				}
				if _, err := tx.Exec(`UPDATE disc_executions SET reply=?, started_at=?, ended_at=? WHERE id=?`, x.Reply, started, ended, x.ID); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(`UPDATE collab_joined SET cursor=?, host_name=COALESCE((SELECT name FROM collab_people WHERE room=? AND host=1), host_name), updated_at=? WHERE room=?`,
			b.Rev, room, ms(time.Now()), room)
		return err
	})
	if errors.Is(err, errNoChange) {
		return false, nil
	}
	return err == nil, err
}

// AttachmentsAfter 是序号大于 seq 的消息上的附件（按序号，最多 limit 条消息），后台按游标增量下载。
func (s *Store) AttachmentsAfter(room string, seq int64, limit int) ([]DiscMessage, error) {
	return collect(s.db, scanDiscMessage, `SELECT `+dmsgCols+` FROM disc_messages WHERE discussion=? AND seq>? AND attachments!='' ORDER BY seq LIMIT ?`, room, seq, limit)
}

// AttachmentCursor 是附件下载处理到的消息序号。
func (s *Store) AttachmentCursor(room string) int64 {
	var v int64
	s.db.QueryRow(`SELECT att_seq FROM collab_joined WHERE room=?`, room).Scan(&v)
	return v
}

func (s *Store) SetAttachmentCursor(room string, seq int64) error {
	_, err := s.db.Exec(`UPDATE collab_joined SET att_seq=MAX(att_seq, ?) WHERE room=?`, seq, room)
	return err
}

// StaleUploads 是超过 olderThan 还没传完的上传（断线、放弃），由调用方删临时文件后 AbortUpload。
func (s *Store) StaleUploads(before time.Time) ([]Upload, error) {
	return collect(s.db, scanUpload, `SELECT `+uploadCols+` FROM collab_uploads WHERE state='uploading' AND created_at<?`, ms(before))
}

// UnusedUploads 是这个人这些上传里已发布、没被消息用过的（同一条消息重发、房主按客户端 ID 去重之后要放掉）。
func (s *Store) UnusedUploads(room, fp string, ids []string) []Upload {
	var out []Upload
	for _, id := range ids {
		if u, err := scanUpload(s.db.QueryRow(`SELECT `+uploadCols+` FROM collab_uploads WHERE id=? AND room=? AND fp=? AND used=0`, id, room, fp)); err == nil {
			out = append(out, u)
		}
	}
	return out
}

// DropUpload 删掉一份没用上的上传记录（文件由调用方删）。
func (s *Store) DropUpload(id string) error {
	_, err := s.db.Exec(`DELETE FROM collab_uploads WHERE id=? AND used=0`, id)
	return err
}

// DropMirror 删掉一个从没加入成功的讨论缓存（申请被拒、邀请无效）：没有消息和成员才删。
func (s *Store) DropMirror(room string) error {
	return s.withTx(func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM disc_messages WHERE discussion=?)+(SELECT COUNT(*) FROM disc_members WHERE discussion=?)`, room, room).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		for _, t := range collabRoomTables {
			if _, err := tx.Exec(`DELETE FROM `+t+` WHERE room=?`, room); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`DELETE FROM discussions WHERE key=? AND mirror=1`, room)
		if err == nil {
			_, err = tx.Exec(`INSERT INTO disc_meta(k,v) VALUES('seq',1) ON CONFLICT(k) DO UPDATE SET v=v+1`)
		}
		return err
	})
}

// SaveIntent 在提交给房主之前记下本机授权意图：@ 自己的哪个 Agent、配置版本、请求摘要。
// 领取时必须对上，房主不能伪造一条归属相同的请求让本机执行。
func (s *Store) SaveIntent(room, clientID, member string, configVersion int64, dig, config string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO collab_intents(room,client_id,member,config_version,digest,config,created_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(room,client_id) DO NOTHING`, room, clientID, member, configVersion, dig, config, ms(at))
	return err
}

// Intent 是记下的授权意图。
type Intent struct {
	Member        string
	ConfigVersion int64
	Digest        string
	Config        string // 发出时拥有者确认的配置内容（OwnConfigDigest）
}

func (s *Store) Intent(room, clientID string) (Intent, error) {
	var in Intent
	err := s.db.QueryRow(`SELECT member, config_version, digest, config FROM collab_intents WHERE room=? AND client_id=?`, room, clientID).Scan(&in.Member, &in.ConfigVersion, &in.Digest, &in.Config)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrNotFound
	}
	return in, err
}

// ClaimCandidates 是房主那边等自己领取的执行（本机缓存里归自己、还在排队、账本里没有的）。
func (s *Store) ClaimCandidates(room, self string) ([]Execution, error) {
	return collect(s.db, scanExecution, `SELECT `+execCols+` FROM disc_executions x WHERE discussion=? AND owner=? AND state='queued'
		AND NOT EXISTS (SELECT 1 FROM collab_accepts a WHERE a.exec=x.id) ORDER BY created_at, rowid`, room, self)
}

// Accept 记下本机接受了一轮（领取成功之后）；重复调用不产生第二条。
func (s *Store) Accept(room, exec string, gen int64, state, reason string, at time.Time) error {
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO collab_accepts(exec,room,generation,state,reason,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(exec) DO NOTHING`,
			exec, room, gen, state, reason, ms(at)); err != nil {
			return err
		}
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		// 房主那边已经是 claimed（同步先到了）：本机队列里它还是排队，由本机调度
		if state == AcceptAccepted || state == AcceptFetching {
			_, err = tx.Exec(`UPDATE disc_executions SET state='queued', reason='', rev=? WHERE id=? AND state='claimed'`, rev, exec)
		}
		return err
	})
}

// MarkAccepted 把下完附件的领取转成 accepted（进本机队列）。同一事务里重查：账本仍是 fetching、
// 仍是加入中的成员、代际没变、Agent 没离开、执行还没开始也没被撤销；不满足就不进队列（撤销和下载完成并发时不复活）。
func (s *Store) MarkAccepted(exec string) (bool, error) {
	ok := false
	err := s.withTx(func(tx *sql.Tx) error {
		var room string
		var gen int64
		if err := tx.QueryRow(`SELECT room, generation FROM collab_accepts WHERE exec=? AND state='fetching'`, exec).Scan(&room, &gen); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		j, err := scanJoined(tx.QueryRow(`SELECT `+joinedCols+` FROM collab_joined WHERE room=?`, room))
		if err != nil {
			return err
		}
		x, err := scanExecution(tx.QueryRow(`SELECT `+execCols+` FROM disc_executions WHERE id=?`, exec))
		if err != nil {
			return err
		}
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, x.Member))
		if err != nil {
			return err
		}
		if j.State != JoinedActive || j.Generation != gen || m.RemovedAt != nil || x.State != ExecQueued && x.State != ExecClaimed {
			return nil
		}
		if _, err := tx.Exec(`UPDATE collab_accepts SET state='accepted' WHERE exec=? AND state='fetching'`, exec); err != nil {
			return err
		}
		if _, err := bump(tx, room); err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}

// Fetching 是房间里领取了、附件还没下完的执行。
func (s *Store) Fetching(room string) []string {
	var out []string
	rows, err := s.db.Query(`SELECT exec FROM collab_accepts WHERE room=? AND state='fetching'`, room)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var e string
		if rows.Scan(&e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// AcceptState 是本机账本里这一轮的状态，没有是空串。
func (s *Store) AcceptState(exec string) string {
	var st string
	s.db.QueryRow(`SELECT state FROM collab_accepts WHERE exec=?`, exec).Scan(&st)
	return st
}

// AcceptStates 是房间里本机账本的状态（执行 ID → 状态），界面用。
func (s *Store) AcceptStates(room string) map[string]string {
	out := map[string]string{}
	rows, err := s.db.Query(`SELECT exec, state FROM collab_accepts WHERE room=?`, room)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var e, st string
		if rows.Scan(&e, &st) == nil {
			out[e] = st
		}
	}
	return out
}

// PendingOutbox 是房间里还没交出去的事件，按写入先后。
func (s *Store) PendingOutbox(room string) ([]OutboxItem, error) {
	rows, err := s.db.Query(`SELECT exec, seq, room, payload, digest, state, error FROM collab_outbox WHERE room=? AND state='pending' ORDER BY created_at, exec, seq`, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		if err := rows.Scan(&it.Exec, &it.Seq, &it.Room, &it.Payload, &it.Digest, &it.State, &it.Error); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(it.Payload), &it.Event)
		out = append(out, it)
	}
	return out, rows.Err()
}

// OutboxCount 是房间里还没交出去和被拒收的事件数（界面提示）。
func (s *Store) OutboxCount(room string) (pending, rejected int) {
	s.db.QueryRow(`SELECT COUNT(*) FROM collab_outbox WHERE room=? AND state='pending'`, room).Scan(&pending)
	s.db.QueryRow(`SELECT COUNT(*) FROM collab_outbox WHERE room=? AND state='rejected'`, room).Scan(&rejected)
	return
}

// MarkOutbox 记下一条事件交出去了（sent）或被拒收（rejected，终态：同一执行后面的事件一起拒收，不无限重试）。
func (s *Store) MarkOutbox(exec string, seq int64, state, errText string) error {
	if state == OutboxRejected {
		_, err := s.db.Exec(`UPDATE collab_outbox SET state='rejected', error=? WHERE exec=? AND seq>=? AND state='pending'`, errText, exec, seq)
		return err
	}
	_, err := s.db.Exec(`UPDATE collab_outbox SET state=?, error=? WHERE exec=? AND seq=?`, state, errText, exec, seq)
	return err
}

// LocalRevoke 是加入者知道自己被移除或退出了：账本记撤销、待交的事件拒收、还没开始的轮次不再开始、
// 自己的 Agent 暂停。已经开始的本机进程照旧收尾，结果留在本机。
func (s *Store) LocalRevoke(room, state, reason string, at time.Time) error {
	return s.withTx(func(tx *sql.Tx) error {
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE collab_joined SET state=?, error=?, secret='', updated_at=? WHERE room=?`, state, reason, ms(at), room); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE collab_accepts SET state='revoked', reason=? WHERE room=? AND state IN ('accepted','fetching')`, reason, room); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE collab_outbox SET state='rejected', error=? WHERE room=? AND state='pending'`, reason, room); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE disc_executions SET state='revoked', reason=?, rev=? WHERE discussion=? AND owner=? AND state IN `+inList(pendingStates),
			reason, rev, room, s.Self()); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE disc_members SET paused=?, rev=? WHERE discussion=? AND owner=? AND removed_at IS NULL`, reason, rev, room, s.Self())
		return err
	})
}

// PauseOwnQueues 在和房主断开时暂停自己还有已领取、没开始的轮次的 Agent：重连后由拥有者确认再开始。
func (s *Store) PauseOwnQueues(room, reason string) (int, error) {
	n := 0
	err := s.withTx(func(tx *sql.Tx) error {
		ms, err := collect(tx, scanMember, `SELECT `+memberCols+` FROM disc_members m WHERE discussion=? AND owner=? AND removed_at IS NULL AND paused=''
			AND EXISTS (SELECT 1 FROM disc_executions x JOIN collab_accepts a ON a.exec=x.id WHERE x.member=m.id AND x.state='queued' AND a.state='accepted')`, room, s.Self())
		if err != nil || len(ms) == 0 {
			return err
		}
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if _, err := tx.Exec(`UPDATE disc_members SET paused=?, rev=? WHERE id=?`, reason, rev, m.ID); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// SetMemberPrivate 记下自己 Agent 在本机的私有配置（执行工作区）：只存在拥有者电脑上。
func (s *Store) SetMemberPrivate(id, workspaceID, label string) error {
	return s.withTx(func(tx *sql.Tx) error {
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, id))
		if err != nil {
			return err
		}
		if m.WorkspaceID == workspaceID && m.WorkspaceLabel == label {
			return nil
		}
		rev, err := bump(tx, m.Discussion)
		if err != nil {
			return err
		}
		paused := m.Paused
		if paused == NeedsWorkspace {
			paused = ""
		}
		_, err = tx.Exec(`UPDATE disc_members SET workspace_id=?, workspace_label=?, paused=?, rev=? WHERE id=?`, workspaceID, label, paused, rev, id)
		return err
	})
}

// NeedsConfirm 是自己的 Agent 在本机没有确认过的配置（旧版本加的、刚同步下来的）时的暂停原因。
const NeedsConfirm = "confirm this agent's settings on this computer before it runs requests"

// ConfigUntrusted 报告这个暂停原因是不是配置还没被拥有者确认（不能按它发请求、重试）。
func ConfigUntrusted(paused string) bool { return paused == ConfigConflict || paused == NeedsConfirm }

// ConfigConflict 是房主那份自己 Agent 的配置和本机拥有者设下的不一致时的暂停原因。
const ConfigConflict = "the host's copy of this agent's settings differs from what you set on this computer; review and save its settings"

// ownConfig 是拥有者确认过的公开配置内容（名字、职责、agent、模型、思考深度、访问）。
// 访问是空（只有拥有者）时和加访问设置之前的算法一样，已经确认过的 Agent 不会因为升级被判成对不上。
func ownConfig(m Member) string {
	if m.Access == "" {
		return digest(m.Name, m.Role, m.Agent, m.Model, m.Effort)
	}
	return digest(m.Name, m.Role, m.Agent, m.Model, m.Effort, "access", m.Access)
}

// OwnConfigDigest 是授权意图里记的配置内容。
func OwnConfigDigest(m Member) string { return digest(m.Role, m.Agent, m.Model, m.Effort) }

// SetOwnConfig 记下拥有者在本机为自己的 Agent 设的公开配置和执行工作区（加、改成功之后）：之后同步来的房主那份
// 不覆盖它，对不上就暂停。
func (s *Store) SetOwnConfig(m Member) error {
	return s.withTx(func(tx *sql.Tx) error {
		old, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, m.ID))
		if err != nil {
			return err
		}
		rev, err := bump(tx, old.Discussion)
		if err != nil {
			return err
		}
		paused := old.Paused
		if paused == ConfigConflict || paused == NeedsConfirm || paused == NeedsWorkspace && m.WorkspaceID != "" {
			paused = ""
		}
		_, err = tx.Exec(`UPDATE disc_members SET name=?, role=?, agent=?, model=?, effort=?, workspace_id=?, workspace_label=?, access=?, own_config=?, paused=?, rev=? WHERE id=?`,
			m.Name, m.Role, m.Agent, m.Model, m.Effort, m.WorkspaceID, m.WorkspaceLabel, m.Access, ownConfig(m), paused, rev, m.ID)
		return err
	})
}

// MarkOwnRemoved 在本机记下自己的 Agent 已删除（加入的讨论里经房主删除之后）：不等房主同步下来，
// 房主应答了删除却不下发删除标记时，它也不再领取、调度别人的请求。
func (s *Store) MarkOwnRemoved(id string, at time.Time) error {
	return s.withTx(func(tx *sql.Tx) error {
		m, err := scanMember(tx.QueryRow(`SELECT `+memberCols+` FROM disc_members WHERE id=?`, id))
		if err != nil {
			return err
		}
		if m.Owner == "" || m.Owner != s.Self() {
			return ErrNotOwner
		}
		rev, err := bump(tx, m.Discussion)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE disc_members SET removed_at=COALESCE(removed_at, ?), task_id='', worker='', worker_config=0, rev=? WHERE id=?`, ms(at), rev, id)
		return err
	})
}

// OwnConfigHolds 报告这个 Agent 现在归本机这个人、确认过配置，而且库里当前的公开配置（含访问设置）
// 就是确认过的那份：别人凭访问设置发来的请求，领取前按它核对，不信同步来的字段。
func (s *Store) OwnConfigHolds(id string) bool {
	m, err := s.Member(id)
	if err != nil || m.Owner == "" || m.Owner != s.Self() || m.RemovedAt != nil {
		return false
	}
	var joined int64
	if s.db.QueryRow(`SELECT generation FROM collab_joined WHERE room=?`, m.Discussion).Scan(&joined) != nil || joined != m.Generation {
		return false
	}
	var own string
	return s.db.QueryRow(`SELECT own_config FROM disc_members WHERE id=?`, id).Scan(&own) == nil && own != "" && own == ownConfig(m)
}

// OwnConfirmed 报告这台电脑是否确认过自己这个 Agent 的配置（SetOwnConfig 记过）。
func (s *Store) OwnConfirmed(id string) bool {
	var own string
	return s.db.QueryRow(`SELECT own_config FROM disc_members WHERE id=?`, id).Scan(&own) == nil && own != ""
}

// AddRequestWorkspace 返回这次添加（请求 ID）先前为 Agent 新建的 worktree 工作区和名字；没有记录时是 ErrNotFound。
func (s *Store) AddRequestWorkspace(room, client string) (workspace, worktree string, err error) {
	err = s.db.QueryRow(`SELECT workspace_id, worktree FROM collab_add_workspaces WHERE room=? AND client=?`, room, client).Scan(&workspace, &worktree)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return workspace, worktree, err
}

// SetAddRequestWorkspace 记下这次添加新建的 worktree 工作区；workspace 为空时删掉记录（工作区已经没了）。
func (s *Store) SetAddRequestWorkspace(room, client, workspace, worktree string, at time.Time) error {
	if workspace == "" {
		_, err := s.db.Exec(`DELETE FROM collab_add_workspaces WHERE room=? AND client=?`, room, client)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO collab_add_workspaces(room,client,workspace_id,worktree,created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(room,client) DO UPDATE SET workspace_id=excluded.workspace_id, worktree=excluded.worktree`, room, client, workspace, worktree, ms(at))
	return err
}

// NeedsWorkspace 是自己的 Agent 在本机还没有执行工作区时的暂停原因：拥有者配置之前不猜路径。
const NeedsWorkspace = "choose a workspace on this computer"

func mimeType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md", ".log":
		return "text/plain"
	}
	return ""
}

// KnownMember 是这个身份是不是某个共享讨论的有效成员（协作连接用哪份名额）。
func (s *Store) KnownMember(fp string) bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM collab_people p JOIN discussions d ON d.key=p.room WHERE p.fp=? AND p.state='active' AND p.host=0 AND d.shared=1`, fp).Scan(&n)
	return n > 0
}

// PauseOwnMembers 暂停自己在这个讨论里的所有 Agent（退出中）：不改账本、不拒收 outbox。
func (s *Store) PauseOwnMembers(room, reason string) error {
	return s.withTx(func(tx *sql.Tx) error {
		rev, err := bump(tx, room)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE disc_members SET paused=?, rev=? WHERE discussion=? AND owner=? AND removed_at IS NULL`, reason, rev, room, s.Self())
		return err
	})
}
