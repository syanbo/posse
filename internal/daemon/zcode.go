package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"log"
	"strings"
	"time"
	"unicode"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/rpc"
	"posse/internal/store"
)

var errZCodeInputChanged = errors.New("ZCode input changed before submission")

func (s *session) queueZCodeNudge() {
	s.mu.Lock()
	s.zcodeNudge = true
	s.mu.Unlock()
}

// syncZCodeDispatch 等真实的输入区空闲后再发。SessionStart 在首条提示提交后才出现，
// Stop 也可能早于界面画回输入区，不能靠这两个 hook 决定何时回车。
func (d *Daemon) syncZCodeDispatch(ctx context.Context, s *session) {
	s.mu.Lock()
	taskID, nudge := s.zcodeStart, s.zcodeNudge
	s.mu.Unlock()
	if taskID == "" && !nudge {
		return
	}
	d.handoff.startMu.RLock()
	defer d.handoff.startMu.RUnlock()
	if d.handoff.active() || d.stopping.Load() {
		return
	}
	if !s.Running() {
		if taskID != "" {
			d.workerGone(s.ID, "ZCode exited before accepting the task prompt")
		}
		s.mu.Lock()
		s.zcodeStart, s.zcodeNudge = "", false
		s.mu.Unlock()
		return
	}
	pg := s.Foreground()
	if !s.zcodeReady(pg) {
		return
	}
	if taskID != "" {
		d.submitZCodeTask(ctx, s, pg, taskID)
		return
	}
	s.mu.Lock()
	s.zcodeNudge = false
	s.mu.Unlock()
	d.nudgeMu.Lock()
	text, ids := d.nudgeText(s.ID)
	d.nudgeMu.Unlock()
	if text == "" {
		return
	}
	attempted, err := s.submitZCode(ctx, pg, text)
	switch {
	case !attempted:
		s.queueZCodeNudge()
	case errors.Is(err, errZCodeInputChanged):
		// 用户正在接手，提醒仍留在收件箱。
	case err != nil:
		log.Printf("terminal %s: submit ZCode reminder: %v", s.ID, err)
	default:
		if err := d.store.MarkNudged(ids, time.Now()); err != nil {
			log.Printf("terminal %s: record ZCode reminder: %v", s.ID, err)
		} else {
			// 同一个终端也可能既是派发方又是 worker，两边的提醒分开提交。
			s.queueZCodeNudge()
		}
	}
}

func (d *Daemon) submitZCodeTask(ctx context.Context, s *session, pg int, taskID string) {
	t, err := d.store.Task(taskID)
	if err == nil && t.Status == store.TaskRunning && t.Worker == s.ID {
		w, err := d.store.Workspace(t.WorkspaceID)
		if err != nil {
			d.workerGone(s.ID, "could not load the ZCode task workspace")
		} else {
			attempted, err := s.submitZCode(ctx, pg, workerPrompt(t, w))
			if !attempted {
				return
			}
			if err != nil && !errors.Is(err, errZCodeInputChanged) {
				log.Printf("terminal %s: submit ZCode task %s: %v", s.ID, taskID, err)
				d.workerGone(s.ID, "could not submit the ZCode task prompt")
			}
		}
	} else if err != nil && err != store.ErrNotFound {
		return
	}
	s.mu.Lock()
	s.zcodeStart = ""
	s.mu.Unlock()
}

func (s *session) zcodeForeground(pg int) bool {
	if pg <= 0 || s.agentName() != "zcode" || s.Foreground() != pg || !s.BracketedPaste() {
		return false
	}
	name, _ := agent.OfProcess(pg)
	return name == "zcode" && s.Foreground() == pg
}

func (s *session) zcodeReady(pg int) bool {
	return time.Since(s.LastInput()) >= nudgeQuiet && s.zcodeForeground(pg) &&
		s.ScreenTextColorsDiffer(zcodePromptCells) && s.Foreground() == pg && time.Since(s.LastInput()) >= nudgeQuiet
}

func zcodePromptCells(screen []string) (image.Point, image.Point, bool) {
	box, ok := zcodeComposer(screen)
	if !ok || !zcodePlaceholder(box.draft) {
		return image.Point{}, image.Point{}, false
	}
	first, firstCol, _ := zcodeComposerCell(screen[box.firstRow])
	_, modelCol, _ := zcodeComposerCell(screen[box.modelRow])
	// 原生占位符用弱化色，用户输入与模型名同色。单看文字无法区分同名草稿。
	return image.Pt(firstCol, box.firstRow), image.Pt(modelCol, box.modelRow), zcodePlaceholder(first)
}

func zcodePlaceholder(draft string) bool {
	return draft == "Type a prompt" || draft == "输入提示词"
}

type zcodeInputBox struct {
	draft              string
	firstRow, modelRow int
}

func zcodeComposerCell(row string) (string, int, bool) {
	left := strings.TrimLeft(row, " ")
	content, ok := strings.CutPrefix(left, "│")
	if !ok {
		return "", 0, false
	}
	text, _, ok := strings.Cut(content, "│")
	trimmed := strings.TrimLeft(text, " ")
	col := len(row) - len(left) + 1 + len(text) - len(trimmed)
	return strings.TrimSpace(trimmed), col, ok
}

// zcodeComposer 识别 0.18 的原生编辑框。审批和交互问题替换编辑框，插件选择器则显示在它上面。
func zcodeComposer(screen []string) (zcodeInputBox, bool) {
	for end := len(screen) - 1; end >= max(5, len(screen)-4); end-- {
		bottom := strings.TrimLeft(screen[end], " ")
		if !strings.HasPrefix(bottom, "╰─") || !strings.Contains(bottom, "╯") {
			continue
		}
		model, _, ok := zcodeComposerCell(screen[end-1])
		if !ok || model == "- - | -" || !strings.Contains(model, " | ") {
			continue
		}
		for start := end - 5; start >= max(0, end-9); start-- {
			top := strings.TrimLeft(screen[start], " ")
			if !strings.HasPrefix(top, "╭─ ") || !strings.Contains(top, "╮") {
				continue
			}
			for _, row := range screen[max(0, start-2):start] {
				if strings.HasPrefix(strings.TrimLeft(row, " "), "└─") && strings.Contains(row, "┘") {
					return zcodeInputBox{}, false
				}
			}
			var lines []string
			for _, row := range screen[start+1 : end-1] {
				line, _, ok := zcodeComposerCell(row)
				if !ok {
					return zcodeInputBox{}, false
				}
				lines = append(lines, line)
			}
			return zcodeInputBox{draft: strings.TrimSpace(strings.Join(lines, "\n")), firstRow: start + 1, modelRow: end - 1}, true
		}
	}
	return zcodeInputBox{}, false
}

func (s *session) submitZCode(ctx context.Context, pg int, text string) (bool, error) {
	// 提示中的控制字符不能作为终端按键。
	text = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ReplaceAll(text, "\r\n", "\n"))
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	input := s.LastInput()
	attempted, err := s.InjectWhen(ctx, []byte("\x1b[200~"+text+"\x1b[201~"), func() bool {
		return s.zcodeReady(pg) && s.LastInput().Equal(input)
	})
	if !attempted || err != nil {
		return attempted, err
	}
	// 等 OpenTUI 同步草稿。紧跟粘贴的回车会发送内容，却在输入框留下已发过的草稿。
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	ready := func() bool {
		if !s.zcodeForeground(pg) {
			return false
		}
		box, ok := zcodeComposer(s.Screen())
		draft := box.draft
		if !ok || draft == "" || zcodePlaceholder(draft) {
			return false
		}
		for line := range strings.SplitSeq(draft, "\n") {
			if line = strings.TrimSpace(line); line != "" && strings.Contains(text, line) {
				return s.Foreground() == pg && s.LastInput().Equal(input)
			}
		}
		return false
	}
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-tick.C:
		}
		if !s.LastInput().Equal(input) {
			return true, errZCodeInputChanged
		}
		sent, err := s.InjectWhen(ctx, []byte("\r"), ready)
		if sent || err != nil {
			return true, err
		}
	}
}

func (d *Daemon) zcodeSessionTools(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	return d.readZCodeTools(ctx, p.SessionID)
}

func (d *Daemon) zcodeDatabase() string {
	d.agents.mu.Lock()
	storage, detected := d.agents.zcode, d.agents.installed != nil
	d.agents.mu.Unlock()
	if !detected {
		storage = agent.CurrentZCodeStorage()
	}
	return storage.Database(d.agentDataHome())
}

func (d *Daemon) readZCodeTools(ctx context.Context, sessionID string) (history.SessionUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return history.ReadZCodeUsage(ctx, d.zcodeDatabase(), sessionID)
}

// zcodeRuntime 在 zcodeMu 内调用。进程同时持有多个顶层会话时只返回 PID，不能用缓存代替当前归属。
func (d *Daemon) zcodeRuntime(ctx context.Context, s *session, path string) (pid int, sessionID string) {
	pid = s.Foreground()
	if name, _ := agent.OfProcess(pid); name != "zcode" {
		return 0, ""
	}
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	sid, title, ambiguous := history.ZCodeRuntimeSession(ctx, path, pid)
	if s.Foreground() != pid || s.agentName() != "zcode" {
		return 0, ""
	}
	if ambiguous {
		return pid, ""
	}
	if sid == "" {
		return 0, ""
	}
	s.mu.Lock()
	if s.agent != "zcode" {
		s.mu.Unlock()
		return 0, ""
	}
	if s.Agent().SessionID != sid {
		s.zcodeTurn = ""
		s.unread = false
		s.ResetAgent(host.AgentState{SessionID: sid, Title: title, Event: "process", At: time.Now()})
	}
	s.mu.Unlock()
	return pid, sid
}

func (d *Daemon) syncZCode(ctx context.Context, s *session) {
	if s.agentName() != "zcode" || !s.Running() || !s.zcodeMu.TryLock() {
		return
	}
	defer s.zcodeMu.Unlock()
	d.zcodeRuntime(ctx, s, d.zcodeDatabase())
}

func (d *Daemon) zcodeHook(s *session, p hookEventParams) {
	var h agent.ZCodeHook
	if json.Unmarshal(p.Payload, &h) != nil || h.Name != p.Event || !agent.IsZCodeSession(h.SessionID) || h.Timestamp.IsZero() {
		return
	}
	s.zcodeMu.Lock()
	defer s.zcodeMu.Unlock()
	// 任务或工具也能启动 ZCode 并继承终端 ID。只有前台进程自身拉起的 hook 才能更新状态。
	if p.ParentPID <= 0 || p.ParentPID != s.Foreground() {
		return
	}
	if found, _ := agent.OfProcess(p.ParentPID); found != "zcode" {
		return
	}
	if s.zcodePID != p.ParentPID {
		s.zcodePID, s.zcodeTurn, s.zcodeAt = p.ParentPID, "", time.Time{}
	}
	if h.Timestamp.Before(s.zcodeAt) {
		return
	}
	d.foreground(s)
	cur := s.Agent()
	st := host.AgentState{Event: p.Event, SessionID: h.SessionID, At: time.Now()}
	switch p.Event {
	case "SessionStart":
		if cur.SessionID == h.SessionID {
			return
		}
		s.zcodeTurn = ""
	case "UserPromptSubmit":
		if h.TurnID == "" || cur.SessionID != "" && cur.SessionID != h.SessionID {
			return
		}
		s.zcodeTurn = h.TurnID
		st.Status = agent.StatusWorking
	case "Stop":
		if h.TurnID == "" || cur.SessionID != h.SessionID || cur.Status != agent.StatusWorking ||
			s.zcodeTurn != "" && s.zcodeTurn != h.TurnID {
			return
		}
		st.Status = agent.StatusIdle
		st.Message = agent.Truncate(h.Response, 300)
	default:
		return
	}
	s.zcodeAt = h.Timestamp
	gen := s.markHooked("zcode", h.SessionID)
	d.setAgentFor(s, st, gen)
	d.saveResumeFor(s, false, gen)
}
