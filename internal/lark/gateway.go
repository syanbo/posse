package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"posse/internal/store"
)

// ExecSnap 是网关用来刷新飞书进度卡与结果卡的执行快照。
type ExecSnap struct {
	ID         string
	Discussion string
	MemberName string
	Agent      string
	Workspace  string
	State      string
	StartedAt  *time.Time
	EndedAt    *time.Time
	Tool       string
	ToolInput  string
	Phase      string
	Subagents  int
	TraceCount int
}

// Host 由 Daemon 实现，连接飞书网关与 Posse 讨论/权限/工作区能力。
type Host interface {
	EnabledAgents() []string
	Workspaces() []WorkspaceRef
	EnsureDiscussion(bot BotConfig, chatID, chatType, threadKey, titleHint string) (store.LarkBinding, error)
	ActiveBinding(botID, chatID, threadKey string) (store.LarkBinding, error)
	BindingByDiscussion(discKey string) (store.LarkBinding, error)
	BindingByTerminal(termID string) (store.LarkBinding, bool)
	SwitchAgent(binding store.LarkBinding, agentName, model string) (store.LarkBinding, error)
	SwitchWorkspace(binding store.LarkBinding, query string) (store.LarkBinding, error)
	ResetBinding(botID, chatID, threadKey string) error
	PostFromLark(binding store.LarkBinding, body, metaHeader string, attachments []string) (execID string, err error)
	AnswerQuestion(questionID, body string) error
	StopExecution(execID string) error
	RetryExecution(execID string) (newExecID string, err error)
	DecidePermission(permID, behavior, source string) bool
	ExecutionSnapshot(execID string) (ExecSnap, bool)
	ActiveExecutionFor(discKey string) (ExecSnap, bool)
	SaveCard(c store.LarkCard) error
	GetCard(kind, refID string) (store.LarkCard, error)
	CardByMessageID(cardMsgID string) (store.LarkCard, error)
	MarkEvent(botID, eventKey string) (bool, error)
}

// PermNotice 描述一次挂起的权限请求，由 Daemon 经 PermWatcher 交给网关。
type PermNotice struct {
	ID         string
	TerminalID string
	Agent      string
	Tool       string
	Summary    string
	RuleLabel  string
}

type activeCard struct {
	botID       string
	profile     string
	botName     string
	execID      string
	chatID      string
	threadKey   string
	replyTo     string
	inThread    bool
	cardMsgID   string
	lastJSON    string
	interleaved bool
	doneSeenAt  time.Time

	// patchMu 串行化同一张飞书卡片的进度刷新与最终结果/收拢补丁，防止在途进度覆盖最终结果。
	patchMu sync.Mutex
	closed  bool
}

type threadMessageFetcher interface {
	ListRecentThreadMessages(ctx context.Context, profile, threadID string, limit int) ([]FetchedMessage, error)
}

type activePerm struct {
	botID     string
	profile   string
	botName   string
	agentName string
	wsLabel   string
	tool      string
	summary   string
	ruleLabel string
	cardMsgID string
}

type botRunner struct {
	cfg       BotConfig
	cancel    context.CancelFunc
	mu        sync.Mutex
	running   bool
	msgReady  bool
	cardReady bool
	lastErr   string
	ident     BotIdentity
}

// Gateway 管理全部已启用的飞书机器人长连接、事件路由、卡片节流更新和三端同步。
type Gateway struct {
	client Client
	host   Host
	home   string

	mu          sync.Mutex
	parentCtx   context.Context
	cfg         Config
	runners     map[string]*botRunner
	activeCards map[string]*activeCard // execID -> 正在轮询更新的进度卡
	activePerms map[string]*activePerm // permID -> 正在等待审批的权限卡
	qCards      map[string]questionMem // questionID -> 提问卡片上下文
	loopCancel  context.CancelFunc
}

type questionMem struct {
	botID     string
	profile   string
	botName   string
	agentName string
	question  string
	choices   []string
	cardMsgID string
}

func NewGateway(home string, client Client, host Host) *Gateway {
	return &Gateway{
		client:      client,
		host:        host,
		home:        home,
		runners:     map[string]*botRunner{},
		activeCards: map[string]*activeCard{},
		activePerms: map[string]*activePerm{},
		qCards:      map[string]questionMem{},
	}
}

// Start 启动配置中的已启用机器人并开启 2.5 秒进度卡节流刷新循环。
func (g *Gateway) Start(ctx context.Context, cfg Config) {
	g.mu.Lock()
	g.parentCtx = ctx
	g.mu.Unlock()
	g.Reload(cfg)
	loopCtx, cancel := context.WithCancel(ctx)
	g.mu.Lock()
	if g.loopCancel != nil {
		g.loopCancel()
	}
	g.loopCancel = cancel
	g.mu.Unlock()
	go g.progressLoop(loopCtx)
}

// Reload 热加载机器人配置：停掉已删除/禁用的机器人，启动新增或变更的机器人。
func (g *Gateway) Reload(cfg Config) {
	// 若尚未配置主人 open_id，尝试从本机 lark-cli 自动探测
	if cfg.OwnerOpenID == "" && g.client != nil {
		profile := ""
		if len(cfg.Bots) > 0 {
			profile = cfg.Bots[0].Profile
		}
		pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if id, err := g.client.OwnerOpenID(pctx, profile); err == nil && id != "" {
			cfg.OwnerOpenID = id
			_ = SaveConfig(g.home, cfg)
		}
		cancel()
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg = cfg
	want := map[string]BotConfig{}
	for _, b := range cfg.Bots {
		b = NormalizeBot(b)
		if b.Enabled && b.ID != "" && b.Profile != "" {
			want[b.ID] = b
		}
	}
	for id, r := range g.runners {
		nb, ok := want[id]
		if !ok || r.cfg.Profile != nb.Profile {
			r.cancel()
			delete(g.runners, id)
		} else {
			r.mu.Lock()
			r.cfg = nb
			r.mu.Unlock()
		}
	}
	if g.parentCtx == nil {
		return
	}
	for id, b := range want {
		if _, exists := g.runners[id]; exists {
			continue
		}
		bctx, cancel := context.WithCancel(g.parentCtx)
		r := &botRunner{cfg: b, cancel: cancel, running: true}
		g.runners[id] = r
		go g.runBot(bctx, r)
	}
}

func (g *Gateway) Stop() {
	g.mu.Lock()
	if g.loopCancel != nil {
		g.loopCancel()
		g.loopCancel = nil
	}
	runners := g.runners
	g.runners = map[string]*botRunner{}
	g.mu.Unlock()
	for _, r := range runners {
		r.cancel()
	}
}

func (g *Gateway) Config() Config {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg
}

func (g *Gateway) botByID(id string) (BotConfig, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, b := range g.cfg.Bots {
		if b.ID == id {
			return NormalizeBot(b), true
		}
	}
	return BotConfig{}, false
}

func (g *Gateway) Status() []BotStatus {
	g.mu.Lock()
	bots := append([]BotConfig(nil), g.cfg.Bots...)
	runners := map[string]*botRunner{}
	for k, v := range g.runners {
		runners[k] = v
	}
	g.mu.Unlock()
	out := make([]BotStatus, 0, len(bots))
	for _, b := range bots {
		st := BotStatus{ID: b.ID, Name: b.Name, Profile: b.Profile, Enabled: b.Enabled}
		if r := runners[b.ID]; r != nil {
			r.mu.Lock()
			st.Running = r.running
			st.MsgReady = r.msgReady
			st.CardReady = r.cardReady
			st.LastError = r.lastErr
			r.mu.Unlock()
		}
		out = append(out, st)
	}
	return out
}

func (g *Gateway) runBot(ctx context.Context, r *botRunner) {
	if bip, ok := g.client.(botIdentityProvider); ok {
		r.mu.Lock()
		prof := r.cfg.Profile
		r.mu.Unlock()
		ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if id, err := bip.BotIdentity(ictx, prof); err == nil {
			r.mu.Lock()
			r.ident = id
			r.mu.Unlock()
		}
		cancel()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		g.consumeLoop(ctx, r, "im.message.receive_v1", true, g.handleMessageRaw)
	}()
	go func() {
		defer wg.Done()
		g.consumeLoop(ctx, r, "card.action.trigger", false, g.handleCardActionRaw)
	}()
	wg.Wait()
	r.mu.Lock()
	r.running, r.msgReady, r.cardReady = false, false, false
	r.mu.Unlock()
}

func (g *Gateway) consumeLoop(ctx context.Context, r *botRunner, key string, isMsg bool, handler func(BotConfig, []byte)) {
	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		cfg := r.cfg
		r.mu.Unlock()
		err := g.client.Consume(ctx, cfg.Profile, key, func() {
			r.mu.Lock()
			if isMsg {
				r.msgReady = true
			} else {
				r.cardReady = true
			}
			r.lastErr = ""
			r.mu.Unlock()
			backoff = 2 * time.Second
		}, func(line []byte) {
			r.mu.Lock()
			cur := r.cfg
			r.mu.Unlock()
			go handler(cur, line)
		})
		r.mu.Lock()
		if isMsg {
			r.msgReady = false
		} else {
			r.cardReady = false
		}
		if err != nil && ctx.Err() == nil {
			r.lastErr = err.Error()
			log.Printf("lark bot %s (%s): %v", cfg.Name, key, err)
		}
		r.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (g *Gateway) handleMessageRaw(bot BotConfig, raw []byte) {
	var ev MessageEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	g.HandleMessage(context.Background(), bot, ev)
}

// HandleMessage 处理一条飞书 IM 消息（单测可直接调用）。
func (g *Gateway) HandleMessage(ctx context.Context, bot BotConfig, ev MessageEvent) {
	bot = NormalizeBot(bot)
	msgID := ev.MsgID()
	if msgID == "" || ev.ChatID == "" || ev.SenderType == "app" {
		return
	}
	if ok, err := g.host.MarkEvent(bot.ID, "msg:"+msgID); err != nil || !ok {
		return
	}
	// 同会话/话题在执行期间收到任何新消息（含未 @ 机器人的群聊发言），都视为已有插队消息
	g.markChatInterleaved(ev.ChatID, ev.ThreadID, ev.RootID, "")
	owner := g.Config().OwnerOpenID
	if !bot.AllowsTrigger(ev.SenderID, owner, ev.ChatID) {
		return
	}
	// 群聊只响应真正 @ 了本机器人的消息（开通全群消息权限时，不能误抢 @ 同事的发言）
	ident := g.botIdentity(bot.ID)
	if ev.ChatType == "group" && !isMentioningBot(bot, ident, ev) {
		return
	}

	threadKey := computeThreadKey(bot.SessionMode, ev)
	inThread := ev.ChatType == "group" && bot.SessionMode == SessionThread
	body := stripLeadingMentions(ev.Content, ev.Mentions, bot.Name, ident.BotAppName)

	// 快捷命令：/agent、/ws、/new、/status、/stop、/help
	if strings.HasPrefix(body, "/") {
		if g.handleCommand(ctx, bot, ev, threadKey, inThread, body) {
			return
		}
	}

	// 下载消息携带的图片/文件附件（若有）
	var cleanupDirs []string
	defer func() {
		for _, dir := range cleanupDirs {
			_ = os.RemoveAll(dir)
		}
	}()
	atts, attDir := g.downloadEventAttachments(ctx, bot, ev)
	if attDir != "" {
		cleanupDirs = append(cleanupDirs, attDir)
	}
	if body == "" && len(atts) == 0 && ev.ParentID == "" {
		return
	}

	// 补齐群聊近期上下文（可选）及被引用父消息的正文与附件
	var contextBlocks []string
	if ev.ChatType == "group" && bot.GroupContextCount > 0 {
		if block := g.fetchGroupContext(ctx, bot, ev.ChatID, msgID, ev.ParentID, bot.GroupContextCount); block != "" {
			contextBlocks = append(contextBlocks, block)
		}
	}
	if ev.ParentID != "" {
		quotedBlock, parentAtts, parentDir := g.fetchParentContext(ctx, bot, ev.ParentID)
		if parentDir != "" {
			cleanupDirs = append(cleanupDirs, parentDir)
		}
		if quotedBlock != "" {
			contextBlocks = append(contextBlocks, quotedBlock)
		}
		atts = append(atts, parentAtts...)
	}
	if body == "" && len(atts) == 0 && len(contextBlocks) == 0 {
		return
	}

	titleHint := oneLine(body, 36)
	if titleHint == "" {
		titleHint = bot.Name
	}
	binding, err := g.host.EnsureDiscussion(bot, ev.ChatID, ev.ChatType, threadKey, titleHint)
	if err != nil {
		g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
		return
	}

	// 在消息头部附上飞书坐标，方便技能（如报障排查）按 chat_id / parent_id 反查多维表格记录
	var metaParts []string
	metaParts = append(metaParts, "chat_id="+ev.ChatID)
	if ev.ChatType != "" {
		metaParts = append(metaParts, "chat_type="+ev.ChatType)
	}
	if ev.ParentID != "" {
		metaParts = append(metaParts, "parent_id="+ev.ParentID)
	}
	if ev.RootID != "" {
		metaParts = append(metaParts, "root_id="+ev.RootID)
	}
	metaParts = append(metaParts, "message_id="+msgID)
	metaHeader := "[Feishu " + strings.Join(metaParts, " ") + "]"

	postBody := body
	if len(contextBlocks) > 0 {
		prefix := strings.Join(contextBlocks, "\n\n")
		if postBody != "" {
			postBody = prefix + "\n\n" + postBody
		} else {
			postBody = prefix
		}
	}

	execID, err := g.host.PostFromLark(binding, postBody, metaHeader, atts)
	if err != nil {
		g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
		return
	}

	wsLabel := g.workspaceLabel(binding.WorkspaceID)
	cardJSON := BuildProgressCard(bot.ID, bot.Name, binding.Agent, wsLabel, execID, tr("Queued"), "0s", "", "", "", 0)
	cardMsgID, err := g.client.ReplyCard(ctx, bot.Profile, msgID, inThread, cardJSON)
	if err != nil {
		log.Printf("lark bot %s: reply progress card: %v", bot.Name, err)
		return
	}
	_ = g.host.SaveCard(store.LarkCard{
		Kind:          "exec",
		RefID:         execID,
		BotID:         bot.ID,
		ChatID:        ev.ChatID,
		ReplyTo:       msgID,
		CardMsgID:     cardMsgID,
		DiscussionKey: binding.DiscussionKey,
		Status:        "active",
	})
	g.mu.Lock()
	g.markChatInterleavedLocked(ev.ChatID, threadKey, ev.RootID, execID)
	g.activeCards[execID] = &activeCard{
		botID:     bot.ID,
		profile:   bot.Profile,
		botName:   bot.Name,
		execID:    execID,
		chatID:    ev.ChatID,
		threadKey: threadKey,
		replyTo:   msgID,
		inThread:  inThread,
		cardMsgID: cardMsgID,
		lastJSON:  cardJSON,
	}
	g.mu.Unlock()
}

func computeThreadKey(sessionMode string, ev MessageEvent) string {
	if ev.ChatType != "group" {
		return ""
	}
	if sessionMode == SessionThread {
		if ev.ThreadID != "" {
			return ev.ThreadID
		}
		if ev.RootID != "" {
			return ev.RootID
		}
		return ev.MsgID()
	}
	return ""
}

func (g *Gateway) botIdentity(botID string) BotIdentity {
	g.mu.Lock()
	r := g.runners[botID]
	g.mu.Unlock()
	if r == nil {
		return BotIdentity{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ident
}

func isMentioningBot(bot BotConfig, ident BotIdentity, ev MessageEvent) bool {
	if len(ev.Mentions) > 0 {
		for _, m := range ev.Mentions {
			if ident.BotOpenID != "" && (m.ID == ident.BotOpenID || m.OpenID == ident.BotOpenID) {
				return true
			}
			if ident.BotAppName != "" && strings.EqualFold(strings.TrimSpace(m.Name), ident.BotAppName) {
				return true
			}
			if bot.Name != "" && strings.EqualFold(strings.TrimSpace(m.Name), bot.Name) {
				return true
			}
		}
		return false
	}
	// lark-cli 可能把 @ 预先渲染进 Content 而不带 Mentions 数组
	content := ev.Content
	for _, name := range []string{ident.BotAppName, bot.Name} {
		if name == "" {
			continue
		}
		if strings.Contains(content, "@"+name) || strings.Contains(content, "\uff20"+name) {
			return true
		}
	}
	return false
}

func stripLeadingMentions(content string, mentions []MessageMention, extraNames ...string) string {
	s := strings.TrimSpace(content)
	for _, m := range mentions {
		if m.Key != "" {
			s = strings.ReplaceAll(s, m.Key, " ")
		}
		if m.Name != "" {
			s = strings.ReplaceAll(s, "@"+m.Name, " ")
			s = strings.ReplaceAll(s, "\uff20"+m.Name, " ")
		}
	}
	for _, name := range extraNames {
		if name != "" {
			s = strings.ReplaceAll(s, "@"+name, " ")
			s = strings.ReplaceAll(s, "\uff20"+name, " ")
		}
	}
	s = strings.TrimSpace(s)
	// 兜底去掉开头的单个 @机器人名
	for strings.HasPrefix(s, "@") || strings.HasPrefix(s, "\uff20") {
		idx := strings.IndexAny(s, " \t\n")
		if idx < 0 {
			return ""
		}
		s = strings.TrimSpace(s[idx+1:])
	}
	return strings.TrimSpace(s)
}

func (g *Gateway) handleCommand(ctx context.Context, bot BotConfig, ev MessageEvent, threadKey string, inThread bool, body string) bool {
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return false
	}
	cmd := strings.ToLower(fields[0])
	rest := strings.TrimSpace(strings.TrimPrefix(body, fields[0]))
	msgID := ev.MsgID()

	switch cmd {
	case "/help":
		help := strings.Join([]string{
			"`/agent [name]` — " + tr("Switch or pick the active Agent"),
			"`/ws [name]` — " + tr("Switch or pick the active Workspace"),
			"`/new` — " + tr("Start a fresh conversation"),
			"`/status` — " + tr("Show current binding and active round"),
			"`/stop` — " + tr("Stop the active round"),
		}, "\n")
		g.replyNotice(ctx, bot, msgID, inThread, bot.Name+" — "+tr("Commands"), "blue", help)
		return true

	case "/agent":
		binding, err := g.host.EnsureDiscussion(bot, ev.ChatID, ev.ChatType, threadKey, bot.Name)
		if err != nil {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
			return true
		}
		if rest == "" {
			card := BuildAgentPickerCard(bot.ID, bot.Name, ev.ChatID, threadKey, binding.Agent, g.host.EnabledAgents())
			_, _ = g.client.ReplyCard(ctx, bot.Profile, msgID, inThread, card)
			return true
		}
		parts := strings.Fields(rest)
		model := ""
		if len(parts) > 1 {
			model = parts[1]
		}
		updated, err := g.host.SwitchAgent(binding, parts[0], model)
		if err != nil {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
			return true
		}
		g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "green", fmt.Sprintf("%s: `%s`", tr("Switched agent to"), updated.Agent))
		return true

	case "/ws", "/workspace":
		binding, err := g.host.EnsureDiscussion(bot, ev.ChatID, ev.ChatType, threadKey, bot.Name)
		if err != nil {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
			return true
		}
		if rest == "" {
			card := BuildWorkspacePickerCard(bot.ID, bot.Name, ev.ChatID, threadKey, binding.WorkspaceID, g.host.Workspaces())
			_, _ = g.client.ReplyCard(ctx, bot.Profile, msgID, inThread, card)
			return true
		}
		updated, err := g.host.SwitchWorkspace(binding, rest)
		if err != nil {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
			return true
		}
		g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "green", fmt.Sprintf("%s: `%s`", tr("Switched workspace to"), g.workspaceLabel(updated.WorkspaceID)))
		return true

	case "/new":
		if err := g.host.ResetBinding(bot.ID, ev.ChatID, threadKey); err != nil {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
			return true
		}
		g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "green", tr("Started a fresh session; your next message will begin a new discussion."))
		return true

	case "/stop":
		binding, err := g.host.ActiveBinding(bot.ID, ev.ChatID, threadKey)
		if err != nil {
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "grey", tr("No active round to stop."))
			return true
		}
		snap, ok := g.host.ActiveExecutionFor(binding.DiscussionKey)
		if !ok {
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "grey", tr("No active round to stop."))
			return true
		}
		if err := g.host.StopExecution(snap.ID); err != nil {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
			return true
		}
		g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "grey", tr("Stopped."))
		return true

	case "/status":
		binding, err := g.host.ActiveBinding(bot.ID, ev.ChatID, threadKey)
		if err != nil {
			body := fmt.Sprintf("**%s**: `%s`\n**%s**: `%s`\n**%s**: `%s`",
				tr("Agent"), bot.Agent,
				tr("Workspace"), g.workspaceLabel(bot.WorkspaceID),
				tr("Permission"), bot.PermissionMode)
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name+" — "+tr("Status"), "blue", body)
			return true
		}
		lines := []string{
			fmt.Sprintf("**%s**: `%s`", tr("Agent"), binding.Agent),
			fmt.Sprintf("**%s**: `%s`", tr("Workspace"), g.workspaceLabel(binding.WorkspaceID)),
			fmt.Sprintf("**%s**: `%s`", tr("Permission"), binding.PermissionMode),
			fmt.Sprintf("**%s**: `%s`", tr("Discussion"), binding.DiscussionKey),
		}
		if snap, ok := g.host.ActiveExecutionFor(binding.DiscussionKey); ok {
			lines = append(lines, fmt.Sprintf("**%s**: `%s`", tr("Active round"), snap.State))
		}
		g.replyNotice(ctx, bot, msgID, inThread, bot.Name+" — "+tr("Status"), "blue", strings.Join(lines, "\n"))
		return true
	}
	return false
}

func (g *Gateway) handleCardActionRaw(bot BotConfig, raw []byte) {
	var ev CardActionEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	g.HandleCardAction(context.Background(), bot, ev)
}

// HandleCardAction 处理飞书卡片按钮与表单回调（单测可直接调用）。
func (g *Gateway) HandleCardAction(ctx context.Context, bot BotConfig, ev CardActionEvent) {
	bot = NormalizeBot(bot)
	if ev.EventID != "" {
		if ok, err := g.host.MarkEvent(bot.ID, "card:"+ev.EventID); err != nil || !ok {
			return
		}
	}
	var val map[string]string
	if ev.ActionValue != "" {
		_ = json.Unmarshal([]byte(ev.ActionValue), &val)
	}
	action := val["action"]
	// 表单提交：从 form_value 里读 answer_text，并通过 card_msg_id 反查对应的 questionId
	if action == "" && ev.FormValue != "" {
		var fv map[string]string
		if json.Unmarshal([]byte(ev.FormValue), &fv) == nil && strings.TrimSpace(fv["answer_text"]) != "" {
			if card, err := g.host.CardByMessageID(ev.MessageID); err == nil && card.Kind == "question" {
				action = "answer_form"
				val = map[string]string{
					"questionId": card.RefID,
					"choice":     strings.TrimSpace(fv["answer_text"]),
				}
			}
		}
	}
	owner := g.Config().OwnerOpenID
	isOwner := owner != "" && ev.OperatorID == owner

	switch action {
	case "perm_allow", "perm_allow_session", "perm_deny":
		// 硬安全边界：审批按钮强制仅限本机主人点击
		if !isOwner {
			return
		}
		behavior := "allow"
		switch action {
		case "perm_allow_session":
			behavior = "allow_session"
		case "perm_deny":
			behavior = "deny"
		}
		g.host.DecidePermission(val["permId"], behavior, "feishu")

	case "exec_stop":
		if !isOwner {
			return
		}
		_ = g.host.StopExecution(val["execId"])

	case "exec_retry":
		if !isOwner {
			return
		}
		origReplyTo := ""
		if prevCard, err := g.host.GetCard("exec", val["execId"]); err == nil {
			origReplyTo = prevCard.ReplyTo
		}
		if newID, err := g.host.RetryExecution(val["execId"]); err == nil && newID != "" && ev.MessageID != "" {
			if snap, ok := g.host.ExecutionSnapshot(newID); ok {
				var threadKey string
				var inThread bool
				if binding, berr := g.host.BindingByDiscussion(snap.Discussion); berr == nil {
					threadKey = binding.ThreadKey
					inThread = threadKey != ""
				}
				cardJSON := BuildProgressCard(bot.ID, bot.Name, snap.Agent, snap.Workspace, newID, tr("Queued"), "0s", "", "", "", 0)
				cardMsgID := ev.MessageID
				if err := g.client.PatchCard(ctx, bot.Profile, ev.MessageID, cardJSON); err != nil {
					log.Printf("lark bot %s: patch retry progress card (fallback to new card): %v", bot.Name, err)
					var sentID string
					var sendErr error
					if origReplyTo != "" {
						sentID, sendErr = g.client.ReplyCard(ctx, bot.Profile, origReplyTo, inThread, cardJSON)
					}
					if sendErr != nil || sentID == "" {
						sentID, sendErr = g.client.SendCard(ctx, bot.Profile, ev.ChatID, cardJSON)
						if sendErr == nil && sentID != "" {
							inThread = false
							threadKey = ""
						}
					}
					if sendErr == nil && sentID != "" {
						cardMsgID = sentID
						g.markChatInterleaved(ev.ChatID, threadKey, "", newID)
					}
				}
				_ = g.host.SaveCard(store.LarkCard{
					Kind: "exec", RefID: newID, BotID: bot.ID, ChatID: ev.ChatID,
					ReplyTo: origReplyTo, CardMsgID: cardMsgID, DiscussionKey: snap.Discussion, Status: "active",
				})
				g.mu.Lock()
				g.activeCards[newID] = &activeCard{
					botID: bot.ID, profile: bot.Profile, botName: bot.Name,
					execID: newID, chatID: ev.ChatID, threadKey: threadKey,
					replyTo: origReplyTo, inThread: inThread, cardMsgID: cardMsgID, lastJSON: cardJSON,
				}
				g.mu.Unlock()
			}
		}

	case "answer_choice", "answer_form":
		if !bot.AllowsTrigger(ev.OperatorID, owner, ev.ChatID) {
			return
		}
		qid := val["questionId"]
		ans := strings.TrimSpace(val["choice"])
		if qid == "" || ans == "" {
			return
		}
		if err := g.host.AnswerQuestion(qid, ans); err == nil {
			g.OnQuestionAnswered(qid, ans)
		}

	case "switch_agent":
		if !isOwner {
			return
		}
		binding, err := g.host.ActiveBinding(bot.ID, val["chatId"], val["threadKey"])
		if err != nil {
			binding, err = g.host.EnsureDiscussion(bot, val["chatId"], "group", val["threadKey"], bot.Name)
		}
		if err != nil {
			return
		}
		if updated, err := g.host.SwitchAgent(binding, val["agent"], ""); err == nil && ev.MessageID != "" {
			card := BuildNoticeCard(bot.Name, "green", fmt.Sprintf("%s: `%s`", tr("Switched agent to"), updated.Agent))
			_ = g.client.PatchCard(ctx, bot.Profile, ev.MessageID, card)
		}

	case "switch_ws":
		if !isOwner {
			return
		}
		binding, err := g.host.ActiveBinding(bot.ID, val["chatId"], val["threadKey"])
		if err != nil {
			binding, err = g.host.EnsureDiscussion(bot, val["chatId"], "group", val["threadKey"], bot.Name)
		}
		if err != nil {
			return
		}
		if updated, err := g.host.SwitchWorkspace(binding, val["workspaceId"]); err == nil && ev.MessageID != "" {
			card := BuildNoticeCard(bot.Name, "green", fmt.Sprintf("%s: `%s`", tr("Switched workspace to"), g.workspaceLabel(updated.WorkspaceID)))
			_ = g.client.PatchCard(ctx, bot.Profile, ev.MessageID, card)
		}
	}
}

// --- 出站通知（由 Daemon 在讨论/权限状态变化时触发）---

func (g *Gateway) progressLoop(ctx context.Context) {
	ticker := time.NewTicker(2500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.tickProgress(ctx)
		}
	}
}

func (g *Gateway) tickProgress(ctx context.Context) {
	g.mu.Lock()
	cards := make([]*activeCard, 0, len(g.activeCards))
	for _, c := range g.activeCards {
		cards = append(cards, c)
	}
	g.mu.Unlock()
	for _, c := range cards {
		snap, ok := g.host.ExecutionSnapshot(c.execID)
		if !ok {
			continue
		}
		if !isActiveExecState(snap.State) {
			// 不立即删除 activeCards，保留本地 interleaved 状态供 OnExecFinished 决策；
			// 仅对超过 2 分钟仍未触发 OnExecFinished 的孤儿项做兜底清理。
			g.mu.Lock()
			if c.doneSeenAt.IsZero() {
				c.doneSeenAt = time.Now()
			} else if time.Since(c.doneSeenAt) > 2*time.Minute {
				delete(g.activeCards, c.execID)
			}
			g.mu.Unlock()
			continue
		}
		elapsed := ""
		if snap.StartedAt != nil {
			elapsed = FormatDuration(time.Since(*snap.StartedAt))
		}
		cardJSON := BuildProgressCard(c.botID, c.botName, snap.Agent, snap.Workspace, c.execID, snap.State, elapsed, snap.Tool, snap.ToolInput, snap.Phase, snap.Subagents)
		c.patchMu.Lock()
		if c.closed || cardJSON == c.lastJSON {
			c.patchMu.Unlock()
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := g.client.PatchCard(pctx, c.profile, c.cardMsgID, cardJSON)
		cancel()
		if err == nil {
			c.lastJSON = cardJSON
		}
		c.patchMu.Unlock()
	}
}

func isActiveExecState(st string) bool {
	switch st {
	case "queued", "held", "blocked", "claimed", "starting", "running", "waiting":
		return true
	}
	return false
}

// OnExecFinished 在讨论执行结束时刷新对应飞书卡片（或发送新卡片以实现桌面/手机→飞书反向同步）。
func (g *Gateway) OnExecFinished(discKey, execID, outcome, body string) {
	binding, err := g.host.BindingByDiscussion(discKey)
	if err != nil {
		return
	}
	bot, ok := g.botByID(binding.BotID)
	if !ok || !bot.Enabled {
		return
	}
	snap, _ := g.host.ExecutionSnapshot(execID)
	elapsed := ""
	if snap.StartedAt != nil && snap.EndedAt != nil {
		elapsed = FormatDuration(snap.EndedAt.Sub(*snap.StartedAt))
	} else if snap.StartedAt != nil {
		elapsed = FormatDuration(time.Since(*snap.StartedAt))
	}
	wsLabel := snap.Workspace
	if wsLabel == "" {
		wsLabel = g.workspaceLabel(binding.WorkspaceID)
	}
	agentName := snap.Agent
	if agentName == "" {
		agentName = binding.Agent
	}

	cardJSON, truncated := BuildResultCard(bot.ID, bot.Name, agentName, wsLabel, execID, outcome, elapsed, snap.TraceCount, body)

	g.mu.Lock()
	ac := g.activeCards[execID]
	g.mu.Unlock()
	// 决策结束前保留 activeCards 以便并发收到的插队消息仍能写到 ac.interleaved；函数返回前统一清理。
	defer func() {
		g.mu.Lock()
		if g.activeCards[execID] == ac {
			delete(g.activeCards, execID)
		}
		g.mu.Unlock()
	}()

	var progressMsgID, replyTo, threadKey string
	inThread := binding.ThreadKey != ""
	threadKey = binding.ThreadKey
	if ac != nil {
		progressMsgID = ac.cardMsgID
		replyTo = ac.replyTo
		inThread = ac.inThread
		threadKey = ac.threadKey
	} else if stored, err := g.host.GetCard("exec", execID); err == nil {
		progressMsgID = stored.CardMsgID
		replyTo = stored.ReplyTo
	}

	// patchProgress 在单卡锁下先关闭后续进度轮询并等待在途进度补丁结束，再执行最终 PatchCard。
	patchProgress := func(payload string, timeout time.Duration) error {
		if ac != nil {
			ac.patchMu.Lock()
			ac.closed = true
			defer ac.patchMu.Unlock()
		}
		pctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return g.client.PatchCard(pctx, bot.Profile, progressMsgID, payload)
	}

	// 手动停止且没有正文结论时，直接把原进度卡改成「已停止」，不再多发一条空卡片
	trimmedBody := strings.TrimSpace(body)
	if progressMsgID != "" && (outcome == "stopped" || outcome == "cancelled") && (trimmedBody == "" || trimmedBody == "stopped by the user") {
		if err := patchProgress(cardJSON, 10*time.Second); err != nil {
			log.Printf("lark bot %s: patch stopped card: %v", bot.Name, err)
		}
		return
	}

	// 中间没有插入其他消息时，直接把结果原地更新到原进度卡，避免紧挨着出现两张重复标题的卡片；
	// 历史查询、原地补丁与降级发新卡各自使用独立超时预算，避免前一阶段超时耗尽后续降级发送。
	inPlaceFailed := false
	if g.canUpdateProgressInPlace(bot, binding, ac, progressMsgID, inThread, threadKey) {
		if err := patchProgress(cardJSON, 10*time.Second); err == nil {
			if err := g.host.SaveCard(store.LarkCard{
				Kind: "result", RefID: execID, BotID: bot.ID, ChatID: binding.ChatID,
				ReplyTo: replyTo, CardMsgID: progressMsgID, DiscussionKey: discKey, Status: outcome,
			}); err != nil {
				log.Printf("lark bot %s: save in-place result card: %v", bot.Name, err)
			}
			if truncated && strings.TrimSpace(body) != "" {
				fctx, fcancel := context.WithTimeout(context.Background(), 15*time.Second)
				g.sendLongMarkdownFile(fctx, bot, binding.ChatID, execID, body)
				fcancel()
			}
			return
		} else {
			inPlaceFailed = true
			log.Printf("lark bot %s: patch result card in place (fallback to new card): %v", bot.Name, err)
		}
	}

	// 中间有其他消息插队（审批卡、提问卡或新发言）或原地更新失败时：
	// 先尽力收拢最初的进度卡（移除停止按钮），再使用独立超时把结果作为新卡片发到对话底部。
	if progressMsgID != "" {
		closedTimeout := 10 * time.Second
		if inPlaceFailed {
			closedTimeout = 5 * time.Second
		}
		closedCard := BuildProgressClosedCard(bot.Name, agentName, wsLabel, outcome, elapsed, snap.TraceCount)
		if err := patchProgress(closedCard, closedTimeout); err != nil {
			log.Printf("lark bot %s: patch progress closed card: %v", bot.Name, err)
		}
	}

	var resultMsgID string
	var sendErr error
	sentThreadKey := ""
	if replyTo != "" {
		rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
		resultMsgID, sendErr = g.client.ReplyCard(rctx, bot.Profile, replyTo, inThread, cardJSON)
		rcancel()
		if sendErr == nil && inThread {
			sentThreadKey = threadKey
		}
		if sendErr != nil {
			sctx, scancel := context.WithTimeout(context.Background(), 15*time.Second)
			resultMsgID, sendErr = g.client.SendCard(sctx, bot.Profile, binding.ChatID, cardJSON)
			scancel()
		}
	} else {
		sctx, scancel := context.WithTimeout(context.Background(), 15*time.Second)
		resultMsgID, sendErr = g.client.SendCard(sctx, bot.Profile, binding.ChatID, cardJSON)
		scancel()
	}
	if sendErr != nil {
		log.Printf("lark bot %s: send result card: %v", bot.Name, sendErr)
	} else if resultMsgID != "" {
		g.markChatInterleaved(binding.ChatID, sentThreadKey, "", execID)
		if err := g.host.SaveCard(store.LarkCard{
			Kind: "result", RefID: execID, BotID: bot.ID, ChatID: binding.ChatID,
			ReplyTo: replyTo, CardMsgID: resultMsgID, DiscussionKey: discKey, Status: outcome,
		}); err != nil {
			log.Printf("lark bot %s: save result card: %v", bot.Name, err)
		}
	}

	if truncated && strings.TrimSpace(body) != "" {
		fctx, fcancel := context.WithTimeout(context.Background(), 15*time.Second)
		g.sendLongMarkdownFile(fctx, bot, binding.ChatID, execID, body)
		fcancel()
	}
}

// OnDesktopUserMessage 处理在桌面或手机 Posse 里发给绑定讨论的用户消息：
// 按确认规则——单聊（p2p）双向全同步，群聊（group）不把桌面内部追问刷到同事群。
func (g *Gateway) OnDesktopUserMessage(discKey, body string) {
	binding, err := g.host.BindingByDiscussion(discKey)
	if err != nil || binding.ChatType != "p2p" {
		return
	}
	bot, ok := g.botByID(binding.BotID)
	if !ok || !bot.Enabled {
		return
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	card := BuildNoticeCard(bot.Name+" — "+tr("Sent from Posse"), "grey", body)
	if msgID, err := g.client.SendCard(ctx, bot.Profile, binding.ChatID, card); err == nil && msgID != "" {
		g.markChatInterleaved(binding.ChatID, "", "", "")
	}
}

// OnQuestion 当讨论里的 worker 发起 `posse task ask` 提问时，在飞书发送选项/表单卡片。
func (g *Gateway) OnQuestion(discKey, execID, questionID, agentName, question string, choices []string) {
	binding, err := g.host.BindingByDiscussion(discKey)
	if err != nil {
		return
	}
	bot, ok := g.botByID(binding.BotID)
	if !ok || !bot.Enabled {
		return
	}
	cardJSON := BuildQuestionCard(bot.ID, bot.Name, agentName, questionID, question, choices, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var cardMsgID string
	sentThreadKey := ""
	if ec, err := g.host.GetCard("exec", execID); err == nil && ec.ReplyTo != "" {
		inThread := binding.ThreadKey != ""
		cardMsgID, err = g.client.ReplyCard(ctx, bot.Profile, ec.ReplyTo, inThread, cardJSON)
		if err == nil && inThread {
			sentThreadKey = binding.ThreadKey
		}
		if err != nil {
			cardMsgID, _ = g.client.SendCard(ctx, bot.Profile, binding.ChatID, cardJSON)
		}
	} else {
		cardMsgID, _ = g.client.SendCard(ctx, bot.Profile, binding.ChatID, cardJSON)
	}
	if cardMsgID == "" {
		return
	}
	g.markChatInterleaved(binding.ChatID, sentThreadKey, "", "")
	_ = g.host.SaveCard(store.LarkCard{
		Kind: "question", RefID: questionID, BotID: bot.ID, ChatID: binding.ChatID,
		CardMsgID: cardMsgID, DiscussionKey: discKey, Status: "waiting",
	})
	g.mu.Lock()
	g.qCards[questionID] = questionMem{
		botID: bot.ID, profile: bot.Profile, botName: bot.Name,
		agentName: agentName, question: question, choices: choices, cardMsgID: cardMsgID,
	}
	g.mu.Unlock()
}

// OnQuestionAnswered 在问题被飞书、桌面或手机任意一端回答后，刷新飞书问答卡片为「已回答」。
func (g *Gateway) OnQuestionAnswered(questionID, answer string) {
	g.mu.Lock()
	qm, ok := g.qCards[questionID]
	delete(g.qCards, questionID)
	g.mu.Unlock()
	if !ok || qm.cardMsgID == "" {
		return
	}
	cardJSON := BuildQuestionCard(qm.botID, qm.botName, qm.agentName, questionID, qm.question, qm.choices, answer)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = g.client.PatchCard(ctx, qm.profile, qm.cardMsgID, cardJSON)
}

// WatchPermission 供 Daemon 的 permissionHub 调用：判断终端是否属于启用了审批的飞书绑定，若是则推送审批卡并返回 true。
func (g *Gateway) WatchPermission(req PermNotice) bool {
	binding, ok := g.host.BindingByTerminal(req.TerminalID)
	if !ok || binding.PermissionMode == PermSkip {
		return false
	}
	bot, ok := g.botByID(binding.BotID)
	if !ok || !bot.Enabled {
		return false
	}
	wsLabel := g.workspaceLabel(binding.WorkspaceID)
	cardJSON := BuildPermissionCard(bot.ID, bot.Name, req.Agent, wsLabel, req.ID, req.Tool, req.Summary, req.RuleLabel, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cardMsgID, err := g.client.SendCard(ctx, bot.Profile, binding.ChatID, cardJSON)
	if err != nil || cardMsgID == "" {
		log.Printf("lark bot %s: send permission card: %v", bot.Name, err)
		return false
	}
	// 审批卡通过 SendCard 发在会话主流中，按实际落点标记主流
	g.markChatInterleaved(binding.ChatID, "", "", "")
	_ = g.host.SaveCard(store.LarkCard{
		Kind: "perm", RefID: req.ID, BotID: bot.ID, ChatID: binding.ChatID,
		CardMsgID: cardMsgID, DiscussionKey: binding.DiscussionKey, Status: "waiting",
	})
	g.mu.Lock()
	g.activePerms[req.ID] = &activePerm{
		botID: bot.ID, profile: bot.Profile, botName: bot.Name,
		agentName: req.Agent, wsLabel: wsLabel, tool: req.Tool, summary: req.Summary, ruleLabel: req.RuleLabel, cardMsgID: cardMsgID,
	}
	g.mu.Unlock()
	return true
}

// PermissionResolved 在审批被飞书、手机、桌面键入或超时解决后刷新飞书审批卡片。
func (g *Gateway) PermissionResolved(permID, behavior, source string) {
	g.mu.Lock()
	ap, ok := g.activePerms[permID]
	delete(g.activePerms, permID)
	g.mu.Unlock()
	if !ok || ap.cardMsgID == "" {
		return
	}
	cardJSON := BuildPermissionCard(ap.botID, ap.botName, ap.agentName, ap.wsLabel, permID, ap.tool, ap.summary, ap.ruleLabel, behavior, source)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = g.client.PatchCard(ctx, ap.profile, ap.cardMsgID, cardJSON)
}

func (g *Gateway) replyNotice(ctx context.Context, bot BotConfig, msgID string, inThread bool, title, template, body string) {
	card := BuildNoticeCard(title, template, body)
	_, _ = g.client.ReplyCard(ctx, bot.Profile, msgID, inThread, card)
}

func (g *Gateway) markChatInterleaved(chatID, threadKey, rootID, excludeExecID string) {
	if chatID == "" {
		return
	}
	g.mu.Lock()
	g.markChatInterleavedLocked(chatID, threadKey, rootID, excludeExecID)
	g.mu.Unlock()
}

func (g *Gateway) markChatInterleavedLocked(chatID, threadKey, rootID, excludeExecID string) {
	for id, ac := range g.activeCards {
		if id == excludeExecID || ac.chatID != chatID {
			continue
		}
		if ac.inThread {
			if ac.threadKey != "" && (threadKey == ac.threadKey || (rootID != "" && rootID == ac.threadKey)) {
				ac.interleaved = true
			}
			continue
		}
		if threadKey == "" {
			ac.interleaved = true
		}
	}
}

func (g *Gateway) canUpdateProgressInPlace(bot BotConfig, binding store.LarkBinding, ac *activeCard, progressMsgID string, inThread bool, threadKey string) bool {
	if progressMsgID == "" {
		return false
	}
	g.mu.Lock()
	interleaved := ac != nil && ac.interleaved
	g.mu.Unlock()
	if interleaved {
		return false
	}
	// 兜底向飞书查最新未撤回消息，覆盖未开全群消息权限的群聊普通发言及 daemon 重启场景
	qctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var msgs []FetchedMessage
	var err error
	checked := false
	if inThread {
		if tf, ok := g.client.(threadMessageFetcher); ok && threadKey != "" {
			msgs, err = tf.ListRecentThreadMessages(qctx, bot.Profile, threadKey, 5)
			checked = err == nil
		}
	} else if mf, ok := g.client.(messageContextFetcher); ok && binding.ChatID != "" {
		msgs, err = mf.ListRecentMessages(qctx, bot.Profile, binding.ChatID, 5)
		checked = err == nil
	}
	// 远端查询期间可能并发收到了本地插队消息，再次在锁内复核
	g.mu.Lock()
	interleaved = ac != nil && ac.interleaved
	g.mu.Unlock()
	if interleaved {
		return false
	}
	if checked {
		for _, m := range msgs {
			if m.Deleted || m.MessageID == "" {
				continue
			}
			return m.MessageID == progressMsgID
		}
	}
	// 群聊若远端查询明确报错（如无历史消息权限），可能漏看同事未 @ 的发言，保守发新卡；
	// 单聊机器人必收全部消息，本地未插队即可原地更新。
	if err != nil && binding.ChatType == "group" {
		return false
	}
	return ac != nil
}

func (g *Gateway) workspaceLabel(wsID string) string {
	for _, w := range g.host.Workspaces() {
		if w.ID == wsID {
			return w.Label
		}
	}
	return wsID
}

func (g *Gateway) sendLongMarkdownFile(ctx context.Context, bot BotConfig, chatID, execID, body string) {
	dir, err := os.MkdirTemp("", "posse-lark-md-*")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "reply-"+execID+".md")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		return
	}
	_ = g.client.SendFile(ctx, bot.Profile, chatID, p)
}

func (g *Gateway) downloadEventAttachments(ctx context.Context, bot BotConfig, ev MessageEvent) ([]string, string) {
	if ev.MessageType != "image" && ev.MessageType != "file" {
		return nil, ""
	}
	var raw map[string]any
	if len(ev.RawContent) > 0 {
		_ = json.Unmarshal(ev.RawContent, &raw)
	}
	if raw == nil {
		_ = json.Unmarshal([]byte(ev.Content), &raw)
	}
	key, _ := raw["image_key"].(string)
	kind := "image"
	if key == "" {
		key, _ = raw["file_key"].(string)
		kind = "file"
	}
	if key == "" {
		return nil, ""
	}
	dir, err := os.MkdirTemp("", "posse-lark-att-*")
	if err != nil {
		return nil, ""
	}
	p, err := g.client.DownloadResource(ctx, bot.Profile, ev.MsgID(), key, kind, dir)
	if err != nil || p == "" {
		_ = os.RemoveAll(dir)
		return nil, ""
	}
	return []string{p}, dir
}

func (g *Gateway) fetchParentContext(ctx context.Context, bot BotConfig, parentID string) (block string, atts []string, tempDir string) {
	fetcher, ok := g.client.(messageContextFetcher)
	if !ok || parentID == "" {
		return "", nil, ""
	}
	dir, err := os.MkdirTemp("", "posse-lark-parent-*")
	if err != nil {
		dir = ""
	}
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	msg, err := fetcher.GetMessage(fctx, bot.Profile, parentID, dir)
	cancel()
	if err != nil || msg.Deleted {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		return "", nil, ""
	}
	for _, r := range msg.Resources {
		if r.Error || r.LocalPath == "" {
			continue
		}
		if st, err := os.Stat(r.LocalPath); err == nil && !st.IsDir() {
			atts = append(atts, r.LocalPath)
		}
	}
	content := strings.TrimSpace(msg.Content)
	if len(content) > 6000 {
		content = truncateUTF8(content, 6000) + "\n…"
	}
	if content == "" && len(atts) == 0 {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		return "", nil, ""
	}
	sender := strings.TrimSpace(msg.Sender.Name)
	if sender == "" {
		sender = msg.Sender.ID
	}
	head := "[Quoted message " + parentID
	if sender != "" {
		head += " · " + sender
	}
	if msg.CreateTime != "" {
		head += " · " + msg.CreateTime
	}
	head += "]"
	if content == "" {
		content = "(attachment)"
	}
	return head + "\n" + content, atts, dir
}

func (g *Gateway) fetchGroupContext(ctx context.Context, bot BotConfig, chatID, currentMsgID, parentID string, count int) string {
	fetcher, ok := g.client.(messageContextFetcher)
	if !ok || chatID == "" || count <= 0 {
		return ""
	}
	reqLimit := count + 4
	if reqLimit > 50 {
		reqLimit = 50
	}
	fctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	msgs, err := fetcher.ListRecentMessages(fctx, bot.Profile, chatID, reqLimit)
	cancel()
	if err != nil || len(msgs) == 0 {
		return ""
	}
	// API 返回按时间倒序（最新在前）：先过滤再反转为时间正序
	var kept []FetchedMessage
	for _, m := range msgs {
		if m.Deleted || m.MessageID == currentMsgID || (parentID != "" && m.MessageID == parentID) {
			continue
		}
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		kept = append(kept, m)
		if len(kept) >= count {
			break
		}
	}
	if len(kept) == 0 {
		return ""
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Recent group messages (%d)]", len(kept))
	total := 0
	for _, m := range kept {
		sender := strings.TrimSpace(m.Sender.Name)
		if sender == "" {
			sender = m.Sender.ID
		}
		if sender == "" {
			sender = m.Sender.SenderType
		}
		text := strings.TrimSpace(m.Content)
		if m.MsgType == "interactive" && strings.HasPrefix(text, "{") {
			text = summarizeInteractiveJSON(text)
			if text == "" {
				continue
			}
		}
		if len(text) > 800 {
			text = truncateUTF8(text, 800) + "…"
		}
		line := "\n- "
		if m.CreateTime != "" {
			line += "[" + m.CreateTime + "] "
		}
		if sender != "" {
			line += sender + ": "
		}
		line += text
		if total+len(line) > 12000 {
			break
		}
		b.WriteString(line)
		total += len(line)
	}
	return b.String()
}

func summarizeInteractiveJSON(raw string) string {
	var card struct {
		Header struct {
			Title struct {
				Content string `json:"content"`
			} `json:"title"`
		} `json:"header"`
		Title string `json:"title"`
	}
	if json.Unmarshal([]byte(raw), &card) != nil {
		return ""
	}
	title := strings.TrimSpace(card.Header.Title.Content)
	if title == "" {
		title = strings.TrimSpace(card.Title)
	}
	if title == "" {
		return ""
	}
	return "[Card] " + title
}
