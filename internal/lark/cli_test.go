package lark

import (
	"bufio"
	"strings"
	"testing"
)

func TestParseConsumeStderr(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name: "pretty printed json envelope with interleaved warn",
			lines: []string{
				"[lark-cli] [WARN] proxy detected",
				"{",
				`  "ok": false,`,
				"[lark-cli] [WARN] another warning",
				`  "error": {`,
				`    "type": "validation",`,
				`    "message": "unknown EventKey: card.action.trigger",`,
				`    "hint": "Run 'lark-cli event list' to see available keys."`,
				`  }`,
				"}",
			},
			want: "unknown EventKey: card.action.trigger (Run 'lark-cli event list' to see available keys.)",
		},
		{
			name: "single line json envelope",
			lines: []string{
				`{"ok":false,"error":{"message":"missing scope","hint":"re-auth"}}`,
			},
			want: "missing scope (re-auth)",
		},
		{
			name: "non-envelope json without ok field falls back to plain text",
			lines: []string{
				`{"error":{"message":"diagnostic only"}}`,
			},
			want: `{"error":{"message":"diagnostic only"}}`,
		},
		{
			name: "plain text error ignoring warn",
			lines: []string{
				"[lark-cli] [WARN] proxy detected",
				"env: node: No such file or directory",
			},
			want: "env: node: No such file or directory",
		},
		{
			name:  "only warnings returns empty",
			lines: []string{"[lark-cli] [WARN] proxy detected"},
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseConsumeStderr(tc.lines); got != tc.want {
				t.Fatalf("parseConsumeStderr() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadCappedLineDrainsLongLine(t *testing.T) {
	longLine := strings.Repeat("a", 600*1024) + "\nnext line\n"
	rd := bufio.NewReaderSize(strings.NewReader(longLine), 4096)

	got1, eof1 := readCappedLine(rd, 1024)
	if eof1 {
		t.Fatalf("expected non-EOF after first line")
	}
	if len(got1) != 1024 {
		t.Fatalf("expected capped length 1024, got %d", len(got1))
	}
	got2, _ := readCappedLine(rd, 1024)
	if strings.TrimSpace(got2) != "next line" {
		t.Fatalf("expected second line after draining long line, got %q", got2)
	}
}

func TestParseBotInfoAndAuthStatusIdentity(t *testing.T) {
	openID, appName, err := parseBotInfo([]byte(`{
		"ok": true,
		"identity": "bot",
		"data": {
			"activate_status": 2,
			"app_name": "Feishu CLI",
			"open_id": "ou_bot_real"
		}
	}`))
	if err != nil || openID != "ou_bot_real" || appName != "Feishu CLI" {
		t.Fatalf("parseBotInfo(data) = (%q, %q, %v), want (ou_bot_real, Feishu CLI, nil)", openID, appName, err)
	}

	openID, appName, err = parseBotInfo([]byte(`{"bot":{"open_id":"ou_raw","app_name":"Raw Bot"}}`))
	if err != nil || openID != "ou_raw" || appName != "Raw Bot" {
		t.Fatalf("parseBotInfo(bot) = (%q, %q, %v), want (ou_raw, Raw Bot, nil)", openID, appName, err)
	}

	if _, _, err := parseBotInfo([]byte(`{"ok":false,"error":{"message":"forbidden"}}`)); err == nil {
		t.Fatalf("parseBotInfo(ok=false) expected error")
	}
	if _, _, err := parseBotInfo([]byte(`{"ok":true,"data":{}}`)); err == nil {
		t.Fatalf("parseBotInfo(empty data) expected error")
	}

	appID, openID, appName, err := parseAuthStatusIdentity([]byte(`{
		"appId": "cli_a945f8f32b38dcd4",
		"identities": {
			"bot": {"status": "ready", "available": true}
		}
	}`))
	if err != nil || appID != "cli_a945f8f32b38dcd4" || openID != "" || appName != "" {
		t.Fatalf("parseAuthStatusIdentity() = (%q, %q, %q, %v)", appID, openID, appName, err)
	}
	if _, _, _, err := parseAuthStatusIdentity([]byte(`{"identities":{"bot":{"status":"ready"}}}`)); err == nil {
		t.Fatalf("parseAuthStatusIdentity(empty fields) expected error")
	}
}
