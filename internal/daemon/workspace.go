package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"hive/internal/agent"
	"hive/internal/gitx"
	"hive/internal/rpc"
	"hive/internal/store"
	"hive/internal/term"
)

// --- projects

type projectAddParams struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	SetupScript string `json:"setupScript"`
	MainName    string `json:"mainName"`
}

type projectInfo struct {
	store.Project
	Workspaces int `json:"workspaces"`
}

func (d *Daemon) projectAdd(ctx context.Context, raw json.RawMessage) (any, error) {
	var p projectAddParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "path required")
	}
	path, err := filepath.Abs(p.Path)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%s is not a directory", path)
	}
	if existing, err := d.store.FindProject(path); err == nil {
		return nil, rpc.Errorf(rpc.CodeConflict, "project %s already added as %q", path, existing.Name)
	}
	name := p.Name
	if name == "" {
		name = filepath.Base(path)
	}
	if err := validName(name); err != nil {
		return nil, err
	}
	kind := "folder"
	if top := gitx.TopLevel(ctx, path); top != "" && samePath(top, path) {
		kind = "git"
	}
	proj := store.Project{ID: store.NewID("p_"), Name: name, Path: path, Kind: kind, SetupScript: p.SetupScript, CreatedAt: time.Now()}
	if err := d.store.AddProject(proj); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, rpc.Errorf(rpc.CodeConflict, "project name %q already used", name)
		}
		return nil, err
	}
	// 项目目录本身就是第一个工作区，对应 git 仓库的主 worktree
	mainName := "main"
	if p.MainName != "" {
		if err := validName(p.MainName); err != nil {
			return nil, err
		}
		mainName = p.MainName
	}
	main := store.Workspace{ID: store.NewID("w_"), ProjectID: proj.ID, Name: mainName, Path: path, Kind: "main", CreatedAt: time.Now()}
	if kind == "git" {
		main.Branch, _ = gitx.CurrentBranch(ctx, path)
	}
	if err := d.store.AddWorkspace(main); err != nil {
		return nil, err
	}
	return projectInfo{Project: proj, Workspaces: 1}, nil
}

func (d *Daemon) projectList(ctx context.Context, _ json.RawMessage) (any, error) {
	ps, err := d.store.Projects()
	if err != nil {
		return nil, err
	}
	counts, err := d.store.WorkspaceCounts()
	if err != nil {
		return nil, err
	}
	out := []projectInfo{}
	for _, p := range ps {
		out = append(out, projectInfo{Project: p, Workspaces: counts[p.ID]})
	}
	return out, nil
}

type projectSetParams struct {
	Project     string  `json:"project"`
	Name        *string `json:"name"`
	SetupScript *string `json:"setupScript"`
}

func (d *Daemon) projectSet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p projectSetParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	proj, err := d.project(p.Project)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		if err := validName(*p.Name); err != nil {
			return nil, err
		}
		proj.Name = *p.Name
	}
	if p.SetupScript != nil {
		proj.SetupScript = *p.SetupScript
	}
	if err := d.store.UpdateProject(proj); err != nil {
		return nil, err
	}
	return proj, nil
}

type projectRmParams struct {
	Project string `json:"project"`
	Force   bool   `json:"force"`
}

// projectRm 只移除登记，不删除项目目录；有 worktree 工作区或运行中的终端时需要 force。
func (d *Daemon) projectRm(ctx context.Context, raw json.RawMessage) (any, error) {
	var p projectRmParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	proj, err := d.project(p.Project)
	if err != nil {
		return nil, err
	}
	ws, err := d.store.Workspaces(proj.ID)
	if err != nil {
		return nil, err
	}
	for _, w := range ws {
		if w.Kind == "worktree" {
			return nil, rpc.Errorf(rpc.CodeConflict, "project has worktree workspace %q; remove it first", w.Name)
		}
		if running := d.runningIn(w.ID); running > 0 && !p.Force {
			return nil, rpc.Errorf(rpc.CodeConflict, "workspace %q has %d running terminals; pass force to close them", w.Name, running)
		}
	}
	for _, w := range ws {
		d.closeWorkspaceTerminals(w.ID)
		d.store.DeleteEndedTerminals(w.ID)
	}
	if err := d.store.DeleteProject(proj.ID); err != nil {
		return nil, err
	}
	return map[string]string{"removed": proj.ID}, nil
}

// --- workspaces

type workspaceCreateParams struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Mode    string `json:"mode"` // auto | folder | worktree | attach
	Path    string `json:"path"` // attach：项目已有的、不是 hive 建的 worktree 目录
	Branch  string `json:"branch"`
	Base    string `json:"base"`
	NoSetup bool   `json:"noSetup"`
	Agent   string `json:"agent"`
	agent.Model
	Prompt string `json:"prompt"`
}

type workspaceInfo struct {
	store.Workspace
	Project   string         `json:"project"`
	Label     string         `json:"label"`
	Terminals []terminalInfo `json:"terminals"`
	Setup     *setupResult   `json:"setup,omitempty"`
	Dirty     *bool          `json:"dirty,omitempty"`
}

type setupResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

func (d *Daemon) workspaceCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p workspaceCreateParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Agent == "" && p.Model != (agent.Model{}) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "model and effort require agent")
	}
	proj, err := d.project(p.Project)
	if err != nil {
		return nil, err
	}
	w, setup, err := d.createWorkspace(ctx, proj, p)
	if err != nil {
		return nil, err
	}
	info := workspaceInfo{Workspace: w, Project: proj.Name, Label: proj.Name + "/" + w.Name, Terminals: []terminalInfo{}, Setup: setup}
	if p.Agent != "" {
		s, err := d.startAgent(w, p.Agent, p.Model, p.Prompt, 0, 0)
		if err != nil {
			return nil, err
		}
		info.Terminals = append(info.Terminals, d.terminalInfo(s))
	}
	return info, nil
}

// createWorkspace 建工作区记录，worktree 模式下同时建 git worktree 并运行 setup 脚本。
func (d *Daemon) createWorkspace(ctx context.Context, proj store.Project, p workspaceCreateParams) (store.Workspace, *setupResult, error) {
	if err := validName(p.Name); err != nil {
		return store.Workspace{}, nil, err
	}
	if p.Name == "main" {
		return store.Workspace{}, nil, rpc.Errorf(rpc.CodeBadRequest, "name main is reserved for the project directory")
	}
	mode := p.Mode
	if mode == "" || mode == "auto" {
		mode = "folder"
		if proj.Kind == "git" {
			mode = "worktree"
		}
	}
	w := store.Workspace{ID: store.NewID("w_"), ProjectID: proj.ID, Name: p.Name, Kind: mode, CreatedAt: time.Now()}
	switch mode {
	case "folder":
		w.Path = proj.Path
	case "attach":
		// 别的工具或命令行建的 worktree：记成 folder，删工作区时只删记录，不删别人的目录和分支
		path, err := d.attachPath(ctx, proj, p.Path)
		if err != nil {
			return w, nil, err
		}
		w.Kind, w.Path = "folder", path
		w.Branch, _ = gitx.CurrentBranch(ctx, path)
	case "worktree":
		if proj.Kind != "git" {
			return w, nil, rpc.Errorf(rpc.CodeBadRequest, "project %s is not a git repository", proj.Name)
		}
		w.Branch = p.Branch
		if w.Branch == "" {
			w.Branch = p.Name
		}
		w.BaseRef = p.Base
		if w.BaseRef == "" {
			w.BaseRef, _ = gitx.CurrentBranch(ctx, proj.Path)
		}
		w.Path = filepath.Join(d.cfg.Home, "worktrees", proj.Name, p.Name)
		if _, err := os.Stat(w.Path); err == nil {
			return w, nil, rpc.Errorf(rpc.CodeConflict, "%s already exists", w.Path)
		}
		if err := os.MkdirAll(filepath.Dir(w.Path), 0o755); err != nil {
			return w, nil, err
		}
		if err := gitx.AddWorktree(ctx, proj.Path, w.Path, w.Branch, w.BaseRef); err != nil {
			return w, nil, rpc.Errorf(rpc.CodeConflict, "%v", err)
		}
	default:
		return w, nil, rpc.Errorf(rpc.CodeBadRequest, "unknown mode %q (auto, folder, worktree, attach)", mode)
	}
	if err := d.store.AddWorkspace(w); err != nil {
		if mode == "worktree" {
			gitx.RemoveWorktree(context.WithoutCancel(ctx), proj.Path, w.Path, true)
		}
		if strings.Contains(err.Error(), "UNIQUE") {
			return w, nil, rpc.Errorf(rpc.CodeConflict, "workspace %s/%s already exists", proj.Name, p.Name)
		}
		return w, nil, err
	}
	var setup *setupResult
	if mode == "worktree" && proj.SetupScript != "" && !p.NoSetup {
		setup = runSetup(ctx, w, proj.SetupScript)
	}
	return w, setup, nil
}

// attachPath 检查 dir 是项目仓库的另一个 worktree，且还没有工作区，返回解析过符号链接的路径。
func (d *Daemon) attachPath(ctx context.Context, proj store.Project, dir string) (string, error) {
	if proj.Kind != "git" {
		return "", rpc.Errorf(rpc.CodeBadRequest, "project %s is not a git repository", proj.Name)
	}
	if dir == "" {
		return "", rpc.Errorf(rpc.CodeBadRequest, "path required")
	}
	path, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	}
	if top := gitx.TopLevel(ctx, path); top == "" || !samePath(top, path) {
		return "", rpc.Errorf(rpc.CodeBadRequest, "%s is not a git worktree root", dir)
	}
	if c := gitx.CommonDir(ctx, path); c == "" || c != gitx.CommonDir(ctx, proj.Path) {
		return "", rpc.Errorf(rpc.CodeBadRequest, "%s is not a worktree of project %s", dir, proj.Name)
	}
	ws, err := d.store.Workspaces(proj.ID)
	if err != nil {
		return "", err
	}
	for _, w := range ws {
		if samePath(w.Path, path) {
			return "", rpc.Errorf(rpc.CodeConflict, "%s is already workspace %s", dir, w.Name)
		}
	}
	return path, nil
}

// runSetup 在新 worktree 里用登录 shell 执行项目的 setup 脚本。
func runSetup(ctx context.Context, w store.Workspace, script string) *setupResult {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, agent.UserShell(), "-l", "-c", script)
	cmd.Dir = w.Path
	cmd.Env = term.Environ([]string{"HIVE_WORKSPACE_ID=" + w.ID, "HIVE_WORKSPACE_PATH=" + w.Path})
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := out.String()
	if len(s) > 4096 {
		s = "…" + s[len(s)-4096:]
	}
	if err != nil {
		s += "\n" + err.Error()
	}
	return &setupResult{OK: err == nil, Output: s}
}

type workspaceListParams struct {
	Project string `json:"project"`
	Dirty   bool   `json:"dirty"`
}

func (d *Daemon) workspaceList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p workspaceListParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	projectID := ""
	if p.Project != "" {
		proj, err := d.project(p.Project)
		if err != nil {
			return nil, err
		}
		projectID = proj.ID
	}
	ws, err := d.store.Workspaces(projectID)
	if err != nil {
		return nil, err
	}
	idx, err := d.loadWorkspaceIndex(false)
	if err != nil {
		return nil, err
	}
	sessions := d.sessionsByWorkspace()
	out := []workspaceInfo{}
	for _, w := range ws {
		out = append(out, d.buildWorkspaceInfo(ctx, w, p.Dirty, idx.label(w), sessions[w.ID]))
	}
	return out, nil
}

func (d *Daemon) workspaceInfo(ctx context.Context, w store.Workspace, dirty bool) workspaceInfo {
	return d.buildWorkspaceInfo(ctx, w, dirty, d.workspaceLabel(w), d.sessionsIn(w.ID))
}

func (d *Daemon) buildWorkspaceInfo(ctx context.Context, w store.Workspace, dirty bool, label string, sessions []*session) workspaceInfo {
	info := workspaceInfo{Workspace: w, Label: label, Terminals: []terminalInfo{}}
	info.Project, _, _ = strings.Cut(info.Label, "/")
	for _, s := range sessions {
		info.Terminals = append(info.Terminals, d.terminalInfo(s))
	}
	if dirty && w.Kind != "folder" {
		if out, err := gitx.Dirty(ctx, w.Path); err == nil {
			v := out != ""
			info.Dirty = &v
		}
	}
	return info
}

type workspaceSel struct {
	Workspace string `json:"workspace"`
}

func (d *Daemon) workspaceShow(ctx context.Context, raw json.RawMessage) (any, error) {
	var p workspaceSel
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	w, err := d.workspace(p.Workspace)
	if err != nil {
		return nil, err
	}
	return d.workspaceInfo(ctx, w, true), nil
}

type workspaceCurrentParams struct {
	TerminalID string `json:"terminalId"`
	Cwd        string `json:"cwd"`
}

// workspaceCurrent 优先按调用方所在终端判断，否则取路径最长匹配的工作区。
func (d *Daemon) workspaceCurrent(ctx context.Context, raw json.RawMessage) (any, error) {
	var p workspaceCurrentParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	w, err := d.currentWorkspace(p.TerminalID, p.Cwd)
	if err != nil {
		return nil, err
	}
	return d.workspaceInfo(ctx, w, false), nil
}

func (d *Daemon) currentWorkspace(terminalID, cwd string) (store.Workspace, error) {
	if terminalID != "" {
		if s, err := d.session(terminalID); err == nil {
			if w, err := d.store.Workspace(s.WorkspaceID); err == nil {
				return w, nil
			}
		}
	}
	ws, err := d.store.Workspaces("")
	if err != nil {
		return store.Workspace{}, err
	}
	var best *store.Workspace
	for i, w := range ws {
		if !within(cwd, w.Path) {
			continue
		}
		// 同一路径上 main 优先于额外的文件夹工作区
		if best == nil || len(w.Path) > len(best.Path) || (len(w.Path) == len(best.Path) && w.Kind == "main") {
			best = &ws[i]
		}
	}
	if best == nil {
		return store.Workspace{}, rpc.Errorf(rpc.CodeNotFound, "no workspace contains %s; pass a workspace", cwd)
	}
	return *best, nil
}

type workspaceRmParams struct {
	Workspace    string `json:"workspace"`
	Force        bool   `json:"force"`
	DeleteBranch bool   `json:"deleteBranch"`
}

func (d *Daemon) workspaceRm(ctx context.Context, raw json.RawMessage) (any, error) {
	var p workspaceRmParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	w, err := d.workspace(p.Workspace)
	if err != nil {
		return nil, err
	}
	if w.Kind == "main" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "main workspace is the project directory; remove the project instead")
	}
	if running := d.runningIn(w.ID); running > 0 && !p.Force {
		return nil, rpc.Errorf(rpc.CodeConflict, "workspace has %d running terminals; pass force to close them", running)
	}
	if w.Kind == "worktree" {
		proj, err := d.store.FindProject(w.ProjectID)
		if err != nil {
			return nil, err
		}
		if out, err := gitx.Dirty(ctx, w.Path); err == nil && out != "" && !p.Force {
			return nil, rpc.Errorf(rpc.CodeConflict, "worktree has uncommitted changes; pass force to discard:\n%s", out)
		}
		d.closeWorkspaceTerminals(w.ID)
		if _, statErr := os.Stat(w.Path); statErr == nil {
			if err := gitx.RemoveWorktree(ctx, proj.Path, w.Path, p.Force); err != nil {
				return nil, rpc.Errorf(rpc.CodeConflict, "%v", err)
			}
		}
		if p.DeleteBranch && w.Branch != "" {
			if err := gitx.DeleteBranch(ctx, proj.Path, w.Branch, p.Force); err != nil {
				return nil, rpc.Errorf(rpc.CodeConflict, "worktree removed but %v", err)
			}
		}
	} else {
		d.closeWorkspaceTerminals(w.ID)
	}
	d.store.DeleteEndedTerminals(w.ID)
	if err := d.store.DeleteWorkspace(w.ID); err != nil {
		return nil, err
	}
	return map[string]string{"removed": w.ID}, nil
}

func (d *Daemon) runningIn(workspaceID string) int {
	n := 0
	for _, s := range d.sessionsIn(workspaceID) {
		if s.Running() {
			n++
		}
	}
	return n
}

func (d *Daemon) closeWorkspaceTerminals(workspaceID string) {
	d.closeSessions(d.sessionsIn(workspaceID), 2*time.Second)
}

// --- helpers

func validName(name string) error {
	if name == "" {
		return rpc.Errorf(rpc.CodeBadRequest, "name required")
	}
	if name == "." || name == ".." || strings.HasPrefix(name, "-") || strings.ContainsAny(name, `/\:*?"<>|~^`) {
		return rpc.Errorf(rpc.CodeBadRequest, "invalid name %q", name)
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return rpc.Errorf(rpc.CodeBadRequest, "name %q must not contain spaces", name)
		}
	}
	return nil
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

func within(path, dir string) bool {
	if path == "" {
		return false
	}
	// macOS 的 /tmp、/var 是软链接，两边都解析后再比较
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		dir = d
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

var errExited = errors.New("terminal has exited")

func (d *Daemon) mustRunning(s *session) error {
	if !s.Running() {
		return rpc.Errorf(rpc.CodeConflict, "%v (exit code %d)", errExited, s.ExitCode())
	}
	return nil
}
