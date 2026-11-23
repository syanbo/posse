package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAntigravityDirectoryBackfillBeforePrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "posse.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	if err := s.AddProject(Project{ID: "p_test", Name: "test", Path: "/project", Kind: "folder", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorkspace(Workspace{ID: "w_test", ProjectID: "p_test", Name: "main", Path: "/project", Kind: "main", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, term := range []Terminal{
		{ID: "t_known", SessionID: "known", Cwd: "/project"},
		{ID: "t_ambiguous_one", SessionID: "ambiguous", Cwd: "/project"},
		{ID: "t_ambiguous_two", SessionID: "ambiguous", Cwd: "/other"},
	} {
		term.WorkspaceID, term.Agent, term.State, term.CreatedAt = "w_test", "antigravity", "exited", now
		if err := s.AddTerminal(term); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.PruneTerminals(); err != nil {
		t.Fatal(err)
	}
	got, err := reopened.AntigravitySessionDirs(t.Context())
	if err != nil || len(got) != 1 || got["known"] != "/project" {
		t.Fatalf("backfilled session directories = %v, %v", got, err)
	}
}
