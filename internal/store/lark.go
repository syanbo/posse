package store

import (
	"database/sql"
	"errors"
	"time"
)

// larkSchema 是本机私有的飞书机器人绑定、卡片关联与事件去重表（不进入局域网讨论同步）。
const larkSchema = `
CREATE TABLE IF NOT EXISTS lark_bindings (
	id TEXT PRIMARY KEY,
	bot_id TEXT NOT NULL,
	chat_id TEXT NOT NULL,
	chat_type TEXT NOT NULL DEFAULT '',
	thread_key TEXT NOT NULL DEFAULT '',
	discussion_key TEXT NOT NULL,
	member_id TEXT NOT NULL DEFAULT '',
	workspace_id TEXT NOT NULL DEFAULT '',
	agent TEXT NOT NULL DEFAULT '',
	permission_mode TEXT NOT NULL DEFAULT '',
	active INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS lark_bindings_active ON lark_bindings(bot_id, chat_id, thread_key) WHERE active = 1;
CREATE INDEX IF NOT EXISTS lark_bindings_disc ON lark_bindings(discussion_key);

CREATE TABLE IF NOT EXISTS lark_cards (
	kind TEXT NOT NULL,
	ref_id TEXT NOT NULL,
	bot_id TEXT NOT NULL,
	chat_id TEXT NOT NULL,
	reply_to TEXT NOT NULL DEFAULT '',
	card_msg_id TEXT NOT NULL,
	discussion_key TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (kind, ref_id)
);
CREATE INDEX IF NOT EXISTS lark_cards_msg ON lark_cards(card_msg_id);

CREATE TABLE IF NOT EXISTS lark_events (
	bot_id TEXT NOT NULL,
	event_key TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (bot_id, event_key)
);
`

// LarkBinding 把一个飞书单聊/群/话题绑定到 Posse 里的一个讨论及当前活跃成员。
type LarkBinding struct {
	ID             string    `json:"id"`
	BotID          string    `json:"botId"`
	ChatID         string    `json:"chatId"`
	ChatType       string    `json:"chatType"` // p2p | group
	ThreadKey      string    `json:"threadKey,omitempty"`
	DiscussionKey  string    `json:"discussionKey"`
	MemberID       string    `json:"memberId,omitempty"`
	WorkspaceID    string    `json:"workspaceId,omitempty"`
	Agent          string    `json:"agent,omitempty"`
	PermissionMode string    `json:"permissionMode,omitempty"` // ask | skip
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// LarkCard 记录某次执行/提问/审批在飞书里对应的卡片消息 ID，用于节流更新与回调反查。
type LarkCard struct {
	Kind          string    `json:"kind"` // exec | question | perm
	RefID         string    `json:"refId"`
	BotID         string    `json:"botId"`
	ChatID        string    `json:"chatId"`
	ReplyTo       string    `json:"replyTo,omitempty"`
	CardMsgID     string    `json:"cardMsgId"`
	DiscussionKey string    `json:"discussionKey,omitempty"`
	Status        string    `json:"status,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (s *Store) ActiveLarkBinding(botID, chatID, threadKey string) (LarkBinding, error) {
	return scanLarkBinding(s.db.QueryRow(`
		SELECT id, bot_id, chat_id, chat_type, thread_key, discussion_key, member_id, workspace_id, agent, permission_mode, active, created_at, updated_at
		FROM lark_bindings WHERE bot_id=? AND chat_id=? AND thread_key=? AND active=1`,
		botID, chatID, threadKey))
}

func (s *Store) LarkBindingByDiscussion(discKey string) (LarkBinding, error) {
	return scanLarkBinding(s.db.QueryRow(`
		SELECT id, bot_id, chat_id, chat_type, thread_key, discussion_key, member_id, workspace_id, agent, permission_mode, active, created_at, updated_at
		FROM lark_bindings WHERE discussion_key=? ORDER BY active DESC, updated_at DESC LIMIT 1`,
		discKey))
}

func (s *Store) ListLarkBindings(botID string) ([]LarkBinding, error) {
	var rows *sql.Rows
	var err error
	if botID != "" {
		rows, err = s.db.Query(`
			SELECT id, bot_id, chat_id, chat_type, thread_key, discussion_key, member_id, workspace_id, agent, permission_mode, active, created_at, updated_at
			FROM lark_bindings WHERE bot_id=? AND active=1 ORDER BY updated_at DESC`, botID)
	} else {
		rows, err = s.db.Query(`
			SELECT id, bot_id, chat_id, chat_type, thread_key, discussion_key, member_id, workspace_id, agent, permission_mode, active, created_at, updated_at
			FROM lark_bindings WHERE active=1 ORDER BY updated_at DESC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LarkBinding
	for rows.Next() {
		b, err := scanLarkBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) SaveLarkBinding(b LarkBinding) error {
	if b.ID == "" {
		b.ID = NewID("lb_")
	}
	now := time.Now()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	act := 0
	if b.Active {
		act = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO lark_bindings(id, bot_id, chat_id, chat_type, thread_key, discussion_key, member_id, workspace_id, agent, permission_mode, active, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			member_id=excluded.member_id,
			workspace_id=excluded.workspace_id,
			agent=excluded.agent,
			permission_mode=excluded.permission_mode,
			active=excluded.active,
			updated_at=excluded.updated_at`,
		b.ID, b.BotID, b.ChatID, b.ChatType, b.ThreadKey, b.DiscussionKey, b.MemberID, b.WorkspaceID, b.Agent, b.PermissionMode, act, ms(b.CreatedAt), ms(b.UpdatedAt))
	return err
}

func (s *Store) DeactivateLarkBinding(botID, chatID, threadKey string) error {
	_, err := s.db.Exec(`UPDATE lark_bindings SET active=0, updated_at=? WHERE bot_id=? AND chat_id=? AND thread_key=? AND active=1`,
		ms(time.Now()), botID, chatID, threadKey)
	return err
}

func scanLarkBinding(row interface{ Scan(...any) error }) (LarkBinding, error) {
	var b LarkBinding
	var act int
	var created, updated int64
	err := row.Scan(&b.ID, &b.BotID, &b.ChatID, &b.ChatType, &b.ThreadKey, &b.DiscussionKey, &b.MemberID, &b.WorkspaceID, &b.Agent, &b.PermissionMode, &act, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	b.Active = act != 0
	b.CreatedAt = fromMS(created)
	b.UpdatedAt = fromMS(updated)
	return b, err
}

func (s *Store) SaveLarkCard(c LarkCard) error {
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = time.Now()
	}
	_, err := s.db.Exec(`
		INSERT INTO lark_cards(kind, ref_id, bot_id, chat_id, reply_to, card_msg_id, discussion_key, status, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(kind, ref_id) DO UPDATE SET
			card_msg_id=excluded.card_msg_id,
			status=excluded.status,
			updated_at=excluded.updated_at`,
		c.Kind, c.RefID, c.BotID, c.ChatID, c.ReplyTo, c.CardMsgID, c.DiscussionKey, c.Status, ms(c.UpdatedAt))
	return err
}

func (s *Store) LarkCard(kind, refID string) (LarkCard, error) {
	var c LarkCard
	var updated int64
	err := s.db.QueryRow(`SELECT kind, ref_id, bot_id, chat_id, reply_to, card_msg_id, discussion_key, status, updated_at FROM lark_cards WHERE kind=? AND ref_id=?`,
		kind, refID).Scan(&c.Kind, &c.RefID, &c.BotID, &c.ChatID, &c.ReplyTo, &c.CardMsgID, &c.DiscussionKey, &c.Status, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.UpdatedAt = fromMS(updated)
	return c, err
}

func (s *Store) LarkCardByMessageID(cardMsgID string) (LarkCard, error) {
	var c LarkCard
	var updated int64
	err := s.db.QueryRow(`SELECT kind, ref_id, bot_id, chat_id, reply_to, card_msg_id, discussion_key, status, updated_at FROM lark_cards WHERE card_msg_id=? ORDER BY updated_at DESC LIMIT 1`,
		cardMsgID).Scan(&c.Kind, &c.RefID, &c.BotID, &c.ChatID, &c.ReplyTo, &c.CardMsgID, &c.DiscussionKey, &c.Status, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.UpdatedAt = fromMS(updated)
	return c, err
}

// MarkLarkEvent 记录事件去重键：首次返回 true，已处理过返回 false。顺手清理 7 天前的旧去重记录。
func (s *Store) MarkLarkEvent(botID, eventKey string, at time.Time) (bool, error) {
	if botID == "" || eventKey == "" {
		return false, nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO lark_events(bot_id, event_key, created_at) VALUES(?,?,?)`, botID, eventKey, ms(at))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		cutoff := at.Add(-7 * 24 * time.Hour)
		_, _ = s.db.Exec(`DELETE FROM lark_events WHERE created_at < ?`, ms(cutoff))
	}
	return n > 0, nil
}
