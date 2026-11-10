package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"strings"
	"unicode/utf8"

	"posse/internal/collaboration"
	"posse/internal/rpc"
	"posse/internal/store"
)

// 个人资料：名字和头像跟着安装身份走（collab.json），本机所有讨论、配对的手机都用这一份。
// 房主的讨论直接改自己那一行、按版本增量同步；加入的讨论连上房主后交过去（profileLocked）。

func ownProfile(id collaboration.Identity) store.Profile {
	return store.Profile{Name: id.Name, Avatar: id.Avatar, Rev: id.ProfileRev}
}

type profileRoom struct {
	Room   string `json:"room"`
	Title  string `json:"title"`
	Host   string `json:"host,omitempty"`
	Reason string `json:"reason,omitempty"` // offline、unsupported、error 的原文
}

type profileInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Avatar string `json:"avatar,omitempty"`
	Rev    int64  `json:"rev"`
	// Pending 是房主还没收下当前资料的加入的讨论
	Pending []profileRoom `json:"pending,omitempty"`
}

func (d *Daemon) profileInfo(id collaboration.Identity) profileInfo {
	out := profileInfo{ID: id.ID, Name: id.Name, Avatar: id.Avatar, Rev: id.ProfileRev}
	joined, _ := d.store.JoinedRooms()
	for _, j := range joined {
		if j.State != store.PersonActive || d.store.ProfileSent(j.Room) >= id.ProfileRev {
			continue
		}
		r := profileRoom{Room: j.Room, Host: j.HostName, Reason: "offline"}
		if disc, err := d.store.Discussion(j.Room); err == nil {
			r.Title = disc.Title
		}
		if rc := d.roomConnFor(j.Room); rc != nil {
			rc.stmu.Lock()
			if rc.profileErr != "" {
				r.Reason = rc.profileErr
			}
			rc.stmu.Unlock()
		}
		out.Pending = append(out.Pending, r)
	}
	return out
}

func (d *Daemon) profileGet(ctx context.Context, raw json.RawMessage) (any, error) {
	id, err := collaboration.Load(d.cfg.Home)
	if errors.Is(err, fs.ErrNotExist) {
		// 还没建过、加入过讨论：只是看一眼资料不生成身份，保存时才生成
		return profileInfo{Name: collaboration.DefaultName()}, nil
	}
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "installation identity: %v", err)
	}
	return d.profileInfo(id), nil
}

// profileSet 改名字、头像。头像三选一：path（本机文件，窗口和 CLI）、data（图片字节，手机）、
// remove；都不带就不动头像。
func (d *Daemon) profileSet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Name   *string `json:"name"`
		Path   string  `json:"path"`
		Data   []byte  `json:"data"`
		Remove bool    `json:"remove"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := d.userOnly(raw); err != nil {
		return nil, err
	}
	cur, err := d.identity()
	if err != nil {
		return nil, err
	}
	var name, avatar *string
	if p.Name != nil {
		v := strings.TrimSpace(*p.Name)
		if v == "" || utf8.RuneCountInString(v) > 60 {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "name must be 1-60 characters")
		}
		if v != cur.Name {
			name = &v
		}
	}
	src := p.Data
	if p.Path != "" {
		st, err := os.Stat(p.Path)
		if err != nil || !st.Mode().IsRegular() || st.Size() > collaboration.MaxAvatarSource {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", collaboration.ErrBadAvatar)
		}
		if src, err = os.ReadFile(p.Path); err != nil {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "read image: %v", err)
		}
	}
	var img []byte
	if len(src) > 0 && !p.Remove {
		// 解码、缩放在锁外做：原图可能很大
		if img, err = collaboration.NormalizeAvatar(src); err != nil {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
		}
	}
	d.avatarMu.Lock()
	defer d.avatarMu.Unlock()
	switch {
	case p.Remove && cur.Avatar != "":
		avatar = new(string)
	case img != nil:
		sha, err := collaboration.SaveAvatar(d.cfg.Home, img)
		if err != nil {
			return nil, rpc.Errorf(rpc.CodeInternal, "save avatar: %v", err)
		}
		if sha != cur.Avatar {
			avatar = &sha
		}
	}
	if name == nil && avatar == nil {
		return d.profileInfo(cur), nil
	}
	id, err := collaboration.SetProfile(d.cfg.Home, name, avatar)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "save profile: %v", err)
	}
	if err := d.store.SetOwnProfile(id.ID, ownProfile(id)); err != nil {
		return nil, err
	}
	if avatar != nil {
		d.pruneAvatarsLocked()
	}
	d.profileKick()
	d.taskNotify()
	return d.profileInfo(id), nil
}

// profileAvatar 给手机读头像（窗口直接读文件）。
func (d *Daemon) profileAvatar(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		SHA string `json:"sha"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	b, err := collaboration.ReadAvatar(d.cfg.Home, p.SHA)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeNotFound, "avatar not found")
	}
	return map[string]any{"sha": p.SHA, "data": b}, nil
}

// pruneAvatars 在启动时清掉上次留下的、没人在用的头像。
func (d *Daemon) pruneAvatars() {
	d.avatarMu.Lock()
	defer d.avatarMu.Unlock()
	d.pruneAvatarsLocked()
}

// pruneAvatarsLocked 删掉自己和各讨论里都没人在用的头像：换下来的、离开的讨论里的。调用方持有 avatarMu。
func (d *Daemon) pruneAvatarsLocked() {
	keep, err := d.store.AvatarsInUse()
	if err != nil {
		return
	}
	id, err := collaboration.Load(d.cfg.Home)
	switch {
	case err == nil:
		if id.Avatar != "" {
			keep[id.Avatar] = true
		}
	case !errors.Is(err, fs.ErrNotExist):
		return // 身份读不出来时不删，免得删掉自己的头像
	}
	if err := collaboration.PruneAvatars(d.cfg.Home, keep); err != nil {
		log.Printf("prune avatars: %v", err)
	}
}

// profileKick 让各个加入的讨论把新资料交给房主。
func (d *Daemon) profileKick() {
	joined, _ := d.store.JoinedRooms()
	for _, j := range joined {
		if rc := d.roomConnFor(j.Room); rc != nil {
			rc.poke()
		}
	}
}

// --- 房主一侧

type profileParams struct {
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
	Rev    int64  `json:"rev"`
	Data   []byte `json:"data,omitempty"`
}

// serveProfile 收下成员交来的资料：只能改连接身份自己（session 的指纹），头像先核对再存。
func (h collabHost) serveProfile(s *collaboration.Session, room string, gen int64, raw json.RawMessage) (any, error) {
	d := h.d
	var p profileParams
	if err := json.Unmarshal(raw, &p); err != nil || p.Rev <= 0 {
		return nil, collaboration.Errorf(collaboration.CodeBadRequest, "bad request")
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 60 {
		return nil, collaboration.Errorf(collaboration.CodeBadRequest, "name must be 1-60 characters")
	}
	// 先看版本：重发、旧版本和已撤销的成员交来的头像不落盘
	cur, err := d.store.MemberProfileRev(room, s.FP, gen)
	if err != nil {
		return nil, cerr(err)
	}
	if p.Rev <= cur {
		return map[string]any{"rev": p.Rev}, nil
	}
	// 看文件在不在要和回收在同一把锁里
	d.avatarMu.Lock()
	defer d.avatarMu.Unlock()
	if p.Avatar != "" && !collaboration.HasAvatar(d.cfg.Home, p.Avatar) {
		if len(p.Data) == 0 {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "avatar data required")
		}
		if err := collaboration.CheckAvatar(p.Data, p.Avatar); err != nil {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "%v", err)
		}
		if _, err := collaboration.SaveAvatar(d.cfg.Home, p.Data); err != nil {
			return nil, err
		}
	}
	changed, err := d.store.ApplyProfile(room, s.FP, gen, store.Profile{Name: p.Name, Avatar: p.Avatar, Rev: p.Rev})
	// 每人只留在用的一份：换下来的、没收下的都删掉，成员反复换头像也占不了房主的磁盘
	d.pruneAvatarsLocked()
	if err != nil {
		return nil, cerr(err)
	}
	if changed {
		d.taskNotify()
	}
	return map[string]any{"rev": p.Rev}, nil
}

// serveAvatar 给成员取讨论里某人的头像：只给这个讨论里有人在用的。
func (h collabHost) serveAvatar(room string, raw json.RawMessage) (any, error) {
	var p struct {
		SHA string `json:"sha"`
	}
	json.Unmarshal(raw, &p)
	if !collaboration.ValidSHA(p.SHA) || !h.d.store.AvatarInRoom(room, p.SHA) {
		return nil, collaboration.Errorf(collaboration.CodeNotFound, "avatar not found")
	}
	b, err := collaboration.ReadAvatar(h.d.cfg.Home, p.SHA)
	if err != nil {
		return nil, collaboration.Errorf(collaboration.CodeNotFound, "avatar not found")
	}
	return map[string]any{"data": b}, nil
}

// --- 成员一侧（roomConn.step 里，持有 rc.mu）

// profileLocked 在本机资料比交过的新时交给房主。交不上（房主是旧版、不收）的同一版本这次连接不再重试，
// 重连或改了资料再交。
func (rc *roomConn) profileLocked(ctx context.Context) error {
	id, err := collaboration.Load(rc.d.cfg.Home)
	if err != nil || id.ProfileRev <= rc.d.store.ProfileSent(rc.room) {
		return nil
	}
	rc.stmu.Lock()
	skip := rc.profileErr != "" && rc.profileTried == id.ProfileRev
	rc.stmu.Unlock()
	if skip {
		return nil
	}
	p := profileParams{Name: id.Name, Avatar: id.Avatar, Rev: id.ProfileRev}
	if id.Avatar != "" {
		if p.Data, err = collaboration.ReadAvatar(rc.d.cfg.Home, id.Avatar); err != nil {
			p.Avatar = "" // 头像文件丢了：只交名字
		}
	}
	err = rc.callLocked(ctx, collabCallTimeout, "profile", p, nil)
	rc.stmu.Lock()
	rc.profileTried = id.ProfileRev
	switch {
	case err == nil:
		rc.profileErr = ""
	case collaboration.IsCode(err, collaboration.CodeBadRequest) && strings.Contains(err.Error(), "unknown method"):
		rc.profileErr = "unsupported"
	default:
		rc.profileErr = err.Error()
	}
	rc.stmu.Unlock()
	if err != nil {
		var ce *collaboration.Error
		if errors.As(err, &ce) && ce.Code != collaboration.CodeRevoked && ce.Code != collaboration.CodeUnauthorized {
			log.Printf("collaboration %s: profile: %v", short(rc.room), err)
			return nil // 房主不收（旧版、图片不合格）不影响同步和领取
		}
		return rc.callFailed(err)
	}
	if err := rc.d.store.SetProfileSent(rc.room, id.ProfileRev); err != nil {
		return err
	}
	rc.d.taskNotify()
	return nil
}

// avatarsLocked 取讨论里别人用着、本机还没有的头像；取不到的这次连接不再试（显示回退成首字）。
func (rc *roomConn) avatarsLocked(ctx context.Context) {
	shas, err := rc.d.store.RoomAvatars(rc.room)
	if err != nil {
		return
	}
	got := false
	for _, sha := range shas {
		if !collaboration.ValidSHA(sha) || collaboration.HasAvatar(rc.d.cfg.Home, sha) {
			continue
		}
		rc.stmu.Lock()
		failed := rc.avatarFailed[sha]
		rc.stmu.Unlock()
		if failed {
			continue
		}
		var res struct {
			Data []byte `json:"data"`
		}
		err := rc.callLocked(ctx, collabCallTimeout, "avatar", map[string]string{"sha": sha}, &res)
		if err == nil {
			err = collaboration.CheckAvatar(res.Data, sha)
		}
		if err == nil {
			_, err = collaboration.SaveAvatar(rc.d.cfg.Home, res.Data)
		}
		if err != nil {
			rc.stmu.Lock()
			if rc.avatarFailed == nil {
				rc.avatarFailed = map[string]bool{}
			}
			rc.avatarFailed[sha] = true
			rc.stmu.Unlock()
			continue
		}
		got = true
	}
	if got {
		rc.d.taskNotify()
	}
}
