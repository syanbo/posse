package usage

import (
	"testing"
	"time"
)

func TestAntigravityQuotaWindows(t *testing.T) {
	now := time.Date(2026, 11, 23, 5, 0, 0, 0, time.UTC)
	u, err := parseAntigravityUsage([]byte(`{
		"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[
			{"name":"Gemini Models","buckets":[
				{"id":"gemini-week","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":0.75,"reset_time":"2026-11-24T05:00:00Z"},
				{"id":"gemini-5h","name":"Limit Remaining","window":"five_hours","remaining_fraction":0.5},
				{"id":"disabled","remaining_fraction":0,"disabled":true},
				{"id":"missing","name":"Missing Fraction"}
			]},
			{"name":"Claude and GPT models","buckets":[
				{"id":"other-week","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":0}
			]}
		]}}}`), now)
	if err != nil {
		t.Fatal(err)
	}
	if u.Agent != "antigravity" || len(u.Windows) != 3 || u.UpdatedAt != now.UnixMilli() {
		t.Fatalf("invalid reading: %+v", u)
	}
	if w := u.Windows[0]; w.Name != "Gemini Models" || w.Percent != 25 || w.Minutes != 10080 || w.ResetsAt != now.Add(24*time.Hour).UnixMilli() {
		t.Fatalf("invalid window: %+v", w)
	}
	store := Open(t.TempDir())
	store.Update(u)
	got := store.List(now.Add(25 * time.Hour))[0]
	if len(got.Windows) != 3 || !got.Windows[0].Unknown || got.Windows[1].Minutes != 300 || got.Windows[2].Percent != 100 {
		t.Fatalf("window lost or reset incorrectly: %+v", got.Windows)
	}
}

func TestAntigravityRejectsUnrelatedOutput(t *testing.T) {
	for _, raw := range []string{
		`{"status":"SUCCESS","num_turns":1,"command":{"name":"usage"}}`,
		`{"status":"SUCCESS","num_turns":0,"response":"No quota data"}`,
		`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[]}}}`,
	} {
		if u, err := parseAntigravityUsage([]byte(raw), time.Now()); err == nil || u.Agent != "" {
			t.Fatalf("unexpected reading from %s: %+v, %v", raw, u, err)
		}
	}
}

func TestAntigravityStatusQuotaPreservesOtherWindows(t *testing.T) {
	s := Open(t.TempDir())
	now := time.Now().Truncate(time.Second)
	reset := now.Add(time.Hour)
	s.Update(Usage{Agent: "antigravity", UpdatedAt: now.UnixMilli(), Windows: []Window{
		{QuotaID: "a", Name: "Gemini", Percent: 10, Minutes: 10080, ResetsAt: reset.UnixMilli()},
		{QuotaID: "b", Name: "Claude and GPT", Percent: 20, Minutes: 300, ResetsAt: reset.UnixMilli()},
	}})
	remaining := 0.75
	update := map[string]AntigravityStatusQuota{"a": {Remaining: &remaining, Reset: reset.Format(time.RFC3339)}}
	if !s.UpdateAntigravityStatusQuota(update, now.Add(time.Second)) {
		t.Fatal("new quota was ignored")
	}
	remaining = 0.875
	if s.UpdateAntigravityStatusQuota(update, now.Add(2*time.Second)) {
		t.Fatal("another process's cached quota reversed the reading")
	}
	windows := s.List(now)[0].Windows
	if len(windows) != 2 || windows[0].Name != "Gemini" || windows[0].Percent != 25 || windows[1].Percent != 20 {
		t.Fatalf("partial status line replaced named windows: %+v", windows)
	}
}
