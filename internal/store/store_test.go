package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := Project{ID: NewID("p_"), Name: "repo", Path: "/r", Kind: "git", CreatedAt: time.Now()}
	if err := s.AddProject(p); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProject(Project{ID: NewID("p_"), Name: "repo", Path: "/other", Kind: "git", CreatedAt: time.Now()}); err == nil {
		t.Fatal("duplicate project name accepted")
	}
	for _, name := range []string{"main", "feat"} {
		if err := s.AddWorkspace(Workspace{ID: NewID("w_"), ProjectID: p.ID, Name: name, Path: "/r", Kind: "folder", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := s.FindWorkspaces("repo/feat")
	if err != nil || len(ws) != 1 || ws[0].Name != "feat" {
		t.Fatalf("FindWorkspaces = %v, %v", ws, err)
	}
	if err := s.AddTerminal(Terminal{ID: "t_1", WorkspaceID: ws[0].ID, Command: []string{"sh"}, State: "running", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.MarkRunningLost(time.Now(), nil); err != nil || n != 1 {
		t.Fatalf("MarkRunningLost = %d, %v", n, err)
	}
	if err := s.AddTerminal(Terminal{ID: "t_2", WorkspaceID: ws[0].ID, Command: []string{"sh"}, State: "running", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneTerminals(); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := s.db.Query(`SELECT id FROM terminals`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if len(left) != 1 || left[0] != "t_2" {
		t.Fatalf("terminals after prune = %v, want [t_2]", left)
	}
	counts, err := s.WorkspaceCounts()
	if err != nil || counts[p.ID] != 2 || len(counts) != 1 {
		t.Fatalf("WorkspaceCounts = %v, %v", counts, err)
	}
	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Workspace(ws[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("workspace survived project deletion: %v", err)
	}
}

// 旧库的 tasks 表没有 model、effort 列，打开时补上，旧任务读出来为空
func TestMigrateTaskModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hive.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.NewReplacer("\tmodel TEXT NOT NULL DEFAULT '',\n", "", "\teffort TEXT NOT NULL DEFAULT '',\n", "").Replace(taskSchema)
	if _, err := db.Exec(old + `INSERT INTO tasks(id,title,spec,workspace_id,worker,status,created_at) VALUES('k_old','t','s','w','t_1','done',0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if task, err := s.Task("k_old"); err != nil || task.Model != "" {
			t.Fatalf("old task %+v %v", task, err)
		}
		id := NewID("k_")
		if err := s.AddTask(Task{ID: id, Title: "t", Spec: "s", WorkspaceID: "w", Worker: "t_2", Agent: "codex", Model: "gpt-5.5", Effort: "high", Status: TaskRunning}); err != nil {
			t.Fatal(err)
		}
		if task, _ := s.Task(id); task.Model != "gpt-5.5" || task.Effort != "high" {
			t.Fatalf("new task %+v", task)
		}
		s.Close()
	}
}
