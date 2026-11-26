package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/lark"
	"posse/internal/store"
)

func TestAgentModelsRPCCacheRefreshSingleflightAndCancel(t *testing.T) {
	binDir := t.TempDir()
	counterFile := filepath.Join(t.TempDir(), "calls.log")
	fakeAgy := filepath.Join(binDir, "agy")
	script := `#!/bin/sh
echo "call" >> "` + counterFile + `"
if [ "$1" = "models" ]; then
  sleep 0.15
  printf 'Fetching available models...\n'
  printf 'gemini-4-neon-high\tGemini 4 Neon (High)\n'
  printf 'gemini-4-neon-low\tGemini 4 Neon (Low)\n'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(fakeAgy, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	d, _ := newTestDaemon(t)
	d.agents.mu.Lock()
	d.agents.installed = []string{"antigravity"}
	d.agents.path = binDir + ":" + os.Getenv("PATH")
	d.agents.mu.Unlock()

	// 1. 并发 singleflight 合并：两次并发调用只执行一次假 CLI
	var wg sync.WaitGroup
	results := make([]agent.ModelList, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = d.fetchAgentModels(context.Background(), "antigravity", false)
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r.Error != "" || len(r.Models) != 1 || r.Models[0].ID != "gemini-4-neon" {
			t.Fatalf("result[%d] unexpected: %+v", i, r)
		}
	}
	rawCount, _ := os.ReadFile(counterFile)
	if calls := countLines(string(rawCount)); calls != 1 {
		t.Fatalf("expected 1 CLI call from concurrent singleflight, got %d", calls)
	}

	// 2. 缓存命中：再次调用不触发 CLI
	cached := d.fetchAgentModels(context.Background(), "antigravity", false)
	if len(cached.Models) != 1 {
		t.Fatalf("cached result unexpected: %+v", cached)
	}
	rawCount, _ = os.ReadFile(counterFile)
	if calls := countLines(string(rawCount)); calls != 1 {
		t.Fatalf("expected cache hit without CLI call, got %d calls", calls)
	}

	// 3. refresh=true 绕过缓存
	refreshed := d.fetchAgentModels(context.Background(), "antigravity", true)
	if len(refreshed.Models) != 1 {
		t.Fatalf("refreshed result unexpected: %+v", refreshed)
	}
	rawCount, _ = os.ReadFile(counterFile)
	if calls := countLines(string(rawCount)); calls != 2 {
		t.Fatalf("expected refresh to invoke CLI (2 total), got %d", calls)
	}

	// 4. ctx 取消时及时中断慢子进程
	slowScript := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(fakeAgy, []byte(slowScript), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := d.fetchAgentModels(ctx, "antigravity", true)
	elapsed := time.Since(start)
	if res.Error == "" {
		t.Fatalf("expected error on cancelled ctx")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("ctx cancel took too long: %v", elapsed)
	}
}

func countLines(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}

func TestLarkModelValidationReconcileAndSwitchAgent(t *testing.T) {
	d, w := newTestDaemon(t)

	// 1. larkBotSave 拒绝 cursor + effort
	badPayload, _ := json.Marshal(map[string]any{
		"bot": lark.BotConfig{
			Profile: "p1",
			Agent:   "cursor",
			Model:   "gpt-5.3-codex",
			Effort:  "high",
		},
	})
	if _, err := d.larkBotSave(context.Background(), badPayload); err == nil {
		t.Fatalf("expected larkBotSave to reject cursor + effort")
	}

	// 2. 正常保存机器人后，测试 SwitchAgent 跨 Agent 时清空旧模型与思考深度
	okPayload, _ := json.Marshal(map[string]any{
		"bot": lark.BotConfig{
			ID:      "lbot_m1",
			Name:    "Bot",
			Profile: "p1",
			Agent:   "claude",
			Model:   "opus",
			Effort:  "high",
		},
	})
	if _, err := d.larkBotSave(context.Background(), okPayload); err != nil {
		t.Fatalf("larkBotSave: %v", err)
	}

	host := larkHost{d: d}
	b, err := host.EnsureDiscussion(lark.BotConfig{
		ID:          "lbot_m1",
		Name:        "Bot",
		Agent:       "claude",
		Model:       "opus",
		Effort:      "high",
		WorkspaceID: w.ID,
	}, "oc_switch", "group", "", "Demo")
	if err != nil {
		t.Fatalf("EnsureDiscussion: %v", err)
	}
	d.agents.mu.Lock()
	d.agents.installed = []string{"claude", "codex"}
	d.agents.mu.Unlock()
	if _, err := host.SwitchAgent(b, "codex", ""); err != nil {
		t.Fatalf("SwitchAgent: %v", err)
	}
	m, err := d.store.Member(b.MemberID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Agent != "codex" || m.Model != "" || m.Effort != "" {
		t.Fatalf("SwitchAgent across agents must clear old model/effort, got: %+v", m)
	}

	// 3. 群分身把 model 从 nil 改成显式 ""（恢复 Agent 默认）应触发旧绑定退役
	empty := ""
	chatPayload, _ := json.Marshal(map[string]any{
		"botId": "lbot_m1",
		"chat": lark.ChatOverride{
			ChatID: "oc_switch",
			Model:  &empty,
		},
	})
	if _, err := d.larkBotChatSave(context.Background(), chatPayload); err != nil {
		t.Fatalf("larkBotChatSave: %v", err)
	}
	if _, err := d.store.ActiveLarkBinding("lbot_m1", "oc_switch", ""); err != store.ErrNotFound {
		t.Fatalf("expected active binding to be deactivated after model override change, got err=%v", err)
	}

	// 4. B1 回归：已缓存目录知晓 gemini-4-carbon 不支持 xhigh 时，群分身继承父级 xhigh 必须被拒绝
	d.models.mu.Lock()
	if d.models.cache == nil {
		d.models.cache = map[string]cachedModelList{}
	}
	d.models.cache["antigravity"] = cachedModelList{
		list: agent.ModelList{
			Agent: "antigravity",
			Models: []agent.ModelOption{
				{ID: "gemini-4-neon", Label: "Gemini 4 Neon", Efforts: []string{"low", "medium", "high", "xhigh"}},
				{ID: "gemini-4-carbon", Label: "Gemini 4 Carbon", Efforts: []string{"low", "medium", "high"}},
			},
		},
		expires: time.Now().Add(time.Minute),
	}
	d.models.mu.Unlock()
	agyBotPayload, _ := json.Marshal(map[string]any{
		"bot": lark.BotConfig{
			ID:      "lbot_agy",
			Name:    "AgyBot",
			Profile: "p1",
			Agent:   "antigravity",
			Model:   "gemini-4-neon",
			Effort:  "xhigh",
		},
	})
	if _, err := d.larkBotSave(context.Background(), agyBotPayload); err != nil {
		t.Fatalf("larkBotSave agy: %v", err)
	}
	carbon := "gemini-4-carbon"
	badChatPayload, _ := json.Marshal(map[string]any{
		"botId": "lbot_agy",
		"chat": lark.ChatOverride{
			ChatID: "oc_carbon",
			Agent:  "antigravity",
			Model:  &carbon,
			Effort: nil, // 继承父级 xhigh -> 与 carbon 冲突
		},
	})
	if _, err := d.larkBotChatSave(context.Background(), badChatPayload); err == nil {
		t.Fatalf("expected larkBotChatSave to reject inherited xhigh on gemini-4-carbon")
	}

	// 5. 同 Agent 的 SwitchAgent 传新模型时必须清空旧档位
	b2, err := host.EnsureDiscussion(lark.BotConfig{
		ID:          "lbot_agy",
		Name:        "AgyBot",
		Agent:       "antigravity",
		Model:       "gemini-4-neon",
		Effort:      "xhigh",
		WorkspaceID: w.ID,
	}, "oc_same_ag", "group", "", "SameAg")
	if err != nil {
		t.Fatalf("EnsureDiscussion same agent: %v", err)
	}
	d.agents.mu.Lock()
	d.agents.installed = []string{"antigravity", "claude", "codex"}
	d.agents.mu.Unlock()
	if _, err := host.SwitchAgent(b2, "antigravity", "gpt-oss-120b"); err != nil {
		t.Fatalf("SwitchAgent same agent new model: %v", err)
	}
	m2, err := d.store.Member(b2.MemberID)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Model != "gpt-oss-120b" || m2.Effort != "" {
		t.Fatalf("SwitchAgent same agent with new model must clear old effort, got: %+v", m2)
	}
}
