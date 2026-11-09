package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/collaboration"
)

// previewDiscussion 打开一个有两位成员的讨论：发言、请求、完整回复、等你回答的问题、失败和中断的轮次、关键消息都有。
func previewDiscussion(a *app) {
	now := time.Now()
	at := func(min int) time.Time { return now.Add(-time.Duration(min) * time.Minute) }
	key := "fp/d_1"
	a.selected = discBoardID
	ago := at(1)
	a.disc.list = []discSummary{
		{Key: key, Title: "重做登录流程", ProjectID: "p5", ProjectName: "Skill", Status: "active", MsgSeq: 9, CreatedAt: at(90), Questions: 1, Active: 1, Members: 2,
			Last: &discMessage{Kind: "question", AuthorName: "Codex 审查", Body: "要不要保留旧的短信登录入口？", CreatedAt: ago}},
		{Key: "fp/d_2", Title: "IM 重连路径梳理", ProjectID: "p1", ProjectName: "Apps", Status: "active", MsgSeq: 14, CreatedAt: at(300),
			Last: &discMessage{Kind: "reply", AuthorName: "Claude", Body: "四条路径里飞行模式恢复后会重复建连，日志位置见报告。", CreatedAt: at(40)}},
		{Key: "fp/d_3", Title: "安卓 qa 包打包失败", ProjectID: "p6", ProjectName: "Works", Status: "archived", MsgSeq: 6, CreatedAt: at(2000),
			Last: &discMessage{Kind: "reply", AuthorName: "Grok", Body: "JDK 版本不对，已经说明。", CreatedAt: at(1900)}},
	}
	a.state.DiscRead = map[string]int64{key: 9, "fp/d_2": 11, "fp/d_3": 6}
	keyAt := at(30)
	msgs := []discMessage{
		{ID: "m1", Seq: 1, Kind: "user", Body: "这周把登录页重做一下：去掉验证码弹窗，记住密码用钥匙串。先各自看看方案。", CreatedAt: at(80), KeyAt: &keyAt},
		{ID: "m2", Seq: 2, Kind: "user", Body: "先读一下 LoginView.swift，列出要改的地方。", Target: "mb1", TargetName: "Claude 方案", Execution: "x1", CreatedAt: at(70)},
		{ID: "m3", Seq: 3, Kind: "reply", AuthorMember: "mb1", AuthorName: "Claude 方案", AuthorAgent: "claude", Execution: "x1", Outcome: "done", CreatedAt: at(60),
			Report: "/Users/x/Skill/.posse/discussions/mb1/login-plan.md",
			Body:   "## 要改的地方\n\n1. `LoginView.swift`：删掉 `CaptchaSheet`，表单下面加「记住密码」开关。\n2. `Keychain.swift`：新增 `savePassword(_:for:)`，service 用 `com.example.skill`。\n3. `LoginViewModel`：登录成功后按开关决定是否写钥匙串。\n\n不需要改注册流程。"},
		{ID: "m4", Seq: 4, Kind: "user", Body: "复核一下 Claude 的方案，有风险就指出来，然后按方案实现。", Quote: "m3", Target: "mb2", TargetName: "Codex 审查", Execution: "x2", CreatedAt: at(50),
			Attachments: []discAttachment{{ID: "a1", Name: "登录页设计稿.png", Path: ".posse/discussions/d_1/attachments/a1/登录页设计稿.png", Size: 482113}}},
		{ID: "m5", Seq: 5, Kind: "question", AuthorMember: "mb2", AuthorName: "Codex 审查", AuthorAgent: "codex", Execution: "x2", Question: "q1", Body: "要不要保留旧的短信登录入口？去掉验证码弹窗后它也会跟着失效。", CreatedAt: ago},
		{ID: "m6", Seq: 6, Kind: "user", Body: "再试一次打包，把错误贴回来。", Target: "mb1", TargetName: "Claude 方案", Execution: "x3", CreatedAt: at(45)},
		{ID: "m7", Seq: 7, Kind: "reply", AuthorMember: "mb1", AuthorName: "Claude 方案", AuthorAgent: "claude", Execution: "x3", Outcome: "failed", CreatedAt: at(44),
			Body: "打包在 `resguardArmQa` 失败：JDK 版本不对（需要 17，本机是 21）。没有改构建配置。"},
		{ID: "m8", Seq: 8, Kind: "user", Body: "顺便把单测补上。", Target: "mb1", TargetName: "Claude 方案", Execution: "x4", CreatedAt: at(3)},
		{ID: "m9", Seq: 9, Kind: "user", Body: "注册页也看一下有没有同样的问题。", Target: "mb1", TargetName: "Claude 方案", Execution: "x5", CreatedAt: at(2)},
	}
	execs := map[string]discExec{
		"x1": {ID: "x1", Member: "mb1", Message: "m2", State: "done", Agent: "claude", WorkspaceID: "w5", WorkspaceLabel: "Skill/main", TaskID: "k9", Worker: "t9", Terminal: true, CreatedAt: at(70)},
		"x2": {ID: "x2", Member: "mb2", Message: "m4", State: "waiting", Agent: "codex", WorkspaceID: "w6", WorkspaceLabel: "Skill/feat-login", TaskID: "k2", Worker: "t5", Terminal: true, CreatedAt: at(50)},
		"x3": {ID: "x3", Member: "mb1", Message: "m6", State: "failed", Agent: "claude", WorkspaceLabel: "Skill/main", CreatedAt: at(45)},
		"x4": {ID: "x4", Member: "mb1", Message: "m8", State: "interrupted", Reason: "worker agent exited before reporting", Agent: "claude", WorkspaceLabel: "Skill/main", CreatedAt: at(3)},
		"x5": {ID: "x5", Member: "mb1", Message: "m9", State: "held", Reason: "concurrent:Codex 审查", Agent: "claude", WorkspaceLabel: "Skill/main", CreatedAt: at(2)},
	}
	a.disc.sel = key
	a.disc.d = &discDetail{key: key, info: a.disc.list[0], msgs: msgs, execs: execs, keyMsgs: []discMessage{msgs[0], msgs[2]}, md: map[string][]mdBlock{}, rev: 30,
		members: []discMember{
			{ID: "mb1", Name: "Claude 方案", Role: "出方案、写实现", Agent: "claude", Model: "opus", WorkspaceLabel: "Skill/main", TaskID: "k9", Worker: "t9", Paused: "worker agent exited before reporting", ConfigVersion: 2, WorkerConfig: 1},
			{ID: "mb2", Name: "Codex 审查", Role: "复核方案和代码，重点看安全", Agent: "codex", Effort: "high", WorkspaceLabel: "Skill/feat-login", TaskID: "k2", Worker: "t5",
				Terminal: &terminalInfo{ID: "t5", WorkspaceID: "w6", Agent: "codex", Running: true, AgentState: state("working")}},
		}}
	a.disc.d.keyMsgs[1].KeyAt = &keyAt
	a.disc.d.msgs[2].KeyAt = &keyAt
	a.disc.target = "mb1"
	a.disc.quote = "m7"
	a.disc.panel = true
}

func renderDiscussionPreviews(t *testing.T, dark bool, suffix string) {
	a := previewApp(t)
	previewDiscussion(a)
	savePNG(t, newPreviewTester(a, dark), "discussions-"+suffix+".png")

	// 窄窗口、右边一栏收起；往上翻着时来了新消息
	a = previewApp(t)
	previewDiscussion(a)
	a.disc.panel, a.disc.quote = false, ""
	a.disc.seen = 6
	a.state.SidebarHidden = true
	tt := newPreviewTester(a, dark)
	tt.SetSize(900, 700)
	a.disc.scroll.Y = 0
	a.disc.follow = false
	tt.Frame()
	a.disc.seen = 6
	for range 3 {
		tt.Frame()
	}
	savePNG(t, tt, "discussions-narrow-"+suffix+".png")

	// 回答问题、搜索结果
	a = previewApp(t)
	previewDiscussion(a)
	a.disc.answerQ, a.disc.text = "q1", "保留，但放到“其他登录方式”里。"
	a.disc.searchQ, a.disc.search = "search", "钥匙串"
	a.disc.hits = []discMessage{a.disc.d.msgs[0], a.disc.d.msgs[2]}
	savePNG(t, newPreviewTester(a, dark), "discussions-answer-search-"+suffix+".png")

	// 长消息
	a = previewApp(t)
	previewDiscussion(a)
	long := a.disc.d.msgs[2]
	long.ID, long.Seq, long.Body = "m10", 10, strings.Repeat("这是一段很长的回复，用来看换行和滚动。", 30)+"\n\n```go\nfunc savePassword(p string, account string) error {\n\treturn keychain.Set(\"com.example.skill\", account, p)\n}\n```"
	a.disc.d.msgs = append(a.disc.d.msgs, long)
	savePNG(t, newPreviewTester(a, dark), "discussions-long-"+suffix+".png")

	// 图片附件直接显示：一张横的设计稿、一张竖的长截图，再带一个普通文件
	a = previewApp(t)
	previewDiscussion(a)
	previewDiscImages(t, a)
	a.disc.panel, a.state.SidebarHidden = false, true
	a.disc.follow, a.disc.scroll.Y = true, math.MaxFloat32
	tt = newPreviewTester(a, dark)
	for range 3 {
		tt.Frame()
	}
	savePNG(t, tt, "discussions-images-"+suffix+".png")

	// 点开的大图
	for path := range a.discThumbs.m {
		if filepath.Base(path) == "design.png" {
			pi, err := decodeDiscFull(path)
			if err != nil {
				t.Fatal(err)
			}
			a.discView = discViewer{open: true, path: path, bm: ui.NewBitmap(pi.Img), size: pi.Size}
		}
	}
	tt = newPreviewTester(a, dark)
	for range 3 {
		tt.Frame()
	}
	savePNG(t, tt, "discussions-image-viewer-"+suffix+".png")
	a.discView = discViewer{}

	// 归档的讨论
	a = previewApp(t)
	previewDiscussion(a)
	a.disc.d.info.Status = "archived"
	a.disc.list[0].Status = "archived"
	savePNG(t, newPreviewTester(a, dark), "discussions-archived-"+suffix+".png")

	// 空页面、新建讨论和成员对话框
	a = previewApp(t)
	a.selected = discBoardID
	savePNG(t, newPreviewTester(a, dark), "discussions-empty-"+suffix+".png")
	a = previewApp(t)
	previewDiscussion(a)
	a.openNewDiscussion()
	a.disc.newTitle = "重做登录流程"
	savePNG(t, newPreviewTester(a, dark), "discussions-new-"+suffix+".png")
	a = previewApp(t)
	previewDiscussion(a)
	a.daemon.Agents = []string{"claude", "codex", "grok", "cursor"}
	a.openMemberForm(a.disc.d, "mb1")
	a.disc.form.mode = 1
	savePNG(t, newPreviewTester(a, dark), "discussions-member-"+suffix+".png")

	// 讨论 worker 的任务详情：给「打开讨论」，不给追加
	a = previewApp(t)
	previewTaskDetail(a)
	a.tasks[1].DiscussionID = "fp/d_1"
	a.indexTasks()
	a.detail.DiscussionID = "fp/d_1"
	savePNG(t, newPreviewTester(a, dark), "task-discussion-"+suffix+".png")
}

// previewCollab 把预览的讨论变成局域网协作：host 为真时本机是房主、开着分享、有一个待批准的申请；
// 否则本机是加入者，Codex 审查是房主的 Agent，本机只有 Claude 方案，连接断了。
func previewCollab(a *app, host bool) {
	d := a.disc.d
	self, other := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
	if !host {
		self, other = other, self
	}
	a.disc.self = self
	hanaAvatar := previewAvatar(a)
	selfName, selfAvatar := "Mo", ""
	if host {
		selfName, selfAvatar = "Hana", hanaAvatar
	}
	a.profile = profileState{info: profileData{ID: self, Name: selfName, Avatar: selfAvatar}, name: selfName, loaded: true, at: time.Now().Add(time.Hour)}
	cb := &discCollab{Self: self, SelfName: selfName, HostID: strings.Repeat("a1", 32), HostName: "Hana", Host: host, Shared: true, Listening: true, State: "active", Connected: host,
		People: []discPerson{
			{FP: strings.Repeat("a1", 32), Name: "Hana", Avatar: hanaAvatar, State: "active", Host: true, Online: true, Generation: 0},
			{FP: strings.Repeat("b2", 32), Name: "Mo", State: "active", Online: host, Generation: 1},
			{FP: strings.Repeat("c3", 32), Name: "Lin", State: "left", Generation: 1},
		}}
	d.collab = cb
	d.info.Shared = true
	// Codex 审查在 Mo 的电脑上，Mo 放开给了 Hana
	d.members[1].Owner = strings.Repeat("b2", 32)
	d.members[1].Access = strings.Repeat("a1", 32)
	for id, x := range d.execs {
		if x.Member == "mb2" {
			x.Owner = strings.Repeat("b2", 32)
			d.execs[id] = x
		}
	}
	for i := range d.msgs {
		if d.msgs[i].ID == "m4" {
			d.msgs[i].AuthorID, d.msgs[i].AuthorName = strings.Repeat("b2", 32), "Mo"
		}
	}
	d.msgs = append(d.msgs, discMessage{ID: "m10", Seq: 10, Kind: "user", AuthorID: strings.Repeat("b2", 32), AuthorName: "Mo", Mention: strings.Repeat("a1", 32), MentionName: "Hana",
		Body: "我这边的 Codex 在改登录页，Hana 你的 Claude 先别动 LoginView。", CreatedAt: time.Now()})
	if host {
		cb.Requests = []discJoinRequest{{ID: "rq_1", FP: strings.Repeat("d4", 32), Name: "Qiao", Addr: "192.168.1.23:52011", State: "pending", CreatedAt: time.Now()}}
		a.disc.list[0].Shared, a.disc.list[0].Requests = true, 1
		return
	}
	d.info.Mirror = true
	d.info.ProjectName = "Hana"
	cb.Error = "Could not reach the host. Check that both Macs are on the same network, Posse is running on the host with sharing turned on, and Local Network access is allowed for Posse on both Macs (System Settings › Privacy & Security › Local Network)."
	cb.Outbox = 1
	cb.Accepts = map[string]string{}
	// 本机是 Mo：Claude 方案是房主 Hana 的
	d.members[0].Owner = strings.Repeat("a1", 32)
	for id, x := range d.execs {
		if x.Member == "mb1" {
			x.Owner = strings.Repeat("a1", 32)
			d.execs[id] = x
		} else {
			cb.Accepts[id] = "accepted"
		}
	}
	a.disc.list[0].Mirror, a.disc.list[0].ProjectName, a.disc.list[0].Joined = true, "Hana", "active"
	a.disc.target = ""
}

func renderCollabPreviews(t *testing.T, dark bool, suffix string) {
	// 有人 @ 了你（列表标出、消息高亮），输入框里打 @ 弹出候选
	a := previewApp(t)
	previewDiscussion(a)
	previewCollab(a, true)
	a.disc.list[0].Questions, a.disc.list[0].MentionSeq = 0, 10
	a.state.DiscRead[a.disc.list[0].Key] = 9
	a.disc.panel, a.disc.target, a.disc.quote = false, "", ""
	a.disc.follow, a.disc.scroll.Y = true, math.MaxFloat32
	tt := newPreviewTester(a, dark)
	if err := tt.Click("消息（⌘↩ 发送）"); err != nil {
		t.Fatal(err)
	}
	tt.Type("@")
	for range 4 {
		tt.Frame()
		pass(tt, 80*time.Millisecond)
	}
	savePNG(t, tt, "discussions-mention-"+suffix+".png")

	// 加入者改自己的 Agent：谁能 @ 它
	a = previewApp(t)
	previewDiscussion(a)
	previewCollab(a, false)
	a.openMemberForm(a.disc.d, "mb2")
	a.disc.form.access, a.disc.form.people = 2, map[string]bool{strings.Repeat("a1", 32): true}
	savePNG(t, newPreviewTester(a, dark), "discussions-member-access-"+suffix+".png")

	a = previewApp(t)
	previewDiscussion(a)
	previewCollab(a, true)
	a.disc.quote = ""
	savePNG(t, newPreviewTester(a, dark), "discussions-collab-host-"+suffix+".png")

	a = previewApp(t)
	previewDiscussion(a)
	previewCollab(a, false)
	a.disc.quote = ""
	savePNG(t, newPreviewTester(a, dark), "discussions-collab-member-"+suffix+".png")
	// 窄窗口开着详情：讨论列表收起，消息区不被挤窄
	savePNG(t, newPreviewTesterSize(a, dark, 1000, 760), "discussions-narrow-"+suffix+".png")

	a = previewApp(t)
	previewDiscussion(a)
	previewCollab(a, true)
	a.disc.invite = inviteView{open: true, text: "posse-invite:v1:eyJ2IjoxLCJob3N0IjoiYTFhMWExYTFhMWExYTFhMWExYTFhMWExYTFhMWExYTFhMWExYTFhMWExYTFhMWExYTFhMWExYTFhMWExIiwicm9vbSI6ImExIn0",
		expires: time.Now().Add(10 * time.Minute), addrs: []string{"192.168.1.8:6791", "10.0.0.4:6791"}, fp: strings.Repeat("a1", 32)}
	savePNG(t, newPreviewTester(a, dark), "discussions-invite-"+suffix+".png")

	a = previewApp(t)
	previewDiscussion(a)
	inv := collaboration.Invite{V: 1, Host: strings.Repeat("a1", 32), HostName: "Hana", Room: strings.Repeat("a1", 32) + "/d_1", Title: "重做登录流程",
		Addrs: []string{"192.168.1.8:6791"}, Secret: "s"}
	a.disc.join = joinForm{open: true, text: inv.Encode()}
	savePNG(t, newPreviewTester(a, dark), "discussions-join-"+suffix+".png")
}

// previewDiscImages 在临时目录里放两张图片当附件，缩略图预先解好（预览不等后台解码）。
func previewDiscImages(t *testing.T, a *app) {
	dir := t.TempDir()
	d := a.disc.d
	d.info.ProjectID, d.info.ProjectPath = "", dir
	write := func(name string, w, h int, top, bottom color.RGBA) discAttachment {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			k := float64(y) / float64(h)
			c := color.RGBA{uint8(float64(top.R)*(1-k) + float64(bottom.R)*k), uint8(float64(top.G)*(1-k) + float64(bottom.G)*k), uint8(float64(top.B)*(1-k) + float64(bottom.B)*k), 255}
			for x := range w {
				img.SetRGBA(x, y, c)
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		img, err := decodeDiscThumb(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if a.discThumbs.m == nil {
			a.discThumbs.m = map[string]*discThumb{}
		}
		th := &discThumb{}
		th.set(img)
		a.discThumbs.m[filepath.Join(dir, name)] = th
		return discAttachment{ID: name, Name: name, Path: name, Size: int64(buf.Len())}
	}
	atts := []discAttachment{
		write("design.png", 1600, 1000, color.RGBA{243, 242, 239, 255}, color.RGBA{217, 119, 87, 255}),
		write("screenshot.png", 590, 1280, color.RGBA{40, 44, 52, 255}, color.RGBA{91, 141, 239, 255}),
		// 和消息底色一样的白底截图：靠描边看出边界
		write("white.png", 900, 240, color.RGBA{255, 255, 255, 255}, color.RGBA{255, 255, 255, 255}),
		{ID: "notes", Name: "notes.md", Path: "notes.md", Size: 2048},
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.msgs = append(d.msgs, discMessage{ID: "m10", Seq: 10, Kind: "user", Body: "图片不直接显示", Attachments: atts, CreatedAt: time.Now()})
	a.disc.quote = ""
}

// previewAvatar 给预览放一张头像（竖条渐变，看得出裁剪和圆角），直接进缓存，不经磁盘。
func previewAvatar(a *app) string {
	img := image.NewRGBA(image.Rect(0, 0, 96, 96))
	for y := range 96 {
		for x := range 96 {
			img.Set(x, y, color.RGBA{uint8(200 - x), uint8(120 + y), 180, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	bm, err := ui.DecodeBitmap(buf.Bytes())
	if err != nil {
		panic(err)
	}
	sha := strings.Repeat("e5", 32)
	if a.avatars == nil {
		a.avatars = map[string]avatarEntry{}
	}
	a.avatars[sha] = avatarEntry{img: bm, checked: time.Now()}
	return sha
}
