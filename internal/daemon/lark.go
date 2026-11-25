package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"posse/internal/agent"
	"posse/internal/collaboration"
	"posse/internal/lark"
	"posse/internal/rpc"
	"posse/internal/store"
)

type larkState struct {
	mu     sync.Mutex
	gw     *lark.Gateway
	client lark.Client
}

type larkPermAdapter struct{ d *Daemon }

func (a larkPermAdapter) WatchPermission(req PermRequest) bool {
	gw := a.d.larkGateway()
	if gw == nil {
		return false
	}
	return gw.WatchPermission(lark.PermNotice{
		ID:         req.ID,
		TerminalID: req.TerminalID,
		Agent:      req.Agent,
		Tool:       req.Tool,
		Summary:    req.Summary,
		RuleLabel:  req.RuleLabel,
	})
}

func (a larkPermAdapter) PermissionResolved(reqID, behavior, source string) {
	if gw := a.d.larkGateway(); gw != nil {
		gw.PermissionResolved(reqID, behavior, source)
	}
}

func (d *Daemon) larkClient() lark.Client {
	d.lark.mu.Lock()
	defer d.lark.mu.Unlock()
	if d.lark.client == nil {
		d.lark.client = &lark.CLIClient{}
	}
	return d.lark.client
}

func (d *Daemon) larkGateway() *lark.Gateway {
	d.lark.mu.Lock()
	defer d.lark.mu.Unlock()
	return d.lark.gw
}

func (d *Daemon) larkStart(ctx context.Context) {
	client := d.larkClient()
	cfg, _ := lark.LoadConfig(d.cfg.Home)
	gw := lark.NewGateway(d.cfg.Home, client, larkHost{d: d})
	d.lark.mu.Lock()
	d.lark.gw = gw
	d.lark.mu.Unlock()
	d.perms.SetWatcher(larkPermAdapter{d: d})
	d.perms.SetRootsResolver(d.larkReadOnlyRoots)
	gw.Start(ctx, cfg)
}

func (d *Daemon) larkStop() {
	d.perms.SetWatcher(nil)
	d.lark.mu.Lock()
	gw := d.lark.gw
	d.lark.gw = nil
	d.lark.mu.Unlock()
	if gw != nil {
		gw.Stop()
	}
}

func (d *Daemon) larkBotByID(botID string) (lark.BotConfig, bool) {
	if botID == "" {
		return lark.BotConfig{}, false
	}
	cfg, err := lark.LoadConfig(d.cfg.Home)
	if err != nil {
		return lark.BotConfig{}, false
	}
	for _, bot := range cfg.Bots {
		if bot.ID == botID {
			return bot, true
		}
	}
	return lark.BotConfig{}, false
}

func (d *Daemon) larkReadOnlyRoots(terminalID string) agent.ScratchRoots {
	roots := agent.DefaultScratchRoots()
	roots.ReadOnlyHosts = nil
	roots.TrustedScripts = nil
	if d.store == nil || terminalID == "" {
		return roots
	}
	b, ok := larkHost{d: d}.BindingByTerminal(terminalID)
	if !ok {
		return roots
	}
	if bot, ok := d.larkBotByID(b.BotID); ok {
		roots.ReadOnlyHosts = bot.ReadOnlyHosts
		roots.TrustedScripts = bot.TrustedScripts
	}
	return roots
}

func (d *Daemon) larkLaunchOptions(discKey string) (permOverride string, externalApproval bool, extraEnv []string) {
	if d.store == nil || discKey == "" {
		return "", false, nil
	}
	b, err := d.store.LarkBindingByDiscussion(discKey)
	if err != nil || !b.Active {
		return "", false, nil
	}
	if b.PermissionMode == lark.PermSkip {
		return agent.PermissionSkip, false, nil
	}
	extraEnv = []string{
		"POSSE_LARK_APPROVAL=1",
		"POSSE_LARK_BOT_ID=" + b.BotID,
	}
	var scripts, hosts string
	if bot, ok := d.larkBotByID(b.BotID); ok {
		scripts = strings.Join(bot.TrustedScripts, ":")
		hosts = strings.Join(bot.ReadOnlyHosts, ",")
	}
	extraEnv = append(extraEnv,
		"POSSE_LARK_TRUSTED_SCRIPTS="+scripts,
		"POSSE_LARK_READONLY_HOSTS="+hosts,
	)
	return agent.PermissionAsk, true, extraEnv
}

func (d *Daemon) larkOnExecFinished(discKey, execID, outcome, body string) {
	if gw := d.larkGateway(); gw != nil && discKey != "" && execID != "" {
		go gw.OnExecFinished(discKey, execID, outcome, body)
	}
}

func (d *Daemon) larkOnQuestion(discKey, execID, questionID, agentName, question string, choices []string) {
	if gw := d.larkGateway(); gw != nil && discKey != "" && questionID != "" {
		go gw.OnQuestion(discKey, execID, questionID, agentName, question, choices)
	}
}

func (d *Daemon) larkOnQuestionAnswered(questionID, answer string) {
	if gw := d.larkGateway(); gw != nil && questionID != "" {
		go gw.OnQuestionAnswered(questionID, answer)
	}
}

func (d *Daemon) larkOnDesktopUserMessage(discKey, body string) {
	if gw := d.larkGateway(); gw != nil && discKey != "" && strings.TrimSpace(body) != "" {
		go gw.OnDesktopUserMessage(discKey, body)
	}
}

// --- RPC 接口 ---

func (d *Daemon) larkProfiles(ctx context.Context, _ json.RawMessage) (any, error) {
	c := d.larkClient()
	list, err := c.Profiles(ctx)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "lark-cli profiles: %v", err)
	}
	cfg, _ := lark.LoadConfig(d.cfg.Home)
	owner := cfg.OwnerOpenID
	if owner == "" {
		profile := ""
		if len(list) > 0 {
			profile = list[0].Name
		}
		if id, e := c.OwnerOpenID(ctx, profile); e == nil && id != "" {
			owner = id
		}
	}
	return map[string]any{"profiles": list, "ownerOpenId": owner}, nil
}

func (d *Daemon) larkBotsList(_ context.Context, _ json.RawMessage) (any, error) {
	cfg, err := lark.LoadConfig(d.cfg.Home)
	if err != nil {
		return nil, err
	}
	var statuses []lark.BotStatus
	if gw := d.larkGateway(); gw != nil {
		statuses = gw.Status()
	}
	return map[string]any{
		"ownerOpenId": cfg.OwnerOpenID,
		"bots":        cfg.Bots,
		"statuses":    statuses,
	}, nil
}

func (d *Daemon) larkStatus(_ context.Context, _ json.RawMessage) (any, error) {
	cfg, _ := lark.LoadConfig(d.cfg.Home)
	var statuses []lark.BotStatus
	if gw := d.larkGateway(); gw != nil {
		statuses = gw.Status()
	}
	return map[string]any{"ownerOpenId": cfg.OwnerOpenID, "statuses": statuses}, nil
}

func (d *Daemon) larkBotSave(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		OwnerOpenID string         `json:"ownerOpenId,omitempty"`
		Bot         lark.BotConfig `json:"bot"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := lark.ValidateBotPolicy(p.Bot.ReadOnlyHosts, p.Bot.TrustedScripts); err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%s", err.Error())
	}
	bot := lark.NormalizeBot(p.Bot)
	if bot.Profile == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "lark bot profile is required")
	}
	if bot.ID == "" {
		bot.ID = store.NewID("lbot_")
	}
	cfg, err := lark.LoadConfig(d.cfg.Home)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.OwnerOpenID) != "" {
		cfg.OwnerOpenID = strings.TrimSpace(p.OwnerOpenID)
	}
	replaced := false
	for i := range cfg.Bots {
		if cfg.Bots[i].ID == bot.ID {
			cfg.Bots[i] = bot
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Bots = append(cfg.Bots, bot)
	}
	if err := lark.SaveConfig(d.cfg.Home, cfg); err != nil {
		return nil, err
	}
	if gw := d.larkGateway(); gw != nil {
		gw.Reload(cfg)
	}
	return bot, nil
}

func (d *Daemon) larkBotDelete(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	cfg, err := lark.LoadConfig(d.cfg.Home)
	if err != nil {
		return nil, err
	}
	out := make([]lark.BotConfig, 0, len(cfg.Bots))
	found := false
	for _, b := range cfg.Bots {
		if b.ID == p.ID {
			found = true
			continue
		}
		out = append(out, b)
	}
	if !found {
		return nil, rpc.Errorf(rpc.CodeNotFound, "lark bot %s not found", p.ID)
	}
	cfg.Bots = out
	if err := lark.SaveConfig(d.cfg.Home, cfg); err != nil {
		return nil, err
	}
	if gw := d.larkGateway(); gw != nil {
		gw.Reload(cfg)
	}
	return map[string]bool{"deleted": true}, nil
}

func (d *Daemon) larkBotToggle(_ context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	cfg, err := lark.LoadConfig(d.cfg.Home)
	if err != nil {
		return nil, err
	}
	var updated lark.BotConfig
	found := false
	for i := range cfg.Bots {
		if cfg.Bots[i].ID == p.ID {
			cfg.Bots[i].Enabled = p.Enabled
			updated = cfg.Bots[i]
			found = true
			break
		}
	}
	if !found {
		return nil, rpc.Errorf(rpc.CodeNotFound, "lark bot %s not found", p.ID)
	}
	if err := lark.SaveConfig(d.cfg.Home, cfg); err != nil {
		return nil, err
	}
	if gw := d.larkGateway(); gw != nil {
		gw.Reload(cfg)
	}
	return updated, nil
}

// --- lark.Host 适配 ---

type larkHost struct{ d *Daemon }

func (h larkHost) EnabledAgents() []string {
	allowed := h.d.enabledAgents()
	if allowed == nil {
		allowed = agent.WithoutDisabled(h.d.cfg.Home, agent.Known)
	}
	var out []string
	for _, a := range allowed {
		if agent.CanJoinDiscussion(a) {
			out = append(out, a)
		}
	}
	return out
}

func (h larkHost) Workspaces() []lark.WorkspaceRef {
	ps, _ := h.d.store.Projects()
	pnames := map[string]string{}
	for _, p := range ps {
		pnames[p.ID] = p.Name
	}
	ws, _ := h.d.store.Workspaces("")
	out := make([]lark.WorkspaceRef, 0, len(ws))
	for _, w := range ws {
		label := w.Name
		if pn := pnames[w.ProjectID]; pn != "" {
			label = pn + "/" + w.Name
		}
		out = append(out, lark.WorkspaceRef{ID: w.ID, Label: label, Path: w.Path})
	}
	return out
}

func (h larkHost) resolveWorkspace(query string) (store.Workspace, error) {
	ws, err := h.d.store.Workspaces("")
	if err != nil {
		return store.Workspace{}, err
	}
	if query == "" {
		if len(ws) == 0 {
			return store.Workspace{}, errors.New("no workspaces registered in Posse")
		}
		return ws[0], nil
	}
	ps, _ := h.d.store.Projects()
	pnames := map[string]string{}
	for _, p := range ps {
		pnames[p.ID] = p.Name
	}
	q := strings.ToLower(strings.TrimSpace(query))
	for _, w := range ws {
		if w.ID == query {
			return w, nil
		}
		pn := pnames[w.ProjectID]
		label := strings.ToLower(pn + "/" + w.Name)
		if label == q || strings.ToLower(pn) == q || strings.ToLower(w.Name) == q {
			return w, nil
		}
	}
	return store.Workspace{}, fmt.Errorf("workspace %q not found", query)
}

func (h larkHost) ActiveBinding(botID, chatID, threadKey string) (store.LarkBinding, error) {
	return h.d.store.ActiveLarkBinding(botID, chatID, threadKey)
}

func (h larkHost) BindingByDiscussion(discKey string) (store.LarkBinding, error) {
	return h.d.store.LarkBindingByDiscussion(discKey)
}

func (h larkHost) BindingByTerminal(termID string) (store.LarkBinding, bool) {
	if termID == "" {
		return store.LarkBinding{}, false
	}
	ms, err := h.d.store.ActiveMembers()
	if err != nil {
		return store.LarkBinding{}, false
	}
	for _, m := range ms {
		if m.Worker == termID {
			b, err := h.d.store.LarkBindingByDiscussion(m.Discussion)
			if err == nil && b.Active {
				return b, true
			}
		}
	}
	return store.LarkBinding{}, false
}

func (h larkHost) EnsureDiscussion(bot lark.BotConfig, chatID, chatType, threadKey, titleHint string) (store.LarkBinding, error) {
	if b, err := h.d.store.ActiveLarkBinding(bot.ID, chatID, threadKey); err == nil {
		if disc, err := h.d.store.Discussion(b.DiscussionKey); err == nil && disc.Status != store.DiscussionArchived {
			return b, nil
		}
		_ = h.d.store.DeactivateLarkBinding(bot.ID, chatID, threadKey)
	}
	w, err := h.resolveWorkspace(bot.WorkspaceID)
	if err != nil {
		return store.LarkBinding{}, err
	}
	proj, _ := h.d.store.FindProject(w.ProjectID)
	title := strings.TrimSpace(titleHint)
	if title == "" {
		title = bot.Name
	}
	title = "[Feishu] " + bot.Name + " · " + title
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:79]) + "…"
	}
	id, err := h.d.identity()
	if err != nil {
		return store.LarkBinding{}, err
	}
	local := collaboration.NewDiscussionID()
	disc := store.Discussion{
		Key:         collaboration.RoomKey(id.ID, local),
		LocalID:     local,
		Title:       title,
		Description: "Feishu chat " + chatID,
		ProjectID:   proj.ID,
		ProjectName: proj.Name,
		ProjectPath: proj.Path,
		WorkspaceID: w.ID,
		Host:        id.ID,
		CreatedAt:   time.Now(),
	}
	key, err := h.d.store.CreateDiscussion(disc, "")
	if err != nil {
		return store.LarkBinding{}, err
	}
	agentName := bot.Agent
	if !agent.CanJoinDiscussion(agentName) {
		agentName = "antigravity"
	}
	m := store.Member{
		ID:          store.NewID("mb_"),
		Discussion:  key,
		Name:        bot.Name,
		Agent:       agentName,
		Model:       bot.Model,
		Effort:      bot.Effort,
		WorkspaceID: w.ID,
		Role:        bot.Role,
		Owner:       id.ID,
		CreatedAt:   time.Now(),
	}
	mid, _, err := h.d.store.AddMemberOnce(m, "")
	if err != nil {
		return store.LarkBinding{}, err
	}
	b := store.LarkBinding{
		ID:             store.NewID("lb_"),
		BotID:          bot.ID,
		ChatID:         chatID,
		ChatType:       chatType,
		ThreadKey:      threadKey,
		DiscussionKey:  key,
		MemberID:       mid,
		WorkspaceID:    w.ID,
		Agent:          agentName,
		PermissionMode: bot.PermissionMode,
		Active:         true,
	}
	if err := h.d.store.SaveLarkBinding(b); err != nil {
		return store.LarkBinding{}, err
	}
	h.d.taskNotify()
	return b, nil
}

func (h larkHost) SwitchAgent(b store.LarkBinding, agentName, model string) (store.LarkBinding, error) {
	agentName = strings.ToLower(strings.TrimSpace(agentName))
	if agentName == "agy" {
		agentName = "antigravity"
	}
	if !agent.CanJoinDiscussion(agentName) {
		return b, fmt.Errorf("agent %q cannot join discussions", agentName)
	}
	if err := h.d.checkEnabled(agentName); err != nil {
		return b, err
	}
	m, err := h.d.store.Member(b.MemberID)
	if err != nil {
		return b, err
	}
	m.Agent = agentName
	if model != "" {
		m.Model = model
	}
	old, _, err := h.d.store.UpdateMemberApplyNow(m)
	if err != nil {
		return b, err
	}
	if old.Worker != "" {
		h.d.perms.ClearTerminal(old.Worker)
		h.d.closeTerminal(old.Worker, 2*time.Second)
	}
	b.Agent = agentName
	if err := h.d.store.SaveLarkBinding(b); err != nil {
		return b, err
	}
	h.d.taskNotify()
	return b, nil
}

func (h larkHost) SwitchWorkspace(b store.LarkBinding, query string) (store.LarkBinding, error) {
	w, err := h.resolveWorkspace(query)
	if err != nil {
		return b, err
	}
	m, err := h.d.store.Member(b.MemberID)
	if err != nil {
		return b, err
	}
	m.WorkspaceID = w.ID
	old, _, err := h.d.store.UpdateMemberApplyNow(m)
	if err != nil {
		return b, err
	}
	if old.Worker != "" {
		h.d.perms.ClearTerminal(old.Worker)
		h.d.closeTerminal(old.Worker, 2*time.Second)
	}
	b.WorkspaceID = w.ID
	if err := h.d.store.SaveLarkBinding(b); err != nil {
		return b, err
	}
	h.d.taskNotify()
	return b, nil
}

func (h larkHost) ResetBinding(botID, chatID, threadKey string) error {
	if b, err := h.d.store.ActiveLarkBinding(botID, chatID, threadKey); err == nil {
		if m, err := h.d.store.Member(b.MemberID); err == nil && m.Worker != "" {
			h.d.perms.ClearTerminal(m.Worker)
			h.d.closeTerminal(m.Worker, 2*time.Second)
		}
		_ = h.d.store.SetArchived(b.DiscussionKey, true, time.Now())
	}
	return h.d.store.DeactivateLarkBinding(botID, chatID, threadKey)
}

func (h larkHost) PostFromLark(b store.LarkBinding, body, metaHeader string, attachments []string) (string, error) {
	disc, err := h.d.store.Discussion(b.DiscussionKey)
	if err != nil {
		return "", err
	}
	var attIn []discAttachIn
	for _, p := range attachments {
		attIn = append(attIn, discAttachIn{Path: p})
	}
	atts, cleanup, err := h.d.copyAttachments(disc, attIn)
	if err != nil {
		return "", err
	}
	for _, p := range attachments {
		_ = os.Remove(p)
	}
	fullBody := body
	if metaHeader != "" {
		if fullBody != "" {
			fullBody = metaHeader + "\n\n" + fullBody
		} else {
			fullBody = metaHeader
		}
	}
	_, xs, _, err := h.d.store.PostMessageMulti(store.PostInput{
		Discussion:  disc.Key,
		Body:        fullBody,
		Target:      b.MemberID,
		Targets:     []string{b.MemberID},
		ClientID:    "lark-" + store.NewID(""),
		Attachments: atts,
		At:          time.Now(),
	})
	if err != nil {
		cleanup()
		return "", err
	}
	h.d.taskNotify()
	h.d.discKick()
	if len(xs) == 0 {
		return "", errors.New("failed to schedule execution")
	}
	return xs[0].ID, nil
}

func (h larkHost) AnswerQuestion(questionID, body string) error {
	_, err := h.d.answerDiscussion(questionID, body, "lark-"+store.NewID(""))
	return err
}

func (h larkHost) StopExecution(execID string) error {
	x, err := h.d.ownExecSel(execID)
	if err != nil {
		return err
	}
	if !store.ExecActive(x.State) {
		return nil
	}
	h.d.finalizeTraceAsync(x.ID, x.Worker)
	h.d.store.EndTask(x.TaskID, store.TaskStopped, nil, store.ExecStopped, "stopped by the user", false, time.Now())
	h.d.closeTerminal(x.Worker, 2*time.Second)
	h.d.taskNotify()
	h.d.discKick()
	h.d.larkOnExecFinished(x.Discussion, x.ID, store.ExecStopped, "stopped by the user")
	return nil
}

func (h larkHost) RetryExecution(execID string) (string, error) {
	x, err := h.d.ownExecSel(execID)
	if err != nil {
		return "", err
	}
	nx, err := h.d.store.RetryExecution(x.ID, store.NewID("x_")+store.NewID(""), time.Now())
	if err != nil {
		return "", err
	}
	h.d.taskNotify()
	h.d.discKick()
	return nx.ID, nil
}

func (h larkHost) DecidePermission(permID, behavior, source string) bool {
	return h.d.perms.Decide(permID, behavior, source)
}

func (h larkHost) ExecutionSnapshot(execID string) (lark.ExecSnap, bool) {
	x, err := h.d.store.Execution(execID)
	if err != nil {
		return lark.ExecSnap{}, false
	}
	snap := lark.ExecSnap{
		ID:         x.ID,
		Discussion: x.Discussion,
		Agent:      x.Agent,
		Workspace:  x.WorkspaceLabel,
		State:      x.State,
		StartedAt:  x.StartedAt,
		EndedAt:    x.EndedAt,
	}
	if m, err := h.d.store.Member(x.Member); err == nil {
		snap.MemberName = m.Name
	}
	if counts := h.d.store.ExecTraceCounts([]string{x.ID}); counts[x.ID] > 0 {
		snap.TraceCount = counts[x.ID]
	}
	if store.ExecActive(x.State) {
		if pm := h.d.discProgressFor([]store.Execution{x}); len(pm) > 0 {
			p := pm[x.ID]
			snap.Tool = p.Tool
			snap.ToolInput = p.ToolInput
			snap.Phase = p.Phase
			snap.Subagents = p.Subagents
		}
	}
	return snap, true
}

func (h larkHost) ActiveExecutionFor(discKey string) (lark.ExecSnap, bool) {
	xs, err := h.d.store.ActiveExecutions()
	if err != nil {
		return lark.ExecSnap{}, false
	}
	for _, x := range xs {
		if x.Discussion == discKey {
			return h.ExecutionSnapshot(x.ID)
		}
	}
	return lark.ExecSnap{}, false
}

func (h larkHost) SaveCard(c store.LarkCard) error { return h.d.store.SaveLarkCard(c) }
func (h larkHost) GetCard(kind, refID string) (store.LarkCard, error) {
	return h.d.store.LarkCard(kind, refID)
}
func (h larkHost) CardByMessageID(cardMsgID string) (store.LarkCard, error) {
	return h.d.store.LarkCardByMessageID(cardMsgID)
}
func (h larkHost) MarkEvent(botID, eventKey string) (bool, error) {
	return h.d.store.MarkLarkEvent(botID, eventKey, time.Now())
}
