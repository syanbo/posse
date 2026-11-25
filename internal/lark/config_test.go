package lark

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBotConfigReadOnlyHostsAndTrustedScripts(t *testing.T) {
	home := t.TempDir()

	// 兼容旧版 lark-bots.json（不含 readOnlyHosts / trustedScripts 字段）
	legacy := `{
  "ownerOpenId": "ou_owner",
  "bots": [
    {
      "id": "lbot_legacy",
      "name": "Legacy",
      "profile": "cli_legacy",
      "enabled": true,
      "permissionMode": "ask"
    }
  ]
}`
	if err := os.WriteFile(ConfigPath(home), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig legacy: %v", err)
	}
	if len(loaded.Bots) != 1 || len(loaded.Bots[0].ReadOnlyHosts) != 0 || len(loaded.Bots[0].TrustedScripts) != 0 {
		t.Fatalf("unexpected legacy bot: %+v", loaded.Bots)
	}

	// NormalizeBot 规范化：host 小写去重；trustedScripts 仅保留绝对路径并保留目录尾部 /
	bot := NormalizeBot(BotConfig{
		ID:      "lbot_1",
		Profile: "cli_1",
		ReadOnlyHosts: []string{
			" Sentry.Corp.Laoyuegou.com ",
			"sentry.corp.laoyuegou.com",
			"",
			"api.example.com:8443",
		},
		TrustedScripts: []string{
			"/Users/dengbo/scripts/download-log.sh",
			"/Users/dengbo/scripts//download-log.sh",
			"/Users/dengbo/trusted-dir/",
			"/Users/dengbo/trusted-dir//",
			"relative/bad.sh",
			"./bad.sh",
			"",
		},
	})
	if len(bot.ReadOnlyHosts) != 2 || bot.ReadOnlyHosts[0] != "sentry.corp.laoyuegou.com" || bot.ReadOnlyHosts[1] != "api.example.com:8443" {
		t.Fatalf("unexpected ReadOnlyHosts: %+v", bot.ReadOnlyHosts)
	}
	if len(bot.TrustedScripts) != 2 || bot.TrustedScripts[0] != "/Users/dengbo/scripts/download-log.sh" || bot.TrustedScripts[1] != "/Users/dengbo/trusted-dir/" {
		t.Fatalf("unexpected TrustedScripts: %+v", bot.TrustedScripts)
	}

	if err := SaveConfig(home, Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	reloaded, err := LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig reloaded: %v", err)
	}
	if len(reloaded.Bots) != 1 || len(reloaded.Bots[0].ReadOnlyHosts) != 2 || len(reloaded.Bots[0].TrustedScripts) != 2 {
		t.Fatalf("unexpected reloaded bot: %+v", reloaded.Bots)
	}
}

func TestValidateBotPolicy(t *testing.T) {
	validScript := filepath.Join(t.TempDir(), "fetch.sh")
	validDir := t.TempDir() + "/"
	if err := ValidateBotPolicy(
		[]string{"sentry.corp.laoyuegou.com", "gitlab.corp.laoyuegou.com:8443"},
		[]string{validScript, validDir},
	); err != nil {
		t.Fatalf("expected valid policy, got %v", err)
	}

	badHosts := []string{
		"",
		"https://sentry.corp.laoyuegou.com",
		"sentry.corp.laoyuegou.com/api",
		"a.com,b.com",
		"a.com b.com",
		"sentry.corp.laoyuegou.com:0",
		"sentry.corp.laoyuegou.com:70000",
		"sentry.corp.laoyuegou.com:abc",
		"sentry.corp.laoyuegou.com:",
	}
	for _, h := range badHosts {
		if err := ValidateBotPolicy([]string{h}, nil); err == nil {
			t.Errorf("expected invalid read-only host error for %q", h)
		}
	}

	tmpLink := filepath.Join(t.TempDir(), "to-tmp")
	if err := os.Symlink("/tmp", tmpLink); err != nil {
		t.Fatal(err)
	}
	badScripts := []string{
		"",
		"relative/bad.sh",
		"./bad.sh",
		"/Users/dengbo/../etc/a.sh",
		"/Users/dengbo/a:b.sh",
		"/Users/dengbo/$HOME/a.sh",
		"~/a.sh",
		"/Users/dengbo/with space.sh",
		"/",
		"/tmp",
		"/tmp/a.sh",
		"/TMP/a.sh",
		"/tmp/",
		"/private/tmp/a.sh",
		"/PRIVATE/TMP/a.sh",
		"/private/",
		"/PRIVATE/",
		filepath.Join(tmpLink, "a.sh"),
	}
	for _, s := range badScripts {
		if err := ValidateBotPolicy(nil, []string{s}); err == nil {
			t.Errorf("expected invalid trusted script error for %q", s)
		}
	}
}
