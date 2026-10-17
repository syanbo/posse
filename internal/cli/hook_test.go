package cli

import (
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
