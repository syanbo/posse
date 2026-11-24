package cli

import (
	"flag"
	"fmt"
	"strings"

	"posse/internal/lark"
)

func cmdLarkProfiles(e *env, args []string) error {
	fs := flag.NewFlagSet("lark profiles", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	var out struct {
		OwnerOpenID string             `json:"ownerOpenId"`
		Profiles    []lark.ProfileInfo `json:"profiles"`
	}
	if err := e.call("lark.profiles", nil, &out); err != nil {
		return err
	}
	return e.emit(out, func() {
		if out.OwnerOpenID != "" {
			fmt.Fprintf(e.out, "owner: %s\n", out.OwnerOpenID)
		}
		if len(out.Profiles) == 0 {
			fmt.Fprintln(e.out, "(no lark-cli profiles found; run `lark-cli config init`)")
			return
		}
		for _, p := range out.Profiles {
			mark := " "
			if p.Active {
				mark = "*"
			}
			fmt.Fprintf(e.out, "%s %-22s  app=%s  brand=%s  user=%s\n", mark, p.Name, p.AppID, p.Brand, p.User)
		}
	})
}

func cmdLarkList(e *env, args []string) error {
	fs := flag.NewFlagSet("lark list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	var out struct {
		OwnerOpenID string           `json:"ownerOpenId"`
		Bots        []lark.BotConfig `json:"bots"`
		Statuses    []lark.BotStatus `json:"statuses"`
	}
	if err := e.call("lark.bots.list", nil, &out); err != nil {
		return err
	}
	stByID := map[string]lark.BotStatus{}
	for _, s := range out.Statuses {
		stByID[s.ID] = s
	}
	return e.emit(out, func() {
		if out.OwnerOpenID != "" {
			fmt.Fprintf(e.out, "owner: %s\n", out.OwnerOpenID)
		}
		if len(out.Bots) == 0 {
			fmt.Fprintln(e.out, "(no Feishu bots configured; run `posse lark add --profile <profile>`)")
			return
		}
		for _, b := range out.Bots {
			st := stByID[b.ID]
			state := "off"
			if b.Enabled {
				switch {
				case st.MsgReady && st.CardReady:
					state = "ready"
				case st.Running:
					state = "connecting"
				default:
					state = "stopped"
				}
			}
			line := fmt.Sprintf("%-14s  %-16s  profile=%-20s  agent=%-11s  perm=%-4s  mode=%-6s  trigger=%-9s  [%s]",
				b.ID, b.Name, b.Profile, b.Agent, b.PermissionMode, b.SessionMode, b.TriggerPolicy, state)
			if st.LastError != "" {
				line += "  err=" + st.LastError
			}
			fmt.Fprintln(e.out, line)
		}
	})
}

func cmdLarkAdd(e *env, args []string) error {
	fs := flag.NewFlagSet("lark add", flag.ContinueOnError)
	id := fs.String("id", "", "existing bot ID to update")
	name := fs.String("name", "", "display name")
	profile := fs.String("profile", "", "lark-cli profile name (required)")
	ws := fs.String("workspace", "", "default Posse workspace ID or name")
	agentName := fs.String("agent", "antigravity", "default agent (antigravity|claude|codex|grok|cursor)")
	model := fs.String("model", "", "optional model override")
	effort := fs.String("effort", "", "optional reasoning effort")
	perm := fs.String("permission", lark.PermAsk, "permission mode (ask|skip)")
	sessionMode := fs.String("session", lark.SessionChat, "group session mode (chat|thread)")
	trigger := fs.String("trigger", lark.TriggerSelf, "who can trigger in groups (self|allowlist|group)")
	role := fs.String("role", "", "default role / instructions for this bot")
	owner := fs.String("owner", "", "override owner Feishu open_id")
	enable := fs.Bool("enable", true, "enable bot immediately")
	var allowUsers, allowChats multiFlag
	fs.Var(&allowUsers, "allow-user", "allowed sender open_id (repeatable)")
	fs.Var(&allowChats, "allow-chat", "allowed chat_id (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*profile) == "" {
		return fmt.Errorf("lark add: --profile is required")
	}
	bot := lark.BotConfig{
		ID:             *id,
		Name:           *name,
		Profile:        *profile,
		Enabled:        *enable,
		WorkspaceID:    *ws,
		Agent:          *agentName,
		Model:          *model,
		Effort:         *effort,
		PermissionMode: *perm,
		Role:           *role,
		SessionMode:    *sessionMode,
		TriggerPolicy:  *trigger,
		AllowedUsers:   []string(allowUsers),
		AllowedChats:   []string(allowChats),
	}
	var saved lark.BotConfig
	if err := e.call("lark.bots.save", map[string]any{"ownerOpenId": *owner, "bot": bot}, &saved); err != nil {
		return err
	}
	return e.emit(saved, func() {
		fmt.Fprintf(e.out, "saved bot %s (%s, profile=%s, agent=%s, enabled=%v)\n",
			saved.ID, saved.Name, saved.Profile, saved.Agent, saved.Enabled)
	})
}

func cmdLarkToggle(on bool) func(*env, []string) error {
	name := "lark disable"
	if on {
		name = "lark enable"
	}
	return func(e *env, args []string) error {
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("%s: bot ID required", name)
		}
		var saved lark.BotConfig
		if err := e.call("lark.bots.toggle", map[string]any{"id": fs.Arg(0), "enabled": on}, &saved); err != nil {
			return err
		}
		return e.emit(saved, func() {
			fmt.Fprintf(e.out, "%s: enabled=%v\n", saved.ID, saved.Enabled)
		})
	}
}

func cmdLarkRemove(e *env, args []string) error {
	fs := flag.NewFlagSet("lark rm", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("lark rm: bot ID required")
	}
	var out map[string]bool
	if err := e.call("lark.bots.delete", map[string]any{"id": fs.Arg(0)}, &out); err != nil {
		return err
	}
	return e.emit(out, func() {
		fmt.Fprintf(e.out, "removed %s\n", fs.Arg(0))
	})
}
