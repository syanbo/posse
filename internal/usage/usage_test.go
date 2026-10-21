package usage

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeStatusLine(t *testing.T) {
	now := time.UnixMilli(1792430000000)
	raw := `{"session_id":"s","model":{"id":"claude-fable-5-1"},"rate_limits":{"five_hour":{"used_percentage":15,"resets_at":1792438800},"seven_day":{"used_percentage":23,"resets_at":1792875600}}}`
	u, ok := ClaudeStatusLine([]byte(raw), now)
	if !ok {
		t.Fatal("no usage")
	}
	want := []Window{{Percent: 15, Minutes: 300, ResetsAt: 1792438800000}, {Percent: 23, Minutes: 10080, ResetsAt: 1792875600000}}
	if u.Agent != "claude" || u.UpdatedAt != now.UnixMilli() || len(u.Windows) != 2 || u.Windows[0] != want[0] || u.Windows[1] != want[1] {
		t.Fatalf("got %+v", u)
	}
	// API key 用户没有 rate_limits
	if _, ok := ClaudeStatusLine([]byte(`{"session_id":"s"}`), now); ok {
		t.Fatal("usage without rate_limits")
	}
}

func TestClaudeCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	os.WriteFile(path, []byte(`{"numStartups":3,"cachedUsageUtilization":{"fetchedAtMs":1792419173259,"accountUuid":"a",
		"utilization":{"five_hour":{"utilization":52,"resets_at":"2026-10-19T14:40:00.065996+00:00","limit_dollars":null},
		"seven_day":{"utilization":21,"resets_at":"2026-10-24T21:00:00.066020+00:00"},"seven_day_opus":null,"limits":[]}}}`), 0o600)
	u, ok := ClaudeCache(path)
	if !ok || u.UpdatedAt != 1792419173259 || len(u.Windows) != 2 {
		t.Fatalf("got %+v %v", u, ok)
	}
	if w := u.Windows[0]; w.Percent != 52 || w.Minutes != 300 || w.ResetsAt != time.Date(2026, 10, 19, 14, 40, 0, 65996000, time.UTC).UnixMilli() {
		t.Fatalf("five hour %+v", w)
	}
}

func TestCodexRollout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	lines := []string{
		`{"timestamp":"2026-10-19T05:50:00.000Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","primary":{"used_percent":6.0,"window_minutes":300,"resets_at":1792402032},"secondary":null,"plan_type":"plus"}}}`,
		`{"timestamp":"2026-10-19T05:51:57.408Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":179291}},"rate_limits":{"limit_id":"codex","limit_name":null,"primary":{"used_percent":7.0,"window_minutes":300,"resets_at":1792402032},"secondary":{"used_percent":8.0,"window_minutes":10080,"resets_at":1792900210},"credits":{"has_credits":false},"plan_type":"plus"}}}`,
		// 单独计的模型额度不算账号总额度
		`{"timestamp":"2026-10-19T05:52:00.000Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex_other","primary":{"used_percent":90,"window_minutes":300,"resets_at":1792402032}}}}`,
		`{"timestamp":"2026-10-19T05:53:00.000Z","type":"response_item","payload":{"type":"message","content":"rate_limits"}}`,
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	u, ok := CodexRollout(path)
	if !ok {
		t.Fatal("no usage")
	}
	if u.Plan != "Plus" || len(u.Windows) != 2 || u.Windows[0].Percent != 7 || u.Windows[1].Minutes != 10080 || u.Windows[1].ResetsAt != 1792900210000 {
		t.Fatalf("got %+v", u)
	}
	if u.UpdatedAt != time.Date(2026, 10, 19, 5, 51, 57, 408e6, time.UTC).UnixMilli() {
		t.Fatalf("updated %d", u.UpdatedAt)
	}
}

func TestCodexLatest(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "10", "19")
	os.MkdirAll(day, 0o700)
	old := filepath.Join(home, "sessions", "2026", "09", "30")
	os.MkdirAll(old, 0o700)
	os.WriteFile(filepath.Join(old, "a.jsonl"), []byte(`{"timestamp":"2026-09-30T01:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":50,"window_minutes":300,"resets_at":1}}}}`+"\n"), 0o600)
	// 最新的会话还没有一轮对话，没有限额
	os.WriteFile(filepath.Join(day, "b.jsonl"), []byte(`{"timestamp":"2026-10-19T01:00:00Z","type":"session_meta","payload":{}}`+"\n"), 0o600)
	u, ok := CodexLatest(home)
	if !ok || u.Windows[0].Percent != 50 {
		t.Fatalf("got %+v %v", u, ok)
	}
}

const grokBilling = `{"ts":"2026-10-19T15:32:06.128Z","src":"shell","pid":78169,"ver":"1.0.63","lvl":"info","msg":"billing: fetched credits config","ctx":{"config":{"creditUsagePercent":%s,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-16T10:44:08.944215+00:00","end":"2026-10-23T10:44:08.944215+00:00"},"onDemandCap":{"val":0},"onDemandUsed":{"val":0}},"onDemandEnabled":null,"subscriptionTier":"SuperGrok Heavy"}}`

func TestGrokLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unified.jsonl")
	write := func(s string) {
		f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		f.WriteString(s)
		f.Close()
	}
	write(strings.Replace(grokBilling, "%s", "46.0", 1) + "\n" + `{"ts":"2026-10-19T15:32:07Z","msg":"turn.inference_start"}` + "\n")
	g := NewGrokLog(path)
	u, ok := g.Read()
	if !ok || u.Plan != "SuperGrok Heavy" || u.Windows[0].Percent != 46 || u.Windows[0].Minutes != 7*24*60 {
		t.Fatalf("got %+v %v", u, ok)
	}
	if _, ok := g.Read(); ok {
		t.Fatal("read the same line twice")
	}
	// 写到一半的行留到下次
	line := strings.Replace(grokBilling, "%s", "53.0", 1)
	write(line[:40])
	if _, ok := g.Read(); ok {
		t.Fatal("parsed a partial line")
	}
	write(line[40:] + "\n")
	if u, ok := g.Read(); !ok || u.Windows[0].Percent != 53 {
		t.Fatalf("got %+v %v", u, ok)
	}
	// 日志被裁短：从末尾重找
	os.WriteFile(path, []byte(strings.Replace(grokBilling, "%s", "1", 1)+"\n"), 0o600)
	if u, ok := g.Read(); !ok || u.Windows[0].Percent != 1 {
		t.Fatalf("after truncate %+v %v", u, ok)
	}
}

func TestCursorUsage(t *testing.T) {
	now := time.UnixMilli(1792430000000)
	raw := `{"billingCycleStart":"1790812800000","billingCycleEnd":"1793491200000","planUsage":{"totalSpend":3255,"includedSpend":3255,"remaining":36745,"limit":40000,"autoPercentUsed":2.1,"apiPercentUsed":9.5,"totalPercentUsed":8.1375},"spendLimitUsage":{"individualLimit":0},"enabled":true}`
	u, ok := cursorUsage([]byte(raw), now)
	if !ok {
		t.Fatal("no usage")
	}
	w := u.Windows[0]
	if w.Percent != 8.1375 || w.ResetsAt != 1793491200000 || w.Minutes != 31*24*60 || u.Spend == nil || u.Spend.Used != 3255 || u.Spend.Limit != 40000 {
		t.Fatalf("got %+v %+v", u, u.Spend)
	}
	// 没有百分比时按金额算
	u, _ = cursorUsage([]byte(`{"planUsage":{"limit":2000,"remaining":500}}`), now)
	if u.Windows[0].Percent != 75 {
		t.Fatalf("computed %+v", u)
	}
	if _, ok := cursorUsage([]byte(`{"enabled":false,"planUsage":{"limit":1}}`), now); ok {
		t.Fatal("usage for a disabled plan")
	}
}

func TestTokenExpired(t *testing.T) {
	now := time.Unix(2000, 0)
	jwt := func(payload string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".s"
	}
	if !tokenExpired(jwt(`{"exp":1000}`), now) || tokenExpired(jwt(`{"exp":9000}`), now) || tokenExpired("opaque", now) {
		t.Fatal("expiry")
	}
}

func TestStore(t *testing.T) {
	home := t.TempDir()
	s := Open(home)
	five := Window{Percent: 10, Minutes: 300, ResetsAt: 5000}
	if !s.Update(Usage{Agent: "codex", Windows: []Window{five}, UpdatedAt: 1000}) {
		t.Fatal("first update")
	}
	if !s.Update(Usage{Agent: "claude", Windows: []Window{five}, UpdatedAt: 1000}) {
		t.Fatal("claude update")
	}
	// 旧读数、没变化又太近的读数都不算
	if s.Update(Usage{Agent: "codex", Windows: []Window{{Percent: 99}}, UpdatedAt: 900}) {
		t.Fatal("older reading accepted")
	}
	if s.Update(Usage{Agent: "codex", Windows: []Window{five}, UpdatedAt: 2000}) {
		t.Fatal("same reading accepted")
	}
	list := s.List(time.UnixMilli(3000))
	if len(list) != 2 || list[0].Agent != "claude" || list[1].Agent != "codex" {
		t.Fatalf("order %+v", list)
	}
	// 过了重置时间归零
	if w := s.List(time.UnixMilli(6000))[0].Windows[0]; w.Percent != 0 || w.ResetsAt != 0 || w.Minutes != 300 {
		t.Fatalf("after reset %+v", w)
	}
	s.SetCursorOff(true)
	again := Open(home)
	if len(again.List(time.UnixMilli(3000))) != 2 || !again.CursorOff() {
		t.Fatal("not persisted")
	}
}

func TestClaudeCacheExtras(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	os.WriteFile(path, []byte(`{"cachedUsageUtilization":{"fetchedAtMs":1000,"utilization":{
		"five_hour":{"utilization":52,"resets_at":"2026-10-19T14:40:00Z"},"seven_day":{"utilization":21,"resets_at":"2026-10-24T21:00:00Z"},
		"seven_day_opus":null,
		"limits":[{"kind":"session","percent":52},{"kind":"weekly_scoped","percent":12,"resets_at":"2026-10-24T21:00:00Z","scope":{"model":{"id":null,"display_name":"Fable"}}}],
		"seven_day_breakdown":{"rows":[{"display_name":"Claude Code","percent":100},{"display_name":"Chats","percent":0}]},
		"spend":{"used":{"amount_minor":1234,"currency":"USD","exponent":2},"limit":{"amount_minor":5000,"exponent":2},"enabled":true}}}}`), 0o600)
	u, ok := ClaudeCache(path)
	if !ok || len(u.Windows) != 3 {
		t.Fatalf("got %+v", u)
	}
	if f := u.Windows[2]; f.Name != "Fable" || f.Percent != 12 || f.Minutes != 10080 {
		t.Fatalf("fable %+v", f)
	}
	if u.Shares == nil || len(u.Shares.Items) != 1 || u.Shares.Items[0].Name != "Claude Code" {
		t.Fatalf("shares %+v", u.Shares)
	}
	if u.OnDemand == nil || u.OnDemand.Used != 1234 || u.OnDemand.Limit != 5000 {
		t.Fatalf("on demand %+v", u.OnDemand)
	}
}

// statusLine 每轮报上来的只有 5 小时和每周；之前从缓存读到的 Fable、用量分布留着，带它们自己的读数时间。
func TestMergeKeepsScopedWindows(t *testing.T) {
	s := Open(t.TempDir())
	s.Update(Usage{Agent: "claude", UpdatedAt: 1000, Windows: []Window{{Percent: 52, Minutes: 300}, {Percent: 12, Minutes: 10080, Name: "Fable"}},
		Shares: &Shares{Items: []Share{{Name: "Claude Code", Percent: 100}}}})
	if !s.Update(Usage{Agent: "claude", UpdatedAt: 5000, Windows: []Window{{Percent: 60, Minutes: 300}, {Percent: 30, Minutes: 10080}}}) {
		t.Fatal("newer reading rejected")
	}
	u := s.List(time.UnixMilli(6000))[0]
	if u.UpdatedAt != 5000 || len(u.Windows) != 3 || u.Windows[0].Percent != 60 {
		t.Fatalf("got %+v", u)
	}
	if f := u.Windows[2]; f.Name != "Fable" || f.At != 1000 {
		t.Fatalf("fable %+v", f)
	}
	if u.Shares == nil || u.Shares.At != 1000 {
		t.Fatalf("shares %+v", u.Shares)
	}
	// 更旧的缓存读数只更新它自己更新的分池，不动总额度
	s.Update(Usage{Agent: "claude", UpdatedAt: 2000, Windows: []Window{{Percent: 1, Minutes: 300}, {Percent: 15, Minutes: 10080, Name: "Fable"}}})
	u = s.List(time.UnixMilli(6000))[0]
	if u.Windows[0].Percent != 60 || u.Windows[2].Percent != 15 || u.Windows[2].At != 2000 {
		t.Fatalf("after old cache %+v", u)
	}
}

// 新读数没带 5 小时窗口时沿用旧的，已过重置时间的算清零；位置仍在每周前面。
func TestMergeKeepsFiveHour(t *testing.T) {
	s := Open(t.TempDir())
	s.Update(Usage{Agent: "claude", UpdatedAt: 1000, Windows: []Window{{Percent: 52, Minutes: 300, ResetsAt: 9000}, {Percent: 40, Minutes: 10080}}})
	s.Update(Usage{Agent: "claude", UpdatedAt: 5000, Windows: []Window{{Percent: 41, Minutes: 10080}}})
	u := s.List(time.UnixMilli(6000))[0]
	if len(u.Windows) != 2 || u.Windows[0].Minutes != 300 || u.Windows[0].Percent != 52 || u.Windows[1].Percent != 41 {
		t.Fatalf("got %+v", u.Windows)
	}
	s.Update(Usage{Agent: "claude", UpdatedAt: 10000, Windows: []Window{{Percent: 42, Minutes: 10080}}})
	u = s.List(time.UnixMilli(11000))[0]
	if w := u.Windows[0]; w.Minutes != 300 || w.Percent != 0 || w.ResetsAt != 0 {
		t.Fatalf("after reset %+v", u.Windows)
	}
}

func TestCursorPools(t *testing.T) {
	raw := `{"billingCycleStart":"1790812800000","billingCycleEnd":"1793491200000","planUsage":{"totalSpend":4110,"limit":40000,"autoPercentUsed":2.5,"apiPercentUsed":18.2,"totalPercentUsed":10.3},"spendLimitUsage":{"individualUsed":250,"individualLimit":2000}}`
	u, ok := cursorUsage([]byte(raw), time.UnixMilli(1792430000000))
	if !ok || len(u.Windows) != 3 || u.Windows[1].Name != "Auto" || u.Windows[2].Name != "API" || u.Windows[2].Percent != 18.2 {
		t.Fatalf("got %+v", u)
	}
	if u.OnDemand == nil || u.OnDemand.Used != 250 || u.OnDemand.Limit != 2000 {
		t.Fatalf("on demand %+v", u.OnDemand)
	}
}

// 回应照 codex app-server 0.163 的 account/rateLimits/read 实际返回（去掉了 ID）。
func TestCodexRateLimits(t *testing.T) {
	raw := `{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex","primary":{"usedPercent":2,"windowDurationMins":300,"resetsAt":1792446937},"secondary":{"usedPercent":8,"windowDurationMins":10080,"resetsAt":1792900210},"credits":{"hasCredits":false},"planType":"plus"},
		"rateLimitResetCredits":{"availableCount":3,"credits":[{"status":"available","expiresAt":1793292156},{"status":"available","expiresAt":1792693790},{"status":"redeemed","expiresAt":1790000000}]}}`
	u, ok := codexRateLimits([]byte(raw), time.UnixMilli(1792430000000))
	if !ok || u.Plan != "Plus" || len(u.Windows) != 2 || u.Windows[0].Percent != 2 {
		t.Fatalf("got %+v", u)
	}
	if u.Resets == nil || u.Resets.Count != 3 || u.Resets.ExpiresAt != 1792693790000 {
		t.Fatalf("resets %+v", u.Resets)
	}
}

func TestCursorPlan(t *testing.T) {
	raw := `{"planInfo":{"planName":"Ultra","price":"$200/mo","billingCycleEnd":"1794853147000"}}`
	if got := cursorPlan([]byte(raw)); got != "Ultra $200/月" {
		t.Fatalf("plan = %q", got)
	}
	if got := cursorPlan([]byte(`{"planInfo":{"planName":"Pro"}}`)); got != "Pro" {
		t.Fatalf("plan = %q", got)
	}
}

// Claude 的套餐名取 oauthAccount：类型加限额倍数；statusLine 的读数不带套餐名，合并时沿用缓存里的。
func TestClaudePlan(t *testing.T) {
	for _, c := range []struct{ typ, tier, want string }{
		{"claude_max", "default_claude_max_5x", "Max 5x"},
		{"claude_max", "default_claude_max_20x", "Max 20x"},
		{"claude_pro", "default_claude_ai", "Pro"},
		{"claude_team", "", "Team"},
		{"", "default_claude_max_5x", ""},
	} {
		if got := claudePlan(c.typ, c.tier); got != c.want {
			t.Errorf("claudePlan(%q, %q) = %q, want %q", c.typ, c.tier, got, c.want)
		}
	}
	path := filepath.Join(t.TempDir(), ".claude.json")
	os.WriteFile(path, []byte(`{"oauthAccount":{"organizationType":"claude_max","organizationRateLimitTier":"default_claude_max_5x","userRateLimitTier":null},
		"cachedUsageUtilization":{"fetchedAtMs":1000,"utilization":{"five_hour":{"utilization":5,"resets_at":"2026-10-19T14:40:00Z"}}}}`), 0o600)
	cache, ok := ClaudeCache(path)
	if !ok || cache.Plan != "Max 5x" {
		t.Fatalf("cache plan %q %v", cache.Plan, ok)
	}
	line, _ := ClaudeStatusLine([]byte(`{"rate_limits":{"five_hour":{"used_percentage":9,"resets_at":1792402032}}}`), time.UnixMilli(2000))
	if got := merge(cache, line); got.Plan != "Max 5x" || got.Windows[0].Percent != 9 {
		t.Fatalf("merged %+v", got)
	}
}
