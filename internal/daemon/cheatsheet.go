package daemon

import (
	"context"
	"encoding/json"
	"slices"

	"posse/internal/cheatsheet"
	"posse/internal/rpc"
	"posse/internal/skills"
)

func (d *Daemon) terminalCheatsheet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Terminal string `json:"terminal"`
		Agent    string `json:"agent"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	s := d.liveSession(p.Terminal)
	if s == nil || !s.Running() {
		return nil, rpc.Errorf(rpc.CodeNotFound, "terminal %s not found", p.Terminal)
	}
	w, err := d.store.Workspace(s.WorkspaceID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	name, dir, extra := s.agent, s.agyDir, s.agyDirs
	s.mu.Unlock()
	if name == "" || name != p.Agent {
		return nil, rpc.Errorf(rpc.CodeConflict, "terminal agent changed")
	}
	list := cheatsheet.AvailableSkills(skills.Options{Home: d.agentDataHome(), Dir: w.Path, AntigravityDir: dir, AntigravityDirs: extra}, name)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	changed := s.agent != name || s.agyDir != dir || !slices.Equal(s.agyDirs, extra)
	s.mu.Unlock()
	if changed || !s.Running() {
		return nil, rpc.Errorf(rpc.CodeConflict, "terminal agent changed")
	}
	return cheatsheet.TerminalInfo{
		Version: cheatsheet.RunningPathVersion(s.Pid(), name),
		Skills:  list,
	}, nil
}
