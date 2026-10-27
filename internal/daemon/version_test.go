package daemon

import (
	"encoding/json"
	"os"
	"testing"
)

// 自动更新按 mygo.json 的 version 比较新旧，CLI 和设置页显示的是 Version：发布前两处要一起改。
func TestVersionMatchesPackage(t *testing.T) {
	b, err := os.ReadFile("../../mygo.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Version != Version {
		t.Fatalf("mygo.json version %q, daemon.Version %q", cfg.Version, Version)
	}
}
