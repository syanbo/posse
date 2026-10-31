package app

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/skills"
)

// 技能页的 MCP 分页：四家用户级的 MCP 服务按名字合在一起，每行挂四家的状态，详情写各家从哪读到、
// 每份配置怎么启动。只看不改：环境变量和请求头只列名字，值里常有密钥。扫描见 skills.ScanMCP。

// shownMCP 是按搜索和 agent 筛过、排好序的服务。
func (s *skillsView) shownMCP() []skills.MCPServer {
	q := strings.ToLower(strings.TrimSpace(s.query))
	var out []skills.MCPServer
	for _, sv := range s.mcp.Servers {
		if s.agent > 0 && sv.Agents[skills.Agents[s.agent-1]].State == skills.Unseen {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(sv.Name), q) && !slices.ContainsFunc(sv.Defs, func(d skills.MCPDef) bool {
			return strings.Contains(strings.ToLower(mcpLaunch(d)), q)
		}) {
			continue
		}
		out = append(out, sv)
	}
	if s.byUse == 1 && s.mcpStats != nil {
		uses := func(name string) int {
			if st := s.mcpStats[name]; st != nil {
				return st.Count
			}
			return 0
		}
		slices.SortStableFunc(out, func(x, y skills.MCPServer) int { return uses(y.Name) - uses(x.Name) })
	}
	return out
}

// mcpLaunch 是一份配置怎么连：远程的是网址，本地的是启动命令。
func mcpLaunch(d skills.MCPDef) string {
	if d.URL != "" {
		return d.URL
	}
	parts := []string{shellWord(d.Command)}
	for _, a := range d.Args {
		parts = append(parts, shellWord(a))
	}
	return strings.Join(parts, " ")
}

// shellWord 只给有空格、引号这类字符的参数加引号，路径照原样显示。
func shellWord(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"$`\\;&|<>(){}*?!#~") {
		return s
	}
	return agent.Quote(s)
}

func mcpTransportLabel(t string) string {
	switch t {
	case "http":
		return "远程 HTTP"
	case "sse":
		return "远程 SSE"
	}
	return "本地进程"
}

func (a *app) mcpBody(c *ui.Context) {
	s := &a.skills
	list := s.shownMCP()
	issues := s.mcpSel == skillIssuesKey && len(s.mcp.Issues) > 0
	if !issues && !slices.ContainsFunc(list, func(sv skills.MCPServer) bool { return sv.Name == s.mcpSel }) {
		s.mcpSel = ""
		if len(list) > 0 {
			s.mcpSel = list[0].Name
		}
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Padding(12).Gap(8).Children(func() {
		// 可以聚焦：点过列表后方向键归它，不被侧栏拿走
		card(c).Width(380).Shrink(0).Clip().Focusable().FocusRing(false).Label("MCP 列表").Children(func() {
			a.mcpTotals(c)
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Padding(4, 8, 16, 8).Gap(2).Children(func() {
					if !s.loaded {
						usageNote(c, "正在扫描…")
						return
					}
					if len(list) == 0 {
						usageNote(c, "没有符合条件的 MCP 服务。")
					}
					for _, sv := range list {
						row := a.mcpRow(c, sv, s.mcpSel == sv.Name)
						if s.reveal && s.mcpSel == sv.Name {
							row.ScrollIntoView()
							s.reveal = false
						}
					}
				})
			})
		})
		// 配置多的服务详情很长，单独滚动
		ui.Scroll(c.Key("mcp-detail")).Grow(1).Shrink(1).Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				if s.mcpSel == skillIssuesKey {
					a.mcpIssues(c)
					return
				}
				for _, sv := range list {
					if sv.Name == s.mcpSel {
						a.mcpDetail(c, sv)
					}
				}
			})
		})
	})
	keys := make([]string, len(list))
	for i, sv := range list {
		keys[i] = sv.Name
	}
	a.skillsArrowKeys(c, keys, &s.mcpSel)
}

// mcpTotals 是列表顶上的总计：服务数、各家用得上的数，和体检发现的问题（点开看）。
func (a *app) mcpTotals(c *ui.Context) {
	t := c.Theme()
	s := &a.skills
	if !s.loaded {
		return
	}
	var byAgent [skills.NumAgents]int
	for _, sv := range s.mcp.Servers {
		for _, ag := range skills.Agents {
			if sv.Agents[ag].State == skills.On {
				byAgent[ag]++
			}
		}
	}
	ui.Column(c).Padding(12, 20, 8, 20).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("%d 个 MCP 服务", len(s.mcp.Servers))).FontSize(fsBody).FontWeight(fwMedium).FontFeatures("tnum").Grow(1)
			if s.mcpStats != nil {
				segmented(c, &s.byUse, "排序", "名字", "用量")
			}
		})
		ui.Row(c).Gap(12).Wrap().Children(func() {
			for _, ag := range skills.Agents {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					agentIcon(c, ag.String(), smallIcon())
					ui.Text(c, fmt.Sprint(byAgent[ag])).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
				}).Tooltip(agentTitle(ag.String()) + "用得上的 MCP 服务")
			}
		})
	})
	if k := len(s.mcp.Issues); k > 0 {
		row := ui.Row(c.Key("mcp-issues")).Margin(0, 8, 4, 8).Padding(8, 12).Gap(8).Radius(rControl).AlignItems(ui.Center).Role(ui.RoleButton).Label("MCP 体检")
		selectedBg(c, row, s.mcpSel == skillIssuesKey)
		row.Children(func() {
			ui.Icon(c, iconInfo).FontSize(iconSize).TextColor(colors(t).honey).Shrink(0)
			ui.Text(c, fmt.Sprintf("体检发现 %d 个问题", k)).FontSize(fsBody).FontFeatures("tnum").Grow(1)
			ui.Icon(c, iconForward).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
		})
		if row.Clicked() {
			s.mcpSel = skillIssuesKey
		}
	}
	hline(c).Margin(0, 20)
}

// mcpRow 是列表的一行：名字和四家的状态，下面是怎么启动（几份配置不同时取第一份），再下面是用量。
func (a *app) mcpRow(c *ui.Context, sv skills.MCPServer, selected bool) ui.Element {
	t := c.Theme()
	s := &a.skills
	row := ui.Column(c.Key("mcp:"+sv.Name)).Padding(12, 12).Gap(4).Radius(rControl).Role(ui.RoleButton).Label(sv.Name)
	selectedBg(c, row, selected)
	row.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, sv.Name).FontSize(fsBody).FontWeight(fwMedium).SingleLine().Grow(1).Shrink(1)
			agentMarks(c, sv.Agents, nil, nil)
		})
		if len(sv.Defs) > 0 {
			ui.Text(c, mcpLaunch(sv.Defs[0])).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine()
		}
		if s.mcpStats != nil {
			ui.Row(c).Gap(12).Children(func() { skillUseLine(c, s.mcpStats[sv.Name]) })
		}
	})
	if row.Clicked() {
		s.mcpSel = sv.Name
	}
	return row
}

// mcpDetail 是右边的详情：名字、连接方式，用量，各家状态（从哪个文件读到），各份配置。
func (a *app) mcpDetail(c *ui.Context, sv skills.MCPServer) {
	t := c.Theme()
	s := &a.skills
	card(c).Padding(cardPad).Gap(12).Shrink(0).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
			tile(c, iconExtensions, tileLarge(), colors(t).tint, t.Accent)
			ui.Column(c).Gap(8).Grow(1).Shrink(1).Children(func() {
				titleText(c, sv.Name, fsPage).LineHeight(1.3).MaxLines(2).Selectable()
				transport := "本地进程"
				if len(sv.Defs) > 0 {
					transport = mcpTransportLabel(sv.Defs[0].Transport)
				}
				var issues func()
				if n := len(mcpIssuesOf(s.mcp.Issues, sv.Name)); n > 0 {
					issues = func() {
						ui.Text(c, fmt.Sprintf("%d 个问题", n)).FontSize(fsSmall).TextColor(colors(t).honey)
					}
				}
				metaRow(c, meta(c, transport), meta(c, fmt.Sprintf("%d 份配置", len(sv.Defs))), issues)
			})
		})
	})
	if s.mcpStats != nil {
		skillUseCard(c, s.mcpStats[sv.Name])
	}
	card(c).Padding(cardPad).Gap(4).Shrink(0).Children(func() {
		cardTitle(c, "各家状态").Margin(0, 0, 4, 0)
		for i, ag := range skills.Agents {
			if i > 0 {
				hline(c)
			}
			mcpAgentRow(c, sv, ag)
		}
	})
	card(c).Padding(cardPad).Gap(4).Shrink(0).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(0, 0, 8, 0).Children(func() {
			cardTitle(c, "配置")
			ui.Text(c, "环境变量和请求头只列名字，不显示值").FontSize(fsSmall).TextColor(colors(t).faint)
		})
		for i, d := range sv.Defs {
			if i > 0 {
				hline(c)
			}
			mcpDefRow(c, d)
		}
	})
	for _, is := range mcpIssuesOf(s.mcp.Issues, sv.Name) {
		ui.Text(c, mcpIssueText(is)).FontSize(fsSmall).TextColor(colors(t).honey).LineHeight(1.45).Padding(0, 4)
	}
}

// mcpAgentRow 是一家的状态：用不用，从哪个文件读到它。
func mcpAgentRow(c *ui.Context, sv skills.MCPServer, ag skills.Agent) {
	t := c.Theme()
	st := sv.Agents[ag]
	ui.Row(c.Key("mcp-agent-"+ag.String())).Padding(8, 0).Gap(12).AlignItems(ui.Start).Children(func() {
		e := agentIcon(c, ag.String(), iconSize)
		if st.State != skills.On {
			e.Opacity(0.55)
		}
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, agentTitle(ag.String())).FontSize(fsBody).FontWeight(fwMedium)
				color := t.TextMuted
				if st.State == skills.On {
					color = colors(t).sage
				}
				ui.Text(c, skillStateText(st.State)).FontSize(fsSmall).TextColor(color)
				if st.Inferred && st.State != skills.Unseen {
					ui.Text(c, "推断").FontSize(fsSmall).TextColor(colors(t).faint).Tooltip(mcpInferredTip(ag))
				}
			})
			if st.Why != "" {
				ui.Text(c, st.Why).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
			}
			if st.Via != "" {
				ui.Text(c, shortPath(st.Via)).FontSize(fsSmall).Font(monoFont).TextColor(colors(t).faint).MaxLines(2)
			}
		})
	})
}

func mcpInferredTip(ag skills.Agent) string {
	if ag == skills.Cursor {
		return "Cursor 的批准和开关存在哪没有核实，读到的都算启用"
	}
	return "没问到它自己（找不到程序或超时），按它文档里的读取规则推算"
}

// mcpDefRow 是一份配置：哪家、哪个文件，怎么启动，带了哪些环境变量和请求头。
func mcpDefRow(c *ui.Context, d skills.MCPDef) {
	t := c.Theme()
	ui.Column(c).Padding(12, 0).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			agentIcon(c, d.Agent.String(), smallIcon())
			ui.Text(c, shortPath(d.File)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(1).Shrink(1).Tooltip(d.File)
			if d.Plugin {
				ui.Text(c, "插件带的").FontSize(fsSmall).TextColor(colors(t).faint).Shrink(0)
			}
			if d.Disabled {
				ui.Text(c, "已关闭").FontSize(fsSmall).TextColor(colors(t).faint).Shrink(0)
			}
		})
		ui.Text(c, mcpLaunch(d)).FontSize(fsSmall).Font(monoFont).Padding(8, 12).Radius(rCard).Background(colors(t).code).Selectable()
		if len(d.EnvKeys) > 0 {
			ui.Text(c, "环境变量："+strings.Join(d.EnvKeys, "、")).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.6)
		}
		if len(d.HeaderKeys) > 0 {
			ui.Text(c, "请求头："+strings.Join(d.HeaderKeys, "、")).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.6)
		}
	})
}

func mcpIssuesOf(all []skills.MCPIssue, name string) []skills.MCPIssue {
	var out []skills.MCPIssue
	for _, is := range all {
		if is.Name == name {
			out = append(out, is)
		}
	}
	return out
}

func mcpIssueText(is skills.MCPIssue) string {
	switch is.Kind {
	case skills.MCPMissingCommand:
		return "启动命令不在了：" + shortPath(is.Detail)
	case skills.MCPMismatch:
		return "几份配置写得不一样（" + is.Detail + "），各家实际启动的可能不是同一个"
	}
	return is.Detail
}

// mcpIssues 是体检的结果，每条点一下跳到那个服务。
func (a *app) mcpIssues(c *ui.Context) {
	t := c.Theme()
	s := &a.skills
	card(c).Padding(cardPad).Gap(8).Shrink(0).Children(func() {
		cardTitle(c, "MCP 体检")
		ui.Text(c, "只看配置文件，不启动服务。").FontSize(fsSmall).TextColor(t.TextMuted)
		ui.Column(c).Gap(2).Children(func() {
			for i, is := range s.mcp.Issues {
				row := ui.Column(c.Key(fmt.Sprint("mcp-issue-", i))).Padding(8, 12).Gap(2).Radius(rControl).Role(ui.RoleButton).Label(is.Name)
				selectedBg(c, row, false)
				row.Children(func() {
					ui.Text(c, is.Name).FontSize(fsBody).FontWeight(fwMedium)
					ui.Text(c, mcpIssueText(is)).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
				})
				if row.Clicked() {
					s.mcpSel = is.Name
				}
			}
		})
	})
}
