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
	Model      string
	Effort     string
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
	Model      string
	Effort     string
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

// chatOverrideUpdater 由 Daemon 实现，在统一配置锁下按群增量更新覆写并同步活跃绑定。
type chatOverrideUpdater interface {
	MutateChatOverride(botID, chatID string, mutate func(*ChatOverride)) (BotConfig, error)
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
	cfg                 BotConfig
	cancel              context.CancelFunc
	mu                  sync.Mutex
	running             bool
	msgReady            bool
	cardReady           bool
	lastErr             string
	ident               BotIdentity
	identTriedAt        time.Time
	identDone           chan struct{}
	pendingIdentRetries int
}

const (
	pendingAttachTTL         = 15 * time.Minute
	maxStagedAttachments     = 20
	defaultInboundDebounce   = 1500 * time.Millisecond
	maxInboundDebounce       = 6 * time.Second
	botIdentityRetryAfter    = 30 * time.Second
	defaultIdentityRetryWait = 3 * time.Second
	maxIdentityRetryAttempts = 2
	maxPendingIdentityEvents = 32
)

type stagedKey struct {
	botID     string
	chatID    string
	threadKey string
	senderID  string
}

type debounceBatch struct {
	bot       BotConfig
	ev        MessageEvent
	threadKey string
	inThread  bool
	bodies    []string
	firstAt   time.Time
	seq       uint64
	timer     *time.Timer
}

type stagedAttachment struct {
	msgID    string
	fileKey  string
	kind     string // "image" | "file"
	fileName string // 原始文件名（可选）
	rootID   string
}

func (a stagedAttachment) displayLabel() string {
	if a.fileName != "" {
		return a.fileName
	}
	if a.kind == "image" {
		return tr("Image")
	}
	return tr("File")
}

type stagedBatch struct {
	id        string
	botID     string
	profile   string
	botName   string
	chatID    string
	chatType  string
	threadKey string
	inThread  bool
	senderID  string
	updatedAt time.Time
	items     []stagedAttachment

	// cardMu 串行化同一暂存批次的发卡、更新与收拢，防止并发事件刷出多张卡或留下孤儿卡。
	cardMu       sync.Mutex
	cardMsgID    string
	closedStatus string
}

// Gateway 管理全部已启用的飞书机器人长连接、事件路由、卡片节流更新和三端同步。
type Gateway struct {
	client             Client
	host               Host
	home               string
	nowFn              func() time.Time // 单测可注入时钟
	debounceWindow     time.Duration    // 连续短文本入站防抖静默窗；Start 默认开启 1.5s，单测默认 0 保持同步
	debounceMax        time.Duration    // 单次防抖最大等待上限
	identityRetryDelay time.Duration    // 机器人身份暂时不可用时不确定 @ 消息的重判间隔；默认 3s

	mu          sync.Mutex
	parentCtx   context.Context
	cfg         Config
	runners     map[string]*botRunner
	activeCards map[string]*activeCard       // execID -> 正在轮询更新的进度卡
	activePerms map[string]*activePerm       // permID -> 正在等待审批的权限卡
	qCards      map[string]questionMem       // questionID -> 提问卡片上下文
	staged      map[stagedKey]*stagedBatch   // 单聊纯附件暂存，等待后续指令一并提交
	debounced   map[stagedKey]*debounceBatch // 连续短文本入站防抖缓存
	loopCancel  context.CancelFunc
}

type questionMem struct {
	botID     string
	chatID    string
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
		debounceMax: maxInboundDebounce,
		runners:     map[string]*botRunner{},
		activeCards: map[string]*activeCard{},
		activePerms: map[string]*activePerm{},
		qCards:      map[string]questionMem{},
		staged:      map[stagedKey]*stagedBatch{},
		debounced:   map[stagedKey]*debounceBatch{},
	}
}

func (g *Gateway) now() time.Time {
	if g.nowFn != nil {
		return g.nowFn()
	}
	return time.Now()
}

// Start 启动配置中的已启用机器人并开启 2.5 秒进度卡节流刷新循环。
func (g *Gateway) Start(ctx context.Context, cfg Config) {
	g.mu.Lock()
	g.parentCtx = ctx
	if g.debounceWindow == 0 {
		g.debounceWindow = defaultInboundDebounce
	}
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
	// 若尚未配置主人 open_id，尝试从本机 lark-cli 自动探测（仅更新内存，不绕过 daemon 配置锁写盘）
	if cfg.OwnerOpenID == "" && g.client != nil {
		profile := ""
		if len(cfg.Bots) > 0 {
			profile = cfg.Bots[0].Profile
		}
		pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if id, err := g.client.OwnerOpenID(pctx, profile); err == nil && id != "" {
			cfg.OwnerOpenID = id
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
	debounced := g.debounced
	g.debounced = map[stagedKey]*debounceBatch{}
	g.mu.Unlock()
	for _, b := range debounced {
		if b.timer != nil {
			b.timer.Stop()
		}
	}
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
	// 异步预热机器人真实身份，不阻塞消息与卡片长连接订阅；
	// 若首条群消息早于预热完成到达，refreshBotIdentity 的 singleflight 会无缝等待本次预热结束。
	go g.refreshBotIdentity(ctx, r, true)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		g.consumeLoop(ctx, r, "im.message.receive_v1", true, func(cfg BotConfig, line []byte) {
			g.handleMessageRaw(ctx, cfg, line)
		})
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

func (g *Gateway) handleMessageRaw(ctx context.Context, bot BotConfig, raw []byte) {
	var ev MessageEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return
	}
	g.HandleMessage(ctx, bot, ev)
}

// HandleMessage 处理一条飞书 IM 消息（单测可直接调用）。
func (g *Gateway) HandleMessage(ctx context.Context, bot BotConfig, ev MessageEvent) {
	g.handleMessage(ctx, bot, ev, true)
}

func (g *Gateway) handleMessage(ctx context.Context, bot BotConfig, ev MessageEvent, allowIdentityRetry bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}
	bot = NormalizeBot(bot)
	msgID := ev.MsgID()
	if msgID == "" || ev.ChatID == "" || ev.SenderType == "app" || ev.SenderType == "bot" {
		return
	}
	latest, hadConfig := g.botByID(bot.ID)
	if hadConfig {
		if !latest.Enabled || latest.Profile != bot.Profile {
			return
		}
		bot = latest
	}
	if ev.ParentID == "" && ev.ReplyTo != "" {
		ev.ParentID = ev.ReplyTo
	}
	// 同会话/话题在执行期间收到任何新消息（含未 @ 机器人的群聊发言），都视为已有插队消息
	g.markChatInterleaved(ev.ChatID, ev.ThreadID, ev.RootID, "")
	owner := g.Config().OwnerOpenID
	effBot, noMention := bot.EffectiveForChat(ev.ChatID)
	if !effBot.AllowsTrigger(ev.SenderID, owner, ev.ChatID) {
		return
	}
	// 群聊 @ 判定始终按机器人真实身份（不以分身别名匹配，避免与同名同事冲突）；
	// 开启免 @ 的群仍忽略显式 @ 其他同事的发言，防止群内有第三人时误抢话。
	ident := g.botIdentity(ctx, bot.ID, bot.Profile)
	if ctx.Err() != nil {
		return
	}
	owner = g.Config().OwnerOpenID
	if hadConfig {
		latest, ok := g.botByID(bot.ID)
		if !ok || !latest.Enabled || latest.Profile != bot.Profile {
			return
		}
		bot = latest
		effBot, noMention = bot.EffectiveForChat(ev.ChatID)
		if !effBot.AllowsTrigger(ev.SenderID, owner, ev.ChatID) {
			return
		}
	}
	if ev.ChatType == "group" {
		matched, uncertain := evaluateGroupMention(bot, ident, ev)
		if !noMention && !matched {
			if uncertain && allowIdentityRetry {
				g.scheduleIdentityRetry(ctx, bot, ev)
			}
			return
		}
		if noMention && !matched && isMentioningOthersOnly(bot, ident, ev) {
			if uncertain && allowIdentityRetry {
				g.scheduleIdentityRetry(ctx, bot, ev)
			}
			return
		}
	}
	if ok, err := g.host.MarkEvent(bot.ID, "msg:"+msgID); err != nil || !ok {
		return
	}

	threadKey := computeThreadKey(effBot.SessionMode, ev)
	inThread := ev.ChatType == "group" && effBot.SessionMode == SessionThread
	body := stripLeadingMentions(ev.Content, ev.Mentions, bot.Name, ident.BotAppName)
	isOwner := owner != "" && ev.SenderID == owner

	// 快捷命令：/agent、/ws、/new、/status、/stop、/mention、/role、/help
	if strings.HasPrefix(body, "/") {
		if g.handleCommand(ctx, bot, effBot, noMention, ev, threadKey, inThread, body, isOwner) {
			return
		}
	}

	// 单聊或免 @ 群收到纯文件/图片消息时，不立即触发无指令执行，先暂存等待补充指令或点击「立即发送」
	if (ev.ChatType != "group" || noMention) && ev.ParentID == "" {
		if meta, ok := parseAttachmentMeta(ev); ok {
			g.stageAttachment(ctx, effBot, ev, threadKey, inThread, meta)
			return
		}
	}

	// 连续短文本入站防抖：将静默窗内同一发送者的多条纯文本合并为一次 Agent 回合
	if g.getDebounceWindow() > 0 && isDebounceableTextEvent(ev, body) {
		g.enqueueTextDebounce(effBot, ev, threadKey, inThread, body)
		return
	}

	// 若同发送者有尚未触发的防抖文本，前置合并以保持消息严格有序
	if prev := g.takeDebounced(stagedKey{
		botID:     effBot.ID,
		chatID:    ev.ChatID,
		threadKey: threadKey,
		senderID:  ev.SenderID,
	}); prev != nil {
		prevBody := strings.Join(prev.bodies, "\n")
		if prevBody != "" {
			if body != "" {
				body = prevBody + "\n" + body
			} else {
				body = prevBody
			}
		}
		if ev.ParentID == "" && prev.ev.ParentID != "" {
			ev.ParentID = prev.ev.ParentID
		}
	}

	g.dispatchMessage(ctx, effBot, ev, threadKey, inThread, body)
}

func (g *Gateway) dispatchMessage(ctx context.Context, bot BotConfig, ev MessageEvent, threadKey string, inThread bool, body string) {
	msgID := ev.MsgID()

	// 下载消息携带的图片/文件附件（若有），并合并此前暂存的同会话附件
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
	stagedBatch := g.takeStaged(ctx, stagedKey{
		botID:     bot.ID,
		chatID:    ev.ChatID,
		threadKey: threadKey,
		senderID:  ev.SenderID,
	})
	parentCoveredByStaged := false
	if stagedBatch != nil {
		stagedAtts, stagedDir, downloadedMsgIDs := g.downloadAttachmentsList(ctx, bot, stagedBatch.items)
		if stagedDir != "" {
			cleanupDirs = append(cleanupDirs, stagedDir)
		}
		atts = append(stagedAtts, atts...)
		if ev.ParentID != "" && downloadedMsgIDs[ev.ParentID] {
			parentCoveredByStaged = true
		}
	}
	if body == "" && len(atts) == 0 && ev.ParentID == "" {
		return
	}

	// 补齐群聊近期上下文（可选）及被引用父消息的正文与附件（已由暂存覆盖的纯附件父消息不再重复拉取）
	var contextBlocks []string
	if ev.ChatType == "group" && bot.GroupContextCount > 0 {
		if block := g.fetchGroupContext(ctx, bot, ev.ChatID, msgID, ev.ParentID, bot.GroupContextCount); block != "" {
			contextBlocks = append(contextBlocks, block)
		}
	}
	if ev.ParentID != "" && !parentCoveredByStaged {
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
		if stagedBatch != nil {
			g.closeStagedCard(ctx, stagedBatch, "cancelled")
		}
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
		if stagedBatch != nil {
			g.closeStagedCard(ctx, stagedBatch, "cancelled")
		}
		g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
		return
	}
	if stagedBatch != nil {
		g.closeStagedCard(ctx, stagedBatch, "submitted")
	}

	wsLabel := g.workspaceLabel(binding.WorkspaceID)
	prevExecID := g.queuedBehindExecID(binding.DiscussionKey, execID)
	agentLabel := g.execAgentLabel(execID, binding.Agent, bot.Model, bot.Effort)
	cardJSON := BuildProgressCard(bot.ID, bot.Name, agentLabel, wsLabel, execID, tr("Queued"), "0s", "", "", "", 0, prevExecID)
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

func (g *Gateway) refreshBotIdentity(ctx context.Context, r *botRunner, force bool) BotIdentity {
	if r == nil {
		return BotIdentity{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	cur := r.ident
	// 已有完整真实身份（OpenID、AppID 与应用显示名）则无需重复请求。
	complete := cur.BotOpenID != "" && cur.BotAppID != "" && cur.BotAppName != ""
	if complete {
		r.mu.Unlock()
		return cur
	}
	if done := r.identDone; done != nil {
		r.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return cur
		}
		r.mu.Lock()
		cur = r.ident
		r.mu.Unlock()
		return cur
	}
	if !force && !r.identTriedAt.IsZero() && time.Since(r.identTriedAt) < botIdentityRetryAfter {
		r.mu.Unlock()
		return cur
	}
	bip, ok := g.client.(botIdentityProvider)
	if !ok {
		r.mu.Unlock()
		return cur
	}
	prof := r.cfg.Profile
	botName := r.cfg.Name
	done := make(chan struct{})
	r.identDone = done
	r.mu.Unlock()

	ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
	id, err := bip.BotIdentity(ictx, prof)
	cancel()

	r.mu.Lock()
	if err == nil {
		if id.BotOpenID != "" {
			r.ident.BotOpenID = id.BotOpenID
		}
		if id.BotAppID != "" {
			r.ident.BotAppID = id.BotAppID
		}
		if id.BotAppName != "" {
			r.ident.BotAppName = id.BotAppName
		}
		r.identTriedAt = time.Now()
	} else if ctx.Err() == nil {
		r.identTriedAt = time.Now()
		log.Printf("lark bot %s (%s): fetch bot identity: %v", botName, prof, err)
	}
	r.identDone = nil
	close(done)
	cur = r.ident
	r.mu.Unlock()
	return cur
}

func (g *Gateway) botIdentity(ctx context.Context, botID, profile string) BotIdentity {
	g.mu.Lock()
	r := g.runners[botID]
	g.mu.Unlock()
	if r == nil {
		return BotIdentity{}
	}
	r.mu.Lock()
	sameProfile := profile == "" || r.cfg.Profile == profile
	r.mu.Unlock()
	if !sameProfile {
		return BotIdentity{}
	}
	return g.refreshBotIdentity(ctx, r, false)
}

func (g *Gateway) identityRetryWait() time.Duration {
	if g.identityRetryDelay > 0 {
		return g.identityRetryDelay
	}
	return defaultIdentityRetryWait
}

// scheduleIdentityRetry 在机器人真实身份暂时不可用且无法确定群消息是否 @ 本机器人时，
// 对该条一次性入站事件做有界延迟重判，避免因瞬时 CLI 抖动永久丢失合法 @。
func (g *Gateway) scheduleIdentityRetry(ctx context.Context, bot BotConfig, ev MessageEvent) {
	if _, ok := g.client.(botIdentityProvider); !ok {
		return
	}
	g.mu.Lock()
	r := g.runners[bot.ID]
	g.mu.Unlock()
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.cfg.Profile != bot.Profile || r.pendingIdentRetries >= maxPendingIdentityEvents {
		r.mu.Unlock()
		return
	}
	r.pendingIdentRetries++
	r.mu.Unlock()

	wait := g.identityRetryWait()
	go func() {
		defer func() {
			r.mu.Lock()
			r.pendingIdentRetries--
			r.mu.Unlock()
		}()
		for attempt := 0; attempt < maxIdentityRetryAttempts; attempt++ {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if latest, ok := g.botByID(bot.ID); ok && (!latest.Enabled || latest.Profile != bot.Profile) {
				return
			}
			ident := g.refreshBotIdentity(ctx, r, true)
			if ctx.Err() != nil {
				return
			}
			matched, uncertain := evaluateGroupMention(bot, ident, ev)
			if matched || !uncertain {
				g.handleMessage(ctx, bot, ev, false)
				return
			}
		}
	}()
}

// evaluateGroupMention 区分「命中」「明确未命中」与「因本机身份字段缺失而暂时不确定」。
func evaluateGroupMention(bot BotConfig, ident BotIdentity, ev MessageEvent) (matched bool, uncertain bool) {
	if isMentioningBot(bot, ident, ev) {
		return true, false
	}
	if len(ev.Mentions) > 0 {
		for _, m := range ev.Mentions {
			hasID := false
			for _, mid := range []string{strings.TrimSpace(m.ID), strings.TrimSpace(m.OpenID)} {
				if mid == "" {
					continue
				}
				hasID = true
				if strings.HasPrefix(mid, "cli_") {
					if ident.BotAppID == "" {
						return false, true
					}
				} else if ident.BotOpenID == "" {
					return false, true
				}
			}
			if !hasID && ident.BotAppName == "" {
				return false, true
			}
		}
		return false, false
	}
	if hasAnyTextMention(ev.Content) && ident.BotAppName == "" {
		return false, true
	}
	return false, false
}

func isMentioningBot(bot BotConfig, ident BotIdentity, ev MessageEvent) bool {
	if len(ev.Mentions) > 0 {
		for _, m := range ev.Mentions {
			ids := make([]string, 0, 2)
			if id := strings.TrimSpace(m.ID); id != "" {
				ids = append(ids, id)
			}
			if oid := strings.TrimSpace(m.OpenID); oid != "" && (len(ids) == 0 || oid != ids[0]) {
				ids = append(ids, oid)
			}
			if len(ids) > 0 {
				// 任一结构化 ID 命中本机器人即直接触发；否则仅当该条 mention 所属命名空间的本机 ID 未知时才允许按名回退，
				// 防止同消息里混入其他命名空间（如同时 @ 同名 cli_other 和无关 ou_colleague）破坏已知 ID 的否决权。
				allNamespacesKnown := true
				for _, mid := range ids {
					if (ident.BotOpenID != "" && mid == ident.BotOpenID) || (ident.BotAppID != "" && mid == ident.BotAppID) {
						return true
					}
					if strings.HasPrefix(mid, "cli_") {
						if ident.BotAppID == "" {
							allNamespacesKnown = false
						}
					} else if ident.BotOpenID == "" {
						allNamespacesKnown = false
					}
				}
				if allNamespacesKnown {
					continue
				}
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
		if containsMentionToken(content, name) {
			return true
		}
	}
	return false
}

// isMentioningOthersOnly 判断消息是否显式 @ 了其他同事而没有 @ 本机器人（用于免 @ 群避免误抢同事对话）。
func isMentioningOthersOnly(bot BotConfig, ident BotIdentity, ev MessageEvent) bool {
	if len(ev.Mentions) > 0 {
		return !isMentioningBot(bot, ident, ev)
	}
	if hasAnyTextMention(ev.Content) && !isMentioningBot(bot, ident, ev) {
		return true
	}
	return false
}

func isMentionWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

// containsMentionToken 匹配独立的 @name / ＠name（避免 @Ops 误命中 @OpsSupport，也排除邮箱前缀）。
func containsMentionToken(content, name string) bool {
	if name == "" || content == "" {
		return false
	}
	for _, prefix := range []string{"@", "\uff20"} {
		needle := prefix + name
		off := 0
		for {
			idx := strings.Index(content[off:], needle)
			if idx < 0 {
				break
			}
			pos := off + idx
			end := pos + len(needle)
			prevOK := pos == 0 || !isMentionWordByte(content[pos-1])
			nextOK := end == len(content) || !isMentionWordByte(content[end])
			if prevOK && nextOK {
				return true
			}
			off = pos + len(prefix)
		}
	}
	return false
}

// hasAnyTextMention 判断纯文本中是否包含任意位置的 @某人 / ＠某人（排除 a@b.com 邮箱）。
func hasAnyTextMention(content string) bool {
	for i := 0; i < len(content); i++ {
		prefixLen := 0
		if content[i] == '@' {
			prefixLen = 1
		} else if strings.HasPrefix(content[i:], "\uff20") {
			prefixLen = len("\uff20")
		}
		if prefixLen == 0 {
			continue
		}
		if i > 0 && isMentionWordByte(content[i-1]) {
			continue
		}
		after := i + prefixLen
		if after < len(content) && content[after] != ' ' && content[after] != '\t' && content[after] != '\n' && content[after] != '\r' {
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

func (g *Gateway) handleCommand(ctx context.Context, rawBot, bot BotConfig, noMention bool, ev MessageEvent, threadKey string, inThread bool, body string, isOwner bool) bool {
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
			"`/mention on|off` — " + tr("Require @mention or allow direct messages in this group"),
			"`/role <text|clear|inherit>` — " + tr("Set, clear, or inherit this group's role prompt"),
		}, "\n")
		g.replyNotice(ctx, bot, msgID, inThread, bot.Name+" — "+tr("Commands"), "blue", help)
		return true

	case "/agent":
		if !isOwner {
			return true
		}
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
		if !isOwner {
			return true
		}
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
		if !isOwner {
			return true
		}
		skey := stagedKey{
			botID:     bot.ID,
			chatID:    ev.ChatID,
			threadKey: threadKey,
			senderID:  ev.SenderID,
		}
		g.cancelDebounced(skey)
		if batch := g.takeStaged(ctx, skey); batch != nil {
			g.closeStagedCard(ctx, batch, "cancelled")
		}
		if err := g.host.ResetBinding(bot.ID, ev.ChatID, threadKey); err != nil {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
			return true
		}
		g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "green", tr("Started a fresh session; your next message will begin a new discussion."))
		return true

	case "/stop":
		if !isOwner {
			return true
		}
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

	case "/mention":
		if !isOwner {
			return true
		}
		if ev.ChatType != "group" {
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "grey", tr("This command only applies to group chats."))
			return true
		}
		arg := strings.ToLower(strings.TrimSpace(rest))
		if arg != "on" && arg != "off" {
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "grey", tr("Usage: `/mention on` or `/mention off`"))
			return true
		}
		updater, ok := g.host.(chatOverrideUpdater)
		if !ok {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", tr("Group overrides are not supported."))
			return true
		}
		wantNoMention := arg == "off"
		if _, err := updater.MutateChatOverride(rawBot.ID, ev.ChatID, func(c *ChatOverride) {
			c.NoMention = wantNoMention
		}); err != nil {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
			return true
		}
		if wantNoMention {
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "green", tr("Direct mode enabled for this group (no @mention needed)."))
		} else {
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "green", tr("This group now requires @mention to trigger."))
		}
		return true

	case "/role":
		if !isOwner {
			return true
		}
		if ev.ChatType != "group" {
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "grey", tr("This command only applies to group chats."))
			return true
		}
		if rest == "" {
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "grey", tr("Usage: `/role <prompt>`, `/role clear`, or `/role inherit`"))
			return true
		}
		updater, ok := g.host.(chatOverrideUpdater)
		if !ok {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", tr("Group overrides are not supported."))
			return true
		}
		low := strings.ToLower(rest)
		if _, err := updater.MutateChatOverride(rawBot.ID, ev.ChatID, func(c *ChatOverride) {
			switch low {
			case "inherit":
				c.Role = nil
			case "clear":
				empty := ""
				c.Role = &empty
			default:
				v := rest
				c.Role = &v
			}
		}); err != nil {
			g.replyNotice(ctx, bot, msgID, inThread, tr("Error"), "red", err.Error())
			return true
		}
		switch low {
		case "inherit":
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "green", tr("Group role reset to inherit bot default; takes effect on next message."))
		case "clear":
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "green", tr("Group role cleared; takes effect on next message."))
		default:
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name, "green", tr("Group role updated; takes effect on next message."))
		}
		return true

	case "/status":
		mentionMode := tr("Required")
		if ev.ChatType != "group" || noMention {
			mentionMode = tr("Direct (no @)")
		}
		roleState := tr("Inherited")
		if ov, ok := rawBot.ChatOverrideOf(ev.ChatID); ok && ov.Role != nil {
			if *ov.Role == "" {
				roleState = tr("None")
			} else {
				roleState = tr("Custom")
			}
		}
		binding, err := g.host.ActiveBinding(bot.ID, ev.ChatID, threadKey)
		if err != nil {
			lines := []string{
				fmt.Sprintf("**%s**: `%s`", tr("Chat ID"), ev.ChatID),
				fmt.Sprintf("**%s**: `%s`", tr("Agent"), bot.Agent),
				fmt.Sprintf("**%s**: `%s`", tr("Workspace"), g.workspaceLabel(bot.WorkspaceID)),
				fmt.Sprintf("**%s**: `%s`", tr("Permission"), bot.PermissionMode),
			}
			if ev.ChatType == "group" {
				lines = append(lines,
					fmt.Sprintf("**%s**: `%s`", tr("Mention"), mentionMode),
					fmt.Sprintf("**%s**: `%s`", tr("Role"), roleState),
				)
			}
			g.replyNotice(ctx, bot, msgID, inThread, bot.Name+" — "+tr("Status"), "blue", strings.Join(lines, "\n"))
			return true
		}
		lines := []string{
			fmt.Sprintf("**%s**: `%s`", tr("Chat ID"), ev.ChatID),
			fmt.Sprintf("**%s**: `%s`", tr("Agent"), binding.Agent),
			fmt.Sprintf("**%s**: `%s`", tr("Workspace"), g.workspaceLabel(binding.WorkspaceID)),
			fmt.Sprintf("**%s**: `%s`", tr("Permission"), binding.PermissionMode),
			fmt.Sprintf("**%s**: `%s`", tr("Discussion"), binding.DiscussionKey),
		}
		if ev.ChatType == "group" {
			lines = append(lines,
				fmt.Sprintf("**%s**: `%s`", tr("Mention"), mentionMode),
				fmt.Sprintf("**%s**: `%s`", tr("Role"), roleState),
			)
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

	case "exec_interrupt_prev":
		if !isOwner {
			return
		}
		snap, ok := g.host.ExecutionSnapshot(val["execId"])
		if !ok || snap.State != "queued" || snap.Discussion == "" {
			return
		}
		wantPrev := strings.TrimSpace(val["prevExecId"])
		if prev, ok := g.host.ActiveExecutionFor(snap.Discussion); ok && prev.ID != "" && prev.ID != snap.ID {
			if wantPrev != "" && prev.ID != wantPrev {
				return
			}
			_ = g.host.StopExecution(prev.ID)
		}

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
				effBot := bot
				if binding, berr := g.host.BindingByDiscussion(snap.Discussion); berr == nil {
					threadKey = binding.ThreadKey
					inThread = threadKey != ""
					effBot, _ = bot.EffectiveForChat(binding.ChatID)
				}
				prevExecID := g.queuedBehindExecID(snap.Discussion, newID)
				agentLabel := FormatModelLabel(snap.Agent, snap.Model, snap.Effort)
				cardJSON := BuildProgressCard(effBot.ID, effBot.Name, agentLabel, snap.Workspace, newID, tr("Queued"), "0s", "", "", "", 0, prevExecID)
				// 重试保留原失败结果卡不覆盖，新建一张进度卡，避免指向原结果卡的引用跳到下一轮。
				var cardMsgID string
				var sendErr error
				replyTarget := origReplyTo
				if replyTarget == "" {
					replyTarget = ev.MessageID
				}
				if replyTarget != "" {
					cardMsgID, sendErr = g.client.ReplyCard(ctx, bot.Profile, replyTarget, inThread, cardJSON)
				}
				if sendErr != nil || cardMsgID == "" {
					cardMsgID, sendErr = g.client.SendCard(ctx, bot.Profile, ev.ChatID, cardJSON)
					if sendErr == nil && cardMsgID != "" {
						inThread = false
						threadKey = ""
					}
				}
				if sendErr != nil || cardMsgID == "" {
					log.Printf("lark bot %s: send retry progress card: %v", bot.Name, sendErr)
					return
				}
				g.markChatInterleaved(ev.ChatID, threadKey, "", newID)
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
		qid := val["questionId"]
		ans := strings.TrimSpace(val["choice"])
		if qid == "" || ans == "" {
			return
		}
		chatID := ev.ChatID
		g.mu.Lock()
		if qm, ok := g.qCards[qid]; ok && qm.chatID != "" {
			chatID = qm.chatID
		}
		g.mu.Unlock()
		if chatID == "" {
			if card, err := g.host.GetCard("question", qid); err == nil && card.ChatID != "" {
				chatID = card.ChatID
			}
		}
		effBot, _ := bot.EffectiveForChat(chatID)
		if !effBot.AllowsTrigger(ev.OperatorID, owner, chatID) {
			return
		}
		if err := g.host.AnswerQuestion(qid, ans); err == nil {
			g.OnQuestionAnswered(qid, ans)
		}

	case "switch_agent":
		if !isOwner {
			return
		}
		chatID := val["chatId"]
		effBot, _ := bot.EffectiveForChat(chatID)
		binding, err := g.host.ActiveBinding(bot.ID, chatID, val["threadKey"])
		if err != nil {
			if ev.MessageID != "" {
				card := BuildNoticeCard(effBot.Name, "grey", tr("This picker has expired; run the command again."))
				_ = g.client.PatchCard(ctx, effBot.Profile, ev.MessageID, card)
			}
			return
		}
		if updated, err := g.host.SwitchAgent(binding, val["agent"], ""); err == nil && ev.MessageID != "" {
			card := BuildNoticeCard(effBot.Name, "green", fmt.Sprintf("%s: `%s`", tr("Switched agent to"), updated.Agent))
			_ = g.client.PatchCard(ctx, effBot.Profile, ev.MessageID, card)
		}

	case "switch_ws":
		if !isOwner {
			return
		}
		chatID := val["chatId"]
		effBot, _ := bot.EffectiveForChat(chatID)
		binding, err := g.host.ActiveBinding(bot.ID, chatID, val["threadKey"])
		if err != nil {
			if ev.MessageID != "" {
				card := BuildNoticeCard(effBot.Name, "grey", tr("This picker has expired; run the command again."))
				_ = g.client.PatchCard(ctx, effBot.Profile, ev.MessageID, card)
			}
			return
		}
		if updated, err := g.host.SwitchWorkspace(binding, val["workspaceId"]); err == nil && ev.MessageID != "" {
			card := BuildNoticeCard(effBot.Name, "green", fmt.Sprintf("%s: `%s`", tr("Switched workspace to"), g.workspaceLabel(updated.WorkspaceID)))
			_ = g.client.PatchCard(ctx, effBot.Profile, ev.MessageID, card)
		}

	case "staged_send":
		batch, denied := g.findAndRemoveStaged(bot.ID, val["batchId"], ev.OperatorID, isOwner)
		if denied {
			return
		}
		if batch == nil {
			if ev.MessageID != "" {
				card := BuildStagedAttachmentsClosedCard(bot.Name, "expired", 0)
				_ = g.client.PatchCard(ctx, bot.Profile, ev.MessageID, card)
			}
			return
		}
		// 消费前按服务端最新机器人与群配置严格复查：机器人须仍存在且启用、发送者与点击者均被允许、且会话粒度未变更
		latestBot, ok := g.botByID(bot.ID)
		if !ok || !latestBot.Enabled {
			g.closeStagedCardWithFallback(ctx, batch, "cancelled", ev.MessageID)
			return
		}
		effBot, noMention := latestBot.EffectiveForChat(batch.chatID)
		sessionMismatch := batch.chatType == "group" && ((effBot.SessionMode == SessionThread) != batch.inThread)
		if sessionMismatch ||
			!effBot.AllowsTrigger(ev.OperatorID, owner, batch.chatID) ||
			(batch.senderID != "" && !effBot.AllowsTrigger(batch.senderID, owner, batch.chatID)) ||
			(batch.chatType == "group" && !noMention) {
			g.closeStagedCardWithFallback(ctx, batch, "cancelled", ev.MessageID)
			return
		}
		g.submitStagedBatch(ctx, effBot, batch, ev.MessageID)

	case "staged_cancel":
		batch, denied := g.findAndRemoveStaged(bot.ID, val["batchId"], ev.OperatorID, isOwner)
		if denied {
			return
		}
		if batch == nil {
			if ev.MessageID != "" {
				card := BuildStagedAttachmentsClosedCard(bot.Name, "cancelled", 0)
				_ = g.client.PatchCard(ctx, bot.Profile, ev.MessageID, card)
			}
			return
		}
		effBot, _ := bot.EffectiveForChat(batch.chatID)
		if !effBot.AllowsTrigger(ev.OperatorID, owner, batch.chatID) {
			return
		}
		g.closeStagedCardWithFallback(ctx, batch, "cancelled", ev.MessageID)
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
		prevExecID := ""
		if snap.State == "queued" {
			prevExecID = g.queuedBehindExecID(snap.Discussion, c.execID)
		}
		agentLabel := FormatModelLabel(snap.Agent, snap.Model, snap.Effort)
		cardJSON := BuildProgressCard(c.botID, c.botName, agentLabel, snap.Workspace, c.execID, snap.State, elapsed, snap.Tool, snap.ToolInput, snap.Phase, snap.Subagents, prevExecID)
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
	effBot, _ := bot.EffectiveForChat(binding.ChatID)
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
	model, effort := snap.Model, snap.Effort
	if model == "" {
		model = effBot.Model
		if effort == "" {
			effort = effBot.Effort
		}
	}
	agentLabel := FormatModelLabel(agentName, model, effort)

	cardJSON, truncated := BuildResultCard(effBot.ID, effBot.Name, agentLabel, wsLabel, execID, outcome, elapsed, snap.TraceCount, body)

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

	// 手动停止且没有正文结论时，原地把进度卡改成「已停止」后安静返回；若补丁失败则继续走下方降级发新卡，不留僵尸停止按钮。
	trimmedBody := strings.TrimSpace(body)
	inPlaceFailed := false
	if progressMsgID != "" && (outcome == "stopped" || outcome == "cancelled") && (trimmedBody == "" || trimmedBody == "stopped by the user") {
		if err := patchProgress(cardJSON, 10*time.Second); err == nil {
			if err := g.host.SaveCard(store.LarkCard{
				Kind: "result", RefID: execID, BotID: bot.ID, ChatID: binding.ChatID,
				ReplyTo: replyTo, CardMsgID: progressMsgID, DiscussionKey: discKey, Status: outcome,
			}); err != nil {
				log.Printf("lark bot %s: save stopped result card: %v", bot.Name, err)
			}
			return
		} else {
			inPlaceFailed = true
			log.Printf("lark bot %s: patch stopped card (fallback to new card): %v", bot.Name, err)
		}
	}

	// 有原进度卡时，始终优先把完整结果原地更新到原进度卡，避免聊天记录留下灰卡占位和重复大卡片；
	// 若期间插入了其他消息（或群聊因无历史权限/超时无法确认未插队），则在底部补发一条轻量纯文本引用原卡片，触发未读提醒并支持一键跳转。
	// 原地更新失败时再降级走底部的「收拢旧卡 + 发新结果卡」。
	if progressMsgID != "" && !inPlaceFailed {
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
			if !g.canUpdateProgressInPlace(bot, binding, ac, progressMsgID, inThread, threadKey) {
				nctx, ncancel := context.WithTimeout(context.Background(), 10*time.Second)
				noticeID, nerr := g.client.ReplyText(nctx, bot.Profile, progressMsgID, inThread, interleavedReplyNotice(outcome))
				ncancel()
				if nerr != nil {
					log.Printf("lark bot %s: send interleaved reply notice: %v", bot.Name, nerr)
				} else if noticeID == "" {
					log.Printf("lark bot %s: send interleaved reply notice returned empty message_id", bot.Name)
				} else {
					sentThreadKey := ""
					if inThread {
						sentThreadKey = threadKey
					}
					g.markChatInterleaved(binding.ChatID, sentThreadKey, "", execID)
				}
			}
			return
		} else {
			inPlaceFailed = true
			log.Printf("lark bot %s: patch result card in place (fallback to new card): %v", bot.Name, err)
		}
	}

	// 无原进度卡或原地更新失败时：
	// 先尽力收拢最初的进度卡（移除停止按钮），再使用独立超时把结果作为新卡片发到对话底部。
	if progressMsgID != "" {
		closedTimeout := 10 * time.Second
		if inPlaceFailed {
			closedTimeout = 5 * time.Second
		}
		closedCard := BuildProgressClosedCard(effBot.Name, agentLabel, wsLabel, outcome, elapsed, snap.TraceCount)
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
	effBot, _ := bot.EffectiveForChat(binding.ChatID)
	agentLabel := g.execAgentLabel(execID, agentName, effBot.Model, effBot.Effort)
	cardJSON := BuildQuestionCard(effBot.ID, effBot.Name, agentLabel, questionID, question, choices, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var cardMsgID string
	sentThreadKey := ""
	if ec, err := g.host.GetCard("exec", execID); err == nil && ec.ReplyTo != "" {
		inThread := binding.ThreadKey != ""
		cardMsgID, err = g.client.ReplyCard(ctx, effBot.Profile, ec.ReplyTo, inThread, cardJSON)
		if err == nil && inThread {
			sentThreadKey = binding.ThreadKey
		}
		if err != nil {
			cardMsgID, _ = g.client.SendCard(ctx, effBot.Profile, binding.ChatID, cardJSON)
		}
	} else {
		cardMsgID, _ = g.client.SendCard(ctx, effBot.Profile, binding.ChatID, cardJSON)
	}
	if cardMsgID == "" {
		return
	}
	g.markChatInterleaved(binding.ChatID, sentThreadKey, "", "")
	_ = g.host.SaveCard(store.LarkCard{
		Kind: "question", RefID: questionID, BotID: effBot.ID, ChatID: binding.ChatID,
		CardMsgID: cardMsgID, DiscussionKey: discKey, Status: "waiting",
	})
	g.mu.Lock()
	g.qCards[questionID] = questionMem{
		botID: effBot.ID, chatID: binding.ChatID, profile: effBot.Profile, botName: effBot.Name,
		agentName: agentLabel, question: question, choices: choices, cardMsgID: cardMsgID,
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
	effBot, _ := bot.EffectiveForChat(binding.ChatID)
	wsLabel := g.workspaceLabel(binding.WorkspaceID)
	model, effort := req.Model, req.Effort
	if model == "" {
		model = effBot.Model
		if effort == "" {
			effort = effBot.Effort
		}
	}
	agentLabel := FormatModelLabel(req.Agent, model, effort)
	cardJSON := BuildPermissionCard(effBot.ID, effBot.Name, agentLabel, wsLabel, req.ID, req.Tool, req.Summary, req.RuleLabel, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cardMsgID, err := g.client.SendCard(ctx, effBot.Profile, binding.ChatID, cardJSON)
	if err != nil || cardMsgID == "" {
		log.Printf("lark bot %s: send permission card: %v", effBot.Name, err)
		return false
	}
	// 审批卡通过 SendCard 发在会话主流中，按实际落点标记主流
	g.markChatInterleaved(binding.ChatID, "", "", "")
	_ = g.host.SaveCard(store.LarkCard{
		Kind: "perm", RefID: req.ID, BotID: effBot.ID, ChatID: binding.ChatID,
		CardMsgID: cardMsgID, DiscussionKey: binding.DiscussionKey, Status: "waiting",
	})
	g.mu.Lock()
	g.activePerms[req.ID] = &activePerm{
		botID: effBot.ID, profile: effBot.Profile, botName: effBot.Name,
		agentName: agentLabel, wsLabel: wsLabel, tool: req.Tool, summary: req.Summary, ruleLabel: req.RuleLabel, cardMsgID: cardMsgID,
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

func interleavedReplyNotice(outcome string) string {
	switch outcome {
	case "failed", "error", "interrupted":
		return tr("Execution failed — tap the quote above for details")
	case "stopped", "cancelled":
		return tr("Execution stopped — tap the quote above for details")
	default:
		return tr("Replied above — tap the quote to view the full result")
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

func (g *Gateway) execAgentLabel(execID, fallbackAgent, fallbackModel, fallbackEffort string) string {
	agentName, model, effort := fallbackAgent, fallbackModel, fallbackEffort
	if execID != "" && g.host != nil {
		if snap, ok := g.host.ExecutionSnapshot(execID); ok {
			if snap.Agent != "" {
				agentName = snap.Agent
			}
			if snap.Model != "" {
				model = snap.Model
				effort = snap.Effort
			} else if snap.Effort != "" {
				effort = snap.Effort
			}
		}
	}
	return FormatModelLabel(agentName, model, effort)
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

func (g *Gateway) queuedBehindExecID(discKey, execID string) string {
	if discKey == "" || execID == "" {
		return ""
	}
	active, ok := g.host.ActiveExecutionFor(discKey)
	if !ok || active.ID == "" || active.ID == execID {
		return ""
	}
	return active.ID
}

func (g *Gateway) getDebounceWindow() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.debounceWindow
}

func isDebounceableTextEvent(ev MessageEvent, body string) bool {
	if body == "" {
		return false
	}
	return ev.MessageType == "" || ev.MessageType == "text"
}

func (g *Gateway) enqueueTextDebounce(bot BotConfig, ev MessageEvent, threadKey string, inThread bool, body string) {
	key := stagedKey{
		botID:     bot.ID,
		chatID:    ev.ChatID,
		threadKey: threadKey,
		senderID:  ev.SenderID,
	}
	now := g.now()

	g.mu.Lock()
	window := g.debounceWindow
	maxWait := g.debounceMax
	if maxWait <= 0 {
		maxWait = maxInboundDebounce
	}
	b := g.debounced[key]
	if b == nil {
		b = &debounceBatch{
			bot:       bot,
			ev:        ev,
			threadKey: threadKey,
			inThread:  inThread,
			bodies:    []string{body},
			firstAt:   now,
		}
		g.debounced[key] = b
	} else {
		if b.timer != nil {
			b.timer.Stop()
		}
		firstParent := b.ev.ParentID
		b.bot = bot
		b.ev = ev
		if b.ev.ParentID == "" && firstParent != "" {
			b.ev.ParentID = firstParent
		}
		b.threadKey = threadKey
		b.inThread = inThread
		b.bodies = append(b.bodies, body)
	}
	b.seq++
	seq := b.seq
	wait := window
	if elapsed := now.Sub(b.firstAt); elapsed+wait > maxWait {
		wait = maxWait - elapsed
		if wait < 0 {
			wait = 0
		}
	}
	target := b
	b.timer = time.AfterFunc(wait, func() {
		g.flushDebounced(key, target, seq)
	})
	g.mu.Unlock()
}

func (g *Gateway) flushDebounced(key stagedKey, target *debounceBatch, seq uint64) {
	g.mu.Lock()
	if g.debounced[key] != target || target.seq != seq {
		g.mu.Unlock()
		return
	}
	delete(g.debounced, key)
	ctx := g.parentCtx
	bot := target.bot
	ev := target.ev
	threadKey := target.threadKey
	inThread := target.inThread
	body := strings.Join(target.bodies, "\n")
	g.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}
	// 派发前严格复查最新机器人与群策略：机器人被删除、禁用、换 profile、关闭免 @、收紧白名单或切换会话粒度时，丢弃旧批次
	latest, ok := g.botByID(bot.ID)
	if !ok || !latest.Enabled || latest.Profile != bot.Profile {
		return
	}
	effBot, noMention := latest.EffectiveForChat(ev.ChatID)
	owner := g.Config().OwnerOpenID
	if !effBot.AllowsTrigger(ev.SenderID, owner, ev.ChatID) {
		return
	}
	if ev.ChatType == "group" {
		if !noMention {
			ident := g.botIdentity(ctx, latest.ID, latest.Profile)
			if ctx.Err() != nil {
				return
			}
			// 身份刷新可能等待外部 CLI，返回后再次复查最新配置、owner 与群策略，防止等待期间被禁用/换 profile/收紧白名单
			latest, ok = g.botByID(bot.ID)
			if !ok || !latest.Enabled || latest.Profile != bot.Profile {
				return
			}
			effBot, noMention = latest.EffectiveForChat(ev.ChatID)
			owner = g.Config().OwnerOpenID
			if !effBot.AllowsTrigger(ev.SenderID, owner, ev.ChatID) {
				return
			}
			if !noMention && !isMentioningBot(latest, ident, ev) {
				return
			}
		}
		if (effBot.SessionMode == SessionThread) != inThread {
			return
		}
	}
	bot = effBot
	g.dispatchMessage(ctx, bot, ev, threadKey, inThread, body)
}

func (g *Gateway) takeDebounced(key stagedKey) *debounceBatch {
	g.mu.Lock()
	b := g.debounced[key]
	if b != nil {
		delete(g.debounced, key)
	}
	g.mu.Unlock()
	if b != nil && b.timer != nil {
		b.timer.Stop()
	}
	return b
}

func (g *Gateway) cancelDebounced(key stagedKey) {
	_ = g.takeDebounced(key)
}

func sanitizeAttachmentName(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\\", "/")
	s = filepath.Base(s)
	if s == "" || s == "." || s == ".." || s == "/" {
		return ""
	}
	return s
}

func parseRawJSONMap(raw json.RawMessage, content string) map[string]any {
	tryParse := func(s string) map[string]any {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil && m != nil {
			return m
		}
		// 兼容双重 JSON 编码字符串（飞书事件 content 本身常为 JSON 字符串）
		var inner string
		if json.Unmarshal([]byte(s), &inner) == nil {
			inner = strings.TrimSpace(inner)
			if json.Unmarshal([]byte(inner), &m) == nil && m != nil {
				return m
			}
		}
		return nil
	}
	if m := tryParse(string(raw)); m != nil {
		return m
	}
	return tryParse(content)
}

func parseAttachmentMeta(ev MessageEvent) (stagedAttachment, bool) {
	if ev.MessageType != "image" && ev.MessageType != "file" {
		return stagedAttachment{}, false
	}
	raw := parseRawJSONMap(ev.RawContent, ev.Content)
	key := ""
	kind := ev.MessageType
	fileName := ""
	if raw != nil {
		if k, _ := raw["image_key"].(string); k != "" {
			key = k
			kind = "image"
		} else if k, _ := raw["file_key"].(string); k != "" {
			key = k
			kind = "file"
		}
		fileName, _ = raw["file_name"].(string)
	}
	if key == "" && ev.MsgID() == "" {
		return stagedAttachment{}, false
	}
	return stagedAttachment{
		msgID:    ev.MsgID(),
		fileKey:  key,
		kind:     kind,
		fileName: sanitizeAttachmentName(fileName),
		rootID:   ev.RootID,
	}, true
}

func stagedLabels(items []stagedAttachment) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.displayLabel())
	}
	return out
}

func (g *Gateway) stageAttachment(ctx context.Context, bot BotConfig, ev MessageEvent, threadKey string, inThread bool, item stagedAttachment) {
	key := stagedKey{
		botID:     bot.ID,
		chatID:    ev.ChatID,
		threadKey: threadKey,
		senderID:  ev.SenderID,
	}
	now := g.now()

	var expiredOld *stagedBatch
	var b *stagedBatch
	var batchID string

	g.mu.Lock()
	b = g.staged[key]
	if b != nil && now.Sub(b.updatedAt) > pendingAttachTTL {
		expiredOld = b
		delete(g.staged, key)
		b = nil
	}
	if b == nil {
		b = &stagedBatch{
			id:        fmt.Sprintf("sb_%d", now.UnixNano()),
			botID:     bot.ID,
			profile:   bot.Profile,
			botName:   bot.Name,
			chatID:    ev.ChatID,
			chatType:  ev.ChatType,
			threadKey: threadKey,
			inThread:  inThread,
			senderID:  ev.SenderID,
			updatedAt: now,
			items:     []stagedAttachment{item},
		}
		g.staged[key] = b
	} else {
		if len(b.items) < maxStagedAttachments {
			b.items = append(b.items, item)
		}
		b.updatedAt = now
	}
	batchID = b.id
	g.mu.Unlock()

	if expiredOld != nil {
		g.closeStagedCard(ctx, expiredOld, "expired")
	}

	b.cardMu.Lock()
	defer b.cardMu.Unlock()
	if b.closedStatus != "" {
		return
	}
	g.mu.Lock()
	labels := stagedLabels(b.items)
	g.mu.Unlock()

	cardJSON := BuildStagedAttachmentsCard(bot.ID, bot.Name, batchID, labels)
	if b.cardMsgID != "" {
		if err := g.client.PatchCard(ctx, bot.Profile, b.cardMsgID, cardJSON); err == nil {
			return
		}
	}
	cardMsgID, err := g.client.ReplyCard(ctx, bot.Profile, ev.MsgID(), inThread, cardJSON)
	if err != nil || cardMsgID == "" {
		return
	}
	b.cardMsgID = cardMsgID
}

func (g *Gateway) takeStaged(ctx context.Context, key stagedKey) *stagedBatch {
	now := g.now()
	g.mu.Lock()
	b := g.staged[key]
	if b != nil {
		delete(g.staged, key)
	}
	g.mu.Unlock()
	if b == nil {
		return nil
	}
	if now.Sub(b.updatedAt) > pendingAttachTTL {
		g.closeStagedCard(ctx, b, "expired")
		return nil
	}
	return b
}

func (g *Gateway) findAndRemoveStaged(botID, batchID, operatorID string, isOwner bool) (batch *stagedBatch, denied bool) {
	if batchID == "" {
		return nil, false
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, b := range g.staged {
		if b.botID != botID || b.id != batchID {
			continue
		}
		if !isOwner && operatorID != "" && b.senderID != "" && b.senderID != operatorID {
			return nil, true
		}
		delete(g.staged, k)
		if now.Sub(b.updatedAt) > pendingAttachTTL {
			return nil, false
		}
		return b, false
	}
	return nil, false
}

func (g *Gateway) closeStagedCard(ctx context.Context, b *stagedBatch, status string) {
	g.closeStagedCardWithFallback(ctx, b, status, "")
}

func (g *Gateway) closeStagedCardWithFallback(ctx context.Context, b *stagedBatch, status, fallbackCardID string) {
	if b == nil {
		return
	}
	b.cardMu.Lock()
	defer b.cardMu.Unlock()
	b.closedStatus = status
	cardID := b.cardMsgID
	if cardID == "" {
		cardID = fallbackCardID
	}
	if cardID == "" {
		return
	}
	g.mu.Lock()
	count := len(b.items)
	g.mu.Unlock()
	card := BuildStagedAttachmentsClosedCard(b.botName, status, count)
	_ = g.client.PatchCard(ctx, b.profile, cardID, card)
}

func (g *Gateway) submitStagedBatch(ctx context.Context, bot BotConfig, b *stagedBatch, actionMsgID string) {
	if b == nil {
		return
	}
	b.cardMu.Lock()
	b.closedStatus = "submitted"
	cardID := b.cardMsgID
	b.cardMu.Unlock()
	if cardID == "" {
		cardID = actionMsgID
	}

	g.mu.Lock()
	items := append([]stagedAttachment(nil), b.items...)
	g.mu.Unlock()
	if len(items) == 0 {
		return
	}
	atts, attDir, _ := g.downloadAttachmentsList(ctx, bot, items)
	if attDir != "" {
		defer os.RemoveAll(attDir)
	}
	if len(atts) == 0 {
		g.closeStagedCardWithFallback(ctx, b, "cancelled", cardID)
		return
	}
	lastItem := items[len(items)-1]
	titleHint := oneLine(lastItem.displayLabel(), 36)
	if titleHint == "" {
		titleHint = bot.Name
	}
	binding, err := g.host.EnsureDiscussion(bot, b.chatID, b.chatType, b.threadKey, titleHint)
	if err != nil {
		if cardID != "" {
			_ = g.client.PatchCard(ctx, bot.Profile, cardID, BuildNoticeCard(tr("Error"), "red", err.Error()))
		}
		return
	}
	var metaParts []string
	metaParts = append(metaParts, "chat_id="+b.chatID)
	if b.chatType != "" {
		metaParts = append(metaParts, "chat_type="+b.chatType)
	}
	if lastItem.rootID != "" {
		metaParts = append(metaParts, "root_id="+lastItem.rootID)
	}
	if lastItem.msgID != "" {
		metaParts = append(metaParts, "message_id="+lastItem.msgID)
	}
	metaHeader := "[Feishu " + strings.Join(metaParts, " ") + "]"

	execID, err := g.host.PostFromLark(binding, "", metaHeader, atts)
	if err != nil {
		if cardID != "" {
			_ = g.client.PatchCard(ctx, bot.Profile, cardID, BuildNoticeCard(tr("Error"), "red", err.Error()))
		}
		return
	}

	wsLabel := g.workspaceLabel(binding.WorkspaceID)
	prevExecID := g.queuedBehindExecID(binding.DiscussionKey, execID)
	agentLabel := g.execAgentLabel(execID, binding.Agent, bot.Model, bot.Effort)
	cardJSON := BuildProgressCard(bot.ID, bot.Name, agentLabel, wsLabel, execID, tr("Queued"), "0s", "", "", "", 0, prevExecID)
	finalCardID := cardID
	if finalCardID == "" || g.client.PatchCard(ctx, bot.Profile, finalCardID, cardJSON) != nil {
		sentID, serr := g.client.ReplyCard(ctx, bot.Profile, lastItem.msgID, b.inThread, cardJSON)
		if serr != nil || sentID == "" {
			sentID, serr = g.client.SendCard(ctx, bot.Profile, b.chatID, cardJSON)
		}
		if serr != nil || sentID == "" {
			return
		}
		finalCardID = sentID
	}
	_ = g.host.SaveCard(store.LarkCard{
		Kind:          "exec",
		RefID:         execID,
		BotID:         bot.ID,
		ChatID:        b.chatID,
		ReplyTo:       lastItem.msgID,
		CardMsgID:     finalCardID,
		DiscussionKey: binding.DiscussionKey,
		Status:        "active",
	})
	g.mu.Lock()
	g.markChatInterleavedLocked(b.chatID, b.threadKey, lastItem.rootID, execID)
	g.activeCards[execID] = &activeCard{
		botID:     bot.ID,
		profile:   bot.Profile,
		botName:   bot.Name,
		execID:    execID,
		chatID:    b.chatID,
		threadKey: b.threadKey,
		replyTo:   lastItem.msgID,
		inThread:  b.inThread,
		cardMsgID: finalCardID,
		lastJSON:  cardJSON,
	}
	g.mu.Unlock()
}

func (g *Gateway) downloadAttachmentsList(ctx context.Context, bot BotConfig, items []stagedAttachment) ([]string, string, map[string]bool) {
	if len(items) == 0 {
		return nil, "", nil
	}
	dir, err := os.MkdirTemp("", "posse-lark-att-*")
	if err != nil {
		return nil, "", nil
	}
	var out []string
	downloadedMsgIDs := map[string]bool{}
	for i, it := range items {
		subDir := filepath.Join(dir, fmt.Sprintf("%d", i))
		dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		if it.fileKey != "" {
			p, err := g.client.DownloadResource(dctx, bot.Profile, it.msgID, it.fileKey, it.kind, subDir)
			cancel()
			if err != nil || p == "" {
				continue
			}
			if it.fileName != "" && filepath.Base(p) != it.fileName {
				target := filepath.Join(filepath.Dir(p), it.fileName)
				if err := os.Rename(p, target); err == nil {
					p = target
				}
			}
			out = append(out, p)
			if it.msgID != "" {
				downloadedMsgIDs[it.msgID] = true
			}
			continue
		}
		// 无 file_key 时回退通过 messages-mget 拉取附件，兼容仅含渲染文本的事件格式
		fetcher, ok := g.client.(messageContextFetcher)
		if !ok || it.msgID == "" {
			cancel()
			continue
		}
		msg, err := fetcher.GetMessage(dctx, bot.Profile, it.msgID, subDir)
		cancel()
		if err != nil || msg.Deleted {
			continue
		}
		got := false
		for _, r := range msg.Resources {
			if r.Error || r.LocalPath == "" {
				continue
			}
			if st, err := os.Stat(r.LocalPath); err == nil && !st.IsDir() {
				out = append(out, r.LocalPath)
				got = true
			}
		}
		if got {
			downloadedMsgIDs[it.msgID] = true
		}
	}
	if len(out) == 0 {
		_ = os.RemoveAll(dir)
		return nil, "", nil
	}
	return out, dir, downloadedMsgIDs
}

func (g *Gateway) downloadEventAttachments(ctx context.Context, bot BotConfig, ev MessageEvent) ([]string, string) {
	meta, ok := parseAttachmentMeta(ev)
	if !ok {
		return nil, ""
	}
	atts, dir, _ := g.downloadAttachmentsList(ctx, bot, []stagedAttachment{meta})
	return atts, dir
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
