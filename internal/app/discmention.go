package app

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"posse/locales"
)

// atCandidate 是在输入框里打 @ 时弹出的一项：自己的 Agent（作为请求执行）、别人的 Agent（提醒它的拥有者）、群里的人（只提醒）。
type atCandidate struct {
	member *discMember // Agent；用不了的别人的 Agent 选中时提醒拥有者，名字留在正文里
	person *discPerson
	can    bool // 能 @ 这个 Agent 让它执行：自己的，或拥有者放开给自己的
}

func (x atCandidate) name() string {
	if x.member != nil {
		return x.member.Name
	}
	return x.person.Name
}

// atToken 找光标前正在打的 @：返回 @ 的位置（按 rune）和后面打的字；没有返回 -1。
// @ 前面紧挨着英文字母、数字时（邮箱）不算；@ 和光标之间有空白就不算。
func atToken(text string, caret int) (int, string) {
	r := []rune(text)
	if caret > len(r) {
		caret = len(r)
	}
	for i := caret - 1; i >= 0; i-- {
		switch {
		case unicode.IsSpace(r[i]):
			return -1, ""
		case r[i] == '@' || r[i] == '＠': // 中文输入法打出的全角 @
			if i > 0 && r[i-1] < unicode.MaxASCII && (unicode.IsLetter(r[i-1]) || unicode.IsDigit(r[i-1])) {
				return -1, ""
			}
			return i, string(r[i+1 : caret])
		}
	}
	return -1, ""
}

// atCandidates 是按 query 筛出的候选：能用的 Agent 在前，然后是用不了的别人的 Agent、群里的人。
func atCandidates(d *discDetail, members []discMember, query string) []atCandidate {
	q := strings.ToLower(query)
	match := func(name string) bool { return q == "" || strings.Contains(strings.ToLower(name), q) }
	var mine, others, people []atCandidate
	for i := range members {
		mem := &members[i]
		if !match(mem.Name) {
			continue
		}
		if d.canUse(*mem) {
			mine = append(mine, atCandidate{member: mem, can: true})
		} else if p := d.person(mem.Owner); p != nil && p.State == "active" {
			others = append(others, atCandidate{member: mem})
		}
	}
	if d.collab != nil {
		for i := range d.collab.People {
			p := &d.collab.People[i]
			if p.State == "active" && p.FP != d.collab.Self && match(p.Name) {
				people = append(people, atCandidate{person: p})
			}
		}
	}
	return append(append(mine, others...), people...)
}

// atState 是输入框里 @ 候选的状态。
type atState struct {
	hot     int  // 高亮的一项
	skip    int  // 按 Esc 关掉的那次 @ 的位置加一：接着打字不再弹，光标离开它（删掉、打了空格）后清掉
	caretTo int  // 下一帧把光标放到这里（选了候选、改了正文之后），-1 是不动
	focus   bool // 下一帧把焦点还给输入框（点了候选）
}

// pickAt 选中一项：能用的 Agent、人从正文里去掉 @ 和打的字，换成接收者；用不了的别人的 Agent 补全名字留在正文里，提醒它的拥有者。
func (a *app) pickAt(text string, at int, query string, x atCandidate) string {
	b := &a.disc
	r := []rune(text)
	end := min(at+1+len([]rune(query)), len(r))
	var insert string
	switch {
	case x.member != nil && x.can:
		b.target, b.mention = x.member.ID, ""
	case x.member != nil:
		b.mention, b.target = x.member.Owner, ""
		insert = "@" + x.member.Name + " "
	default:
		b.mention, b.target = x.person.FP, ""
	}
	out := string(r[:at]) + insert + string(r[end:])
	b.at = atState{caretTo: at + len([]rune(insert)), focus: true}
	return out
}

// atKeys 是候选开着时输入框的按键：上下选、回车或 Tab 选中、Esc 关掉；其余照常输入。
func (a *app) atKeys(k ui.TextKey, cands []atCandidate, at int, query string) bool {
	b := &a.disc
	if len(cands) == 0 || k.Mods&^ui.Shift != 0 {
		return false
	}
	switch k.Key {
	case ui.KeyUp:
		b.at.hot = (b.at.hot - 1 + len(cands)) % len(cands)
	case ui.KeyDown:
		b.at.hot = (b.at.hot + 1) % len(cands)
	case ui.KeyEnter, ui.KeyTab:
		if k.Mods != 0 {
			return false
		}
		k.SetText(a.pickAt(k.Text(), at, query, cands[min(b.at.hot, len(cands)-1)]))
	case ui.KeyEscape:
		b.at.skip = at + 1
	default:
		return false
	}
	return true
}

// atPopup 画候选：浮在输入区上面，左边对齐输入框。
func (a *app) atPopup(c *ui.Context, d *discDetail, box ui.Element, cands []atCandidate, at int, query string) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	hot := min(b.at.hot, len(cands)-1)
	panel := ui.Column(c.Key("at-popup")).Absolute().Left(12).Bottom(box.Bounds().H + 6).Width(sz(300)).Padding(6).Gap(2)
	floating(c, panel).Children(func() {
		for i, x := range cands {
			row := ui.Row(c.Key(fmt.Sprint("at-", i))).Gap(8).Padding(6, 8).Radius(rRow).AlignItems(ui.Center).Role(ui.RoleButton)
			if i == hot {
				row.Background(colors(t).tint)
			} else if row.Hovered() {
				row.Background(colors(t).wash)
			}
			row.Children(func() {
				if x.member != nil {
					agentTile(c, x.member.Agent, smallIcon())
				} else {
					ui.Box(c).Size(smallIcon(), smallIcon()).Center().Shrink(0).Children(func() {
						ui.Icon(c, iconAt).FontSize(fsSmall).TextColor(t.TextMuted)
					})
				}
				ui.Text(c, x.name()).FontSize(fsBody).MaxLines(1).Shrink(1)
				ui.Spacer(c)
				// 图标分得出 Agent 和人；只有用不了的别人的 Agent 要说一句选了会怎样
				if x.member != nil && !x.can {
					ui.Text(c, m.DiscAtOthersAgent(d.ownerName(x.member.Owner))).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(1).Shrink(0)
				}
			})
			if row.Clicked() {
				b.text = a.pickAt(b.text, at, query, x)
			}
		}
	})
}

// canUse 报告本机这个人能不能 @ 这个 Agent、让它执行：自己的，和拥有者在访问设置里放开给自己的。
// 只决定界面里能不能选；房主和拥有者的电脑都会再核对。
func (d *discDetail) canUse(mem discMember) bool {
	if d.mine(mem.Owner) {
		return true
	}
	if d.collab == nil {
		return false
	}
	return mem.Access == "all" || slices.Contains(strings.Split(mem.Access, ","), d.collab.Self)
}

// accessSummary 是成员的访问设置写成一句话：只有拥有者时为空。
func accessSummary(m locales.Messages, d *discDetail, mem discMember) string {
	switch mem.Access {
	case "":
		return ""
	case "all":
		return m.DiscAccessEveryone()
	}
	var names []string
	for _, fp := range strings.Split(mem.Access, ",") {
		names = append(names, d.ownerName(fp))
	}
	return m.DiscAccessPeople(strings.Join(names, m.AppListSeparator()))
}

// memberAccessField 是成员表单里的「谁能 @ 它」：只有我、所有人、指定的人（勾选讨论里的人）。
func (a *app) memberAccessField(c *ui.Context, d *discDetail, f *memberForm) {
	t, m := c.Theme(), L(c)
	ui.Column(c).Gap(6).Children(func() {
		segmented(c, &f.access, m.DiscAccessLabel(), m.DiscAccessOnlyMe(), m.DiscAccessAll(), m.DiscAccessSome())
		if f.access == 2 {
			if f.people == nil {
				f.people = map[string]bool{}
			}
			some := false
			ui.Row(c).Gap(12).Wrap().Children(func() {
				for _, p := range d.collab.People {
					if p.FP == d.collab.Self || p.State != "active" {
						continue
					}
					some = true
					on := f.people[p.FP]
					if ui.Checkbox(c.Key("access-"+p.FP), &on, p.Name).Changed() {
						f.people[p.FP] = on
					}
				}
			})
			if !some {
				ui.Text(c, m.DiscAccessNobody()).FontSize(fsSmall).TextColor(t.TextMuted)
			}
		}
	})
}
