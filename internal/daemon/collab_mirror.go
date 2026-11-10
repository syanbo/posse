package daemon

// 加入的讨论里用户的操作：发言、@ 自己的 Agent、改自己的 Agent、重试，都交给房主做；
// 本机先记下授权意图，房主的结果经同步回到本机缓存。

import (
	"time"

	"posse/internal/collaboration"
	"posse/internal/rpc"
	"posse/internal/store"
)

// mirrorPost 在加入的讨论里发言：附件先分块传给房主，@ 自己的 Agent 时先存授权意图，再提交房主。
// 房主按客户端请求 ID 去重：响应丢了重发不会多一条。离线时报错，草稿留在窗口里，不自动补发。
func (d *Daemon) mirrorPost(disc store.Discussion, p discPostParams) (any, error) {
	if d.guestIn(disc.Key) {
		return nil, guestErr()
	}
	rc, err := d.mirrorConn(disc)
	if err != nil {
		return nil, err
	}
	if p.ClientID == "" {
		p.ClientID = store.NewID("c_") + store.NewID("")
	}
	var target store.Member
	if p.Target != "" {
		if target, err = d.memberSel(p.Target); err != nil {
			return nil, err
		}
		if target.Discussion != disc.Key || target.RemovedAt != nil {
			return nil, discError(store.ErrMemberGone)
		}
		if !d.mine(target.Owner) {
			// 别人的 Agent：它的拥有者放开给你才能 @（房主和拥有者的电脑都会再核对）；授权意图只记自己 Agent 的
			if !target.Allows(d.store.Self(), disc.Host) {
				return nil, notYours(target.Name)
			}
		} else if store.ConfigUntrusted(target.Paused) {
			return nil, rpc.Errorf(rpc.CodeConflict, "%s's settings on this computer need your review; open its settings and save them first", target.Name)
		} else if target.WorkspaceID == "" {
			return nil, rpc.Errorf(rpc.CodeConflict, "choose a workspace on this computer for %s first", target.Name)
		}
	}
	if len(p.Attachments) > discMaxAttachments {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "at most %d attachments per message", discMaxAttachments)
	}
	// 上次发送的响应丢了：先补齐增量，房主已经收下就直接返回，不再传一遍附件
	if len(p.Attachments) > 0 {
		rc.syncNow()
		if m, x, err := d.store.MessageByClientID(disc.Key, p.ClientID); err == nil {
			return discPostResult{Message: m, Execution: x, Duplicate: true}, nil
		}
	}
	var uploads []string
	var atts []store.Attachment
	for _, a := range p.Attachments {
		name := safeFileName(a.Name)
		id, err := rc.upload(a.Path, name)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, id)
		atts = append(atts, store.Attachment{ID: id})
	}
	if target.ID != "" && d.mine(target.Owner) {
		dig := store.IntentDigest(p.Body, p.Quote, target.ID, atts)
		if err := d.store.SaveIntent(disc.Key, p.ClientID, target.ID, target.ConfigVersion, dig, store.OwnConfigDigest(target), time.Now()); err != nil {
			return nil, err
		}
	}
	var res struct {
		Message   string `json:"message"`
		Execution string `json:"execution"`
		Duplicate bool   `json:"duplicate"`
	}
	if err := rc.call("post", collabPostParams{ClientID: p.ClientID, Body: p.Body, Quote: p.Quote, Target: p.Target, Mention: p.Mention, Uploads: uploads}, &res); err != nil {
		return nil, err
	}
	d.taskNotify()
	m, err := d.store.DiscMessage(res.Message)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeConflict, "the host accepted the message but it has not synced yet; it will appear shortly")
	}
	out := discPostResult{Message: m, Duplicate: res.Duplicate}
	if res.Execution != "" {
		if x, err := d.store.Execution(res.Execution); err == nil {
			out.Execution = &x
		}
	}
	return out, nil
}

// mirrorMention 是加入的讨论里本机的 Agent @ 另一个成员：经房主的 post 带上发起的那一轮（from），
// 房主核对这一轮在本机名下、算层数。对象也是本机的 Agent 时照自己发的请求一样先记授权意图，领取时对得上。
func (d *Daemon) mirrorMention(disc store.Discussion, x store.Execution, target store.Member, body, clientID string) (any, error) {
	if !d.store.HostHas(disc.Key, store.FeatureDelegate) {
		return nil, rpc.Errorf(rpc.CodeConflict, "the host of this discussion runs an older Posse where members cannot mention each other; report to the user instead")
	}
	rc, err := d.mirrorConn(disc)
	if err != nil {
		return nil, err
	}
	if d.mine(target.Owner) {
		switch {
		case store.ConfigUntrusted(target.Paused):
			return nil, rpc.Errorf(rpc.CodeConflict, "%s's settings on this computer need the user's review first", target.Name)
		case target.WorkspaceID == "":
			return nil, rpc.Errorf(rpc.CodeConflict, "%s has no workspace on this computer yet", target.Name)
		}
		dig := store.IntentDigest(body, "", target.ID, nil)
		if err := d.store.SaveIntent(disc.Key, clientID, target.ID, target.ConfigVersion, dig, store.OwnConfigDigest(target), time.Now()); err != nil {
			return nil, err
		}
	} else if !target.Allows(d.store.Self(), disc.Host) {
		return nil, notYours(target.Name)
	}
	var res struct {
		Message   string `json:"message"`
		Execution string `json:"execution"`
		Duplicate bool   `json:"duplicate"`
	}
	if err := rc.call("post", collabPostParams{ClientID: clientID, Body: body, Target: target.ID, From: x.ID}, &res); err != nil {
		return nil, err
	}
	d.taskNotify()
	out := discPostResult{Duplicate: res.Duplicate}
	out.Message, _ = d.store.DiscMessage(res.Message)
	if nx, err := d.store.Execution(res.Execution); err == nil {
		out.Execution = &nx
	}
	return out, nil
}

// mirrorRetry 在加入的讨论里为自己的一轮另发起新的一轮：先记授权意图（retry:<旧轮次>），再交给房主。
func (d *Daemon) mirrorRetry(disc store.Discussion, x store.Execution, abandon bool) (any, error) {
	rc, err := d.mirrorConn(disc)
	if err != nil {
		return nil, err
	}
	if abandon && d.store.AcceptState(x.ID) == store.AcceptAccepted && store.ExecActive(x.State) {
		return nil, rpc.Errorf(rpc.CodeConflict, "execution %s is still running on this computer; stop it first", x.ID)
	}
	m, err := d.memberSel(x.Member)
	if err != nil {
		return nil, err
	}
	if store.ConfigUntrusted(m.Paused) {
		return nil, rpc.Errorf(rpc.CodeConflict, "%s's settings on this computer need your review; open its settings and save them first", m.Name)
	}
	if err := d.store.SaveIntent(disc.Key, "retry:"+x.ID, x.Member, m.ConfigVersion, "", store.OwnConfigDigest(m), time.Now()); err != nil {
		return nil, err
	}
	var res struct {
		Execution string `json:"execution"`
	}
	if err := rc.call("retry", map[string]any{"execution": x.ID, "abandon": abandon}, &res); err != nil {
		return nil, err
	}
	d.taskNotify()
	return d.store.Execution(res.Execution)
}

// mirrorMemberUpdate 改自己 Agent 的配置：公开的部分交给房主，执行工作区只改本机。
func (d *Daemon) mirrorMemberUpdate(disc store.Discussion, old, nm store.Member, applyNow bool) (any, error) {
	rc, err := d.mirrorConn(disc)
	if err != nil {
		return nil, err
	}
	if applyNow {
		if q, _ := d.store.MemberQueue(old.ID); len(q) > 0 {
			return nil, discError(store.ErrBusyMember)
		}
	}
	// 每次保存都把本机这份公开配置交给房主（房主那份被改过时，保存一次就对齐），再记成本机确认的配置
	if err := rc.call("member.update", map[string]string{"member": old.ID, "name": nm.Name, "role": nm.Role, "agent": nm.Agent, "model": nm.Model, "effort": nm.Effort, "access": nm.Access}, nil); err != nil {
		return nil, err
	}
	if err := d.store.SetOwnConfig(nm); err != nil {
		return nil, discError(err)
	}
	if applyNow {
		was, err := d.store.ReleaseWorker(old.ID, false)
		if err != nil {
			return nil, discError(err)
		}
		d.closeDiscWorker(was.TaskID, was.Worker)
	}
	d.taskNotify()
	d.discKick()
	return d.store.Member(old.ID)
}

// discCollabInfo 是讨论详情里的协作部分：每次都带（在线状态、连接状态不推进讨论版本）。
type discCollabInfo struct {
	Self        string              `json:"self"`
	SelfName    string              `json:"selfName,omitempty"`
	Host        bool                `json:"host"` // 本机是房主
	HostID      string              `json:"hostId"`
	HostName    string              `json:"hostName,omitempty"`
	Shared      bool                `json:"shared"`
	Listening   bool                `json:"listening"`
	ListenError string              `json:"listenError,omitempty"`
	State       string              `json:"state,omitempty"` // 加入者：pending、active、rejected、removed、left
	Connected   bool                `json:"connected"`
	Error       string              `json:"error,omitempty"`
	People      []discPersonInfo    `json:"people"`
	Requests    []store.JoinRequest `json:"requests,omitempty"`
	Accepts     map[string]string   `json:"accepts,omitempty"` // 本机账本：执行 → accepted/refused/revoked
	Outbox      int                 `json:"outbox,omitempty"`  // 还没交给房主的执行事件
	Rejected    int                 `json:"rejected,omitempty"`
}

type discPersonInfo struct {
	store.Person
	Online bool `json:"online"`
}

func (d *Daemon) collabInfo(disc store.Discussion) *discCollabInfo {
	self := d.store.Self()
	if !disc.Shared && !disc.Mirror {
		return nil
	}
	info := &discCollabInfo{Self: self, Host: !disc.Mirror, HostID: disc.Host, Shared: disc.Shared, People: []discPersonInfo{}}
	if id, err := collaboration.Load(d.cfg.Home); err == nil {
		info.SelfName = id.Name
	}
	people, _ := d.store.People(disc.Key)
	online := map[string]bool{self: true}
	if disc.Mirror {
		j, _ := d.store.Joined(disc.Key)
		info.State, info.HostName, info.Error = j.State, j.HostName, j.Error
		st := d.roomStatus(disc.Key)
		info.Connected = st.Connected
		if st.Error != "" {
			info.Error = st.Error
		}
		online[disc.Host] = st.Connected
		if rc := d.roomConnFor(disc.Key); rc != nil {
			rc.stmu.Lock()
			for fp, t := range rc.presence {
				if st.Connected && time.Since(t) < collaboration.HeartbeatLost {
					online[fp] = true
				}
			}
			rc.stmu.Unlock()
		}
		info.Accepts = d.store.AcceptStates(disc.Key)
		info.Outbox, info.Rejected = d.store.OutboxCount(disc.Key)
	} else {
		d.collab.mu.Lock()
		info.Listening, info.ListenError = d.collab.srv != nil, d.collab.listenErr
		srv := d.collab.srv
		d.collab.mu.Unlock()
		if srv != nil {
			for fp := range srv.Online(disc.Key, collaboration.HeartbeatLost) {
				online[fp] = true
			}
		}
		info.Requests, _ = d.store.JoinRequests(disc.Key, true)
	}
	for _, p := range people {
		if p.Host {
			info.HostName = p.Name
		}
		info.People = append(info.People, discPersonInfo{Person: p, Online: online[p.FP] && p.State == store.PersonActive})
	}
	return info
}
