package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"posse/internal/rpc"
	"posse/internal/usage"
)

// 套餐用量：agent 运行时顺带更新，不轮询。
//   - Claude：posse statusline 经 usage.report 报上 statusLine 的输入；Stop 时看一眼 Claude 自己的缓存（~/.claude.json）
//   - Codex：Stop、PostToolUse 时读会话文件末尾的 rate_limits；重置卡只有 Codex 自己知道，
//     Stop 之后拉起 codex app-server 问一次，至少隔 codexAskEvery
//   - Grok：标题变化时增量读它的日志
//   - Cursor：启动时和 stop 时向 Cursor 查一次，至少隔 cursorEvery
//   - 界面点刷新（usage.refresh）时各家都马上取一次，不管间隔
//   - 设置页整个关掉（usage.json 的 off）后以上都不做，读数也不给界面和手机
const (
	cursorEvery   = 5 * time.Minute
	codexAskEvery = 5 * time.Minute
	grokEvery     = 3 * time.Second
	codexEvery    = 5 * time.Second
)

type usageState struct {
	store *usage.Store // 测试里为空
	grok  *usage.GrokLog
	http  *http.Client

	mu           sync.Mutex
	grokAt       time.Time
	codexAt      map[string]time.Time // 按会话文件
	claudeMod    time.Time            // 上次读 Claude 缓存时文件的修改时间
	cursorAt     time.Time
	cursorBusy   bool
	cursorDenied bool // 读钥匙串要弹授权窗：本次常驻进程运行期间不再试
	codexAskAt   time.Time
	codexBusy    bool
}

// usageStatus 随 daemon.status 给界面。
type usageStatus struct {
	Usages    []usage.Usage `json:"usages"`
	Off       bool          `json:"off"`
	CursorOff bool          `json:"cursorOff"`
	CodexOff  bool          `json:"codexOff"`
}

func (d *Daemon) openUsage() {
	d.usage.store = usage.Open(d.cfg.Home)
	d.usage.grok = usage.NewGrokLog(usage.GrokLogPath())
	d.usage.http = &http.Client{Timeout: 15 * time.Second}
	d.usage.codexAt = map[string]time.Time{}
	go d.usageStart()
}

// usageStart 从各家本地留下的记录里取最近的一份（都只读本地文件），常驻进程启动和设置里重新打开时各做一次。
func (d *Daemon) usageStart() {
	if !d.usageOn() {
		return
	}
	d.claudeCacheUsage()
	if u, ok := usage.CodexLatest(usage.CodexHome()); ok {
		d.usage.store.Update(u)
	}
	d.grokUsage(true)
	// Cursor 本地没有用量，不查就要等到下次用 Cursor 才有
	d.cursorUsage(false)
}

// usageOn 是要不要读取套餐用量：测试里没有 store，用户也可以在设置里整个关掉。
func (d *Daemon) usageOn() bool {
	return d.usage.store != nil && !d.usage.store.Off()
}

func (d *Daemon) usageStatus() *usageStatus {
	if d.usage.store == nil {
		return nil
	}
	if d.usage.store.Off() {
		return &usageStatus{Usages: []usage.Usage{}, Off: true, CursorOff: d.usage.store.CursorOff(), CodexOff: d.usage.store.CodexOff()}
	}
	return &usageStatus{Usages: d.usage.store.List(time.Now()), CursorOff: d.usage.store.CursorOff(), CodexOff: d.usage.store.CodexOff()}
}

// usageHook 在 hook 事件之后看要不要更新用量；都在后台做，不拖慢 hook。
func (d *Daemon) usageHook(agentName, event string, payload json.RawMessage) {
	if !d.usageOn() {
		return
	}
	switch {
	case agentName == "claude" && event == "Stop":
		go d.claudeCacheUsage()
	case agentName == "codex" && (event == "Stop" || event == "PostToolUse"):
		var p struct {
			TranscriptPath string `json:"transcript_path"`
		}
		json.Unmarshal(payload, &p)
		if p.TranscriptPath == "" {
			return
		}
		d.usage.mu.Lock()
		due := event == "Stop" || time.Since(d.usage.codexAt[p.TranscriptPath]) >= codexEvery
		if due {
			d.usage.codexAt[p.TranscriptPath] = time.Now()
		}
		d.usage.mu.Unlock()
		if due {
			go func() {
				// token_count 和 hook 几乎同时写，稍等一下再读
				time.Sleep(500 * time.Millisecond)
				if u, ok := usage.CodexRollout(p.TranscriptPath); ok {
					d.usage.store.Update(u)
				}
			}()
		}
		if event == "Stop" {
			go d.codexAsk(false)
		}
	case agentName == "cursor" && event == "stop":
		go d.cursorUsage(false)
	}
}

// claudeCacheUsage 读 Claude 自己缓存的用量，文件没改过就不读（它有几百 KB）。
func (d *Daemon) claudeCacheUsage() {
	if !d.usageOn() {
		return
	}
	path := usage.ClaudeConfigPath()
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	d.usage.mu.Lock()
	same := fi.ModTime().Equal(d.usage.claudeMod)
	d.usage.claudeMod = fi.ModTime()
	d.usage.mu.Unlock()
	if same {
		return
	}
	if u, ok := usage.ClaudeCache(path); ok {
		d.usage.store.Update(u)
	}
}

// grokUsage 在 grok 终端标题变化时读日志新增的部分。
func (d *Daemon) grokUsage(force bool) {
	if !d.usageOn() {
		return
	}
	d.usage.mu.Lock()
	due := force || time.Since(d.usage.grokAt) >= grokEvery
	if due {
		d.usage.grokAt = time.Now()
	}
	d.usage.mu.Unlock()
	if !due {
		return
	}
	if u, ok := d.usage.grok.Read(); ok {
		d.usage.store.Update(u)
	}
}

// cursorUsage 向 Cursor 查一次用量。force 是用户点了刷新：不管间隔，钥匙串上次超时也再试。
func (d *Daemon) cursorUsage(force bool) {
	s := &d.usage
	if !d.usageOn() || s.store.CursorOff() {
		return
	}
	s.mu.Lock()
	if s.cursorBusy || (!force && (s.cursorDenied || time.Since(s.cursorAt) < cursorEvery)) {
		s.mu.Unlock()
		return
	}
	s.cursorBusy, s.cursorAt = true, time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cursorBusy = false
		s.mu.Unlock()
	}()
	token, err := usage.CursorToken(context.Background())
	if errors.Is(err, usage.ErrCursorLoggedOut) {
		return
	}
	if err != nil {
		if errors.Is(err, usage.ErrCursorDenied) {
			s.mu.Lock()
			s.cursorDenied = true
			s.mu.Unlock()
		}
		log.Printf("cursor usage: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u, err := usage.FetchCursor(ctx, s.http, token, time.Now())
	if err != nil {
		log.Printf("cursor usage: %v", err)
		return
	}
	s.store.Update(u)
}

// codexAsk 让 Codex 自己查一次限额和重置卡。force 是用户点了刷新，不管间隔。
func (d *Daemon) codexAsk(force bool) {
	s := &d.usage
	if !d.usageOn() || s.store.CodexOff() {
		return
	}
	s.mu.Lock()
	if s.codexBusy || (!force && time.Since(s.codexAskAt) < codexAskEvery) {
		s.mu.Unlock()
		return
	}
	s.codexBusy, s.codexAskAt = true, time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.codexBusy = false
		s.mu.Unlock()
	}()
	codex, err := exec.LookPath("codex")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u, err := usage.CodexRemote(ctx, codex, time.Now())
	if err != nil {
		log.Printf("codex usage: %v", err)
		return
	}
	s.store.Update(u)
}

type usageReportParams struct {
	Agent   string          `json:"agent"`
	Payload json.RawMessage `json:"payload"`
}

// usageReport 收 posse statusline 转来的 Claude statusLine 输入。
func (d *Daemon) usageReport(ctx context.Context, raw json.RawMessage) (any, error) {
	var p usageReportParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if !d.usageOn() || p.Agent != "claude" {
		return map[string]bool{"updated": false}, nil
	}
	u, ok := usage.ClaudeStatusLine(p.Payload, time.Now())
	return map[string]bool{"updated": ok && d.usage.store.Update(u)}, nil
}

// usageRefresh 是界面上的刷新：各家都马上取一次，等都做完再回。设置页里关掉的查询照样不做；
// Claude 没有可查的接口，只能重读它自己的缓存，statusLine 的读数要等它下次运行。
func (d *Daemon) usageRefresh(ctx context.Context, raw json.RawMessage) (any, error) {
	if d.usage.store == nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "usage is not available")
	}
	if d.usage.store.Off() {
		return d.usageStatus(), nil
	}
	d.usage.mu.Lock()
	d.usage.claudeMod = time.Time{}
	d.usage.mu.Unlock()
	var wg sync.WaitGroup
	for _, f := range []func(){
		d.claudeCacheUsage,
		func() {
			if u, ok := usage.CodexLatest(usage.CodexHome()); ok {
				d.usage.store.Update(u)
			}
			d.codexAsk(true)
		},
		func() { d.grokUsage(true) },
		func() { d.cursorUsage(true) },
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	wg.Wait()
	return d.usageStatus(), nil
}

func (d *Daemon) usageGet(ctx context.Context, raw json.RawMessage) (any, error) {
	if st := d.usageStatus(); st != nil {
		return st, nil
	}
	return usageStatus{Usages: []usage.Usage{}}, nil
}

type usageSetParams struct {
	Off       *bool `json:"off"`
	CursorOff *bool `json:"cursorOff"`
	CodexOff  *bool `json:"codexOff"`
}

func (d *Daemon) usageSet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p usageSetParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if d.usage.store == nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "usage is not available")
	}
	if p.Off != nil {
		was := d.usage.store.Off()
		d.usage.store.SetOff(*p.Off)
		if was && !*p.Off {
			// 关着的时候什么都没读，重新打开时像启动时一样取一次；Claude 缓存的修改时间也要重新比
			d.usage.mu.Lock()
			d.usage.claudeMod = time.Time{}
			d.usage.mu.Unlock()
			go d.usageStart()
		}
	}
	if p.CursorOff != nil {
		d.usage.store.SetCursorOff(*p.CursorOff)
		if !*p.CursorOff {
			// 重新打开时再试一次钥匙串，下次 Cursor 用完就查
			d.usage.mu.Lock()
			d.usage.cursorDenied, d.usage.cursorAt = false, time.Time{}
			d.usage.mu.Unlock()
		}
	}
	if p.CodexOff != nil {
		d.usage.store.SetCodexOff(*p.CodexOff)
	}
	return d.usageStatus(), nil
}
