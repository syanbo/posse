package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPermissionDecision(t *testing.T) {
	allow := permissionDecision("allow")
	var doc map[string]any
	if err := json.Unmarshal([]byte(allow), &doc); err != nil {
		t.Fatal(err)
	}
	out := doc["hookSpecificOutput"].(map[string]any)
	if out["hookEventName"] != "PermissionRequest" {
		t.Fatalf("event %v", out["hookEventName"])
	}
	if out["decision"].(map[string]any)["behavior"] != "allow" {
		t.Fatalf("decision %v", out["decision"])
	}
	if permissionDecision("deny") == "" || permissionDecision("") != "" || permissionDecision("ask") != "" {
		t.Fatal("only allow and deny are written")
	}
}

// hook 参数不足也不能让 agent 看到失败：退出码 0，Cursor 照样拿到 {}。
func TestHookMissingArgs(t *testing.T) {
	for _, args := range [][]string{nil, {"claude"}, {"cursor"}} {
		var out, errOut bytes.Buffer
		e := &env{out: &out, errOut: &errOut}
		if err := cmdHook(e, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		want := ""
		if len(args) == 1 && args[0] == "cursor" {
			want = "{}\n"
		}
		if out.String() != want {
			t.Fatalf("%v: stdout %q, want %q", args, out.String(), want)
		}
	}
}
