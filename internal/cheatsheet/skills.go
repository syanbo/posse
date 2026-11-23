package cheatsheet

import (
	"slices"
	"strings"

	"posse/internal/skills"
)

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Builtin     bool   `json:"builtin,omitempty"`
}

type TerminalInfo struct {
	Version string  `json:"version,omitempty"`
	Skills  []Skill `json:"skills,omitempty"`
}

func AvailableSkills(o skills.Options, agent string) []Skill {
	for _, ag := range skills.Agents {
		if ag.String() != agent {
			continue
		}
		var list []Skill
		for _, s := range skills.Scan(o).Skills {
			st := s.Agents[ag].State
			// Claude 同步技能的调用名带账号前缀，不能直接填裸名字。
			if (st == skills.On || st == skills.Manual) && !s.Meta.UserHidden && !(ag == skills.Claude && s.Scope == skills.ScopeSynced) {
				list = append(list, Skill{Name: s.Name, Description: s.Description, Builtin: s.Scope == skills.ScopeSystem})
			}
		}
		slices.SortFunc(list, func(x, y Skill) int { return strings.Compare(x.Name, y.Name) })
		return list
	}
	return nil
}
