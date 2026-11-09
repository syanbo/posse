package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"posse/internal/collaboration"
	"posse/internal/store"
)

func testPNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func person(t *testing.T, d *Daemon, room, fp string) store.Person {
	t.Helper()
	p, err := d.store.Person(room, fp)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// 两边各改一次名字和头像：房主的改动同步到成员，成员的改动交给房主；旧版本不覆盖新的，头像能取回、能去掉。
func TestProfileSyncBothWays(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	room := p.disc.Key
	hostFP, memberFP := h.store.Self(), m.store.Self()

	res := call(t, m.profileSet, map[string]any{"name": "Mei", "data": testPNG(t, 640, 480, color.RGBA{200, 0, 0, 255})}).(profileInfo)
	if res.Avatar == "" || res.Name != "Mei" {
		t.Fatalf("member profile %+v", res)
	}
	waitFor(t, "host sees member profile", func() bool {
		pp, _ := h.store.Person(room, memberFP)
		return pp.Name == "Mei" && pp.Avatar == res.Avatar
	})
	if !collaboration.HasAvatar(h.cfg.Home, res.Avatar) {
		t.Fatal("host did not store the member's avatar")
	}
	waitFor(t, "profile marked sent", func() bool {
		return len(call(t, m.profileGet, map[string]any{}).(profileInfo).Pending) == 0
	})

	hres := call(t, h.profileSet, map[string]any{"name": "Hana", "data": testPNG(t, 300, 300, color.RGBA{0, 0, 200, 255})}).(profileInfo)
	waitFor(t, "member sees host profile and avatar", func() bool {
		pp, _ := m.store.Person(room, hostFP)
		return pp.Name == "Hana" && pp.Avatar == hres.Avatar && collaboration.HasAvatar(m.cfg.Home, hres.Avatar)
	})

	// 重发旧版本：房主不动
	cur := person(t, h, room, memberFP)
	if ok, err := h.store.ApplyProfile(room, memberFP, cur.Generation, store.Profile{Name: "Old", Rev: cur.ProfileRev - 1}); err != nil || ok {
		t.Fatalf("stale profile applied: %v %v", ok, err)
	}
	// 冒充：成员资格不是这一代的不收
	if _, err := h.store.ApplyProfile(room, memberFP, cur.Generation+1, store.Profile{Name: "X", Rev: cur.ProfileRev + 1}); err == nil {
		t.Fatal("profile accepted for another generation")
	}

	// 换头像：两边都只留在用的一份，对方的头像不删
	res2 := call(t, m.profileSet, map[string]any{"data": testPNG(t, 300, 300, color.RGBA{0, 200, 0, 255})}).(profileInfo)
	waitFor(t, "host sees new avatar", func() bool {
		return person(t, h, room, memberFP).Avatar == res2.Avatar
	})
	if collaboration.HasAvatar(h.cfg.Home, res.Avatar) || collaboration.HasAvatar(m.cfg.Home, res.Avatar) {
		t.Fatal("replaced avatar kept")
	}
	if !collaboration.HasAvatar(h.cfg.Home, hres.Avatar) || !collaboration.HasAvatar(m.cfg.Home, hres.Avatar) {
		t.Fatal("avatar in use was pruned")
	}

	// 去掉头像也同步过去，旧缓存不把它带回来
	call(t, m.profileSet, map[string]any{"remove": true})
	waitFor(t, "avatar removed on host", func() bool {
		return person(t, h, room, memberFP).Avatar == ""
	})
	if person(t, m, room, memberFP).Avatar != "" {
		t.Fatal("member's own row kept the removed avatar")
	}
	if collaboration.HasAvatar(h.cfg.Home, res2.Avatar) {
		t.Fatal("removed avatar kept on host")
	}
}

// 房主收到旧版本的资料：头像不落盘。
func TestProfileStaleAvatarNotSaved(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	room, memberFP := p.disc.Key, m.store.Self()
	call(t, m.profileSet, map[string]any{"name": "Mei"})
	waitFor(t, "host sees member profile", func() bool { return person(t, h, room, memberFP).Name == "Mei" })
	cur := person(t, h, room, memberFP)
	img, err := collaboration.NormalizeAvatar(testPNG(t, 64, 64, color.Black))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(img)
	sha := hex.EncodeToString(sum[:])
	raw, _ := json.Marshal(profileParams{Name: "Old", Avatar: sha, Rev: cur.ProfileRev, Data: img})
	if _, err := (collabHost{h}).serveProfile(&collaboration.Session{FP: memberFP}, room, cur.Generation, raw); err != nil {
		t.Fatal(err)
	}
	if collaboration.HasAvatar(h.cfg.Home, sha) {
		t.Fatal("stale profile saved its avatar")
	}
}

// 只看资料不生成安装身份。
func TestProfileGetKeepsIdentity(t *testing.T) {
	d, _ := newTestDaemon(t)
	path := filepath.Join(d.cfg.Home, collaboration.FileName)
	os.Remove(path)
	if res := call(t, d.profileGet, map[string]any{}).(profileInfo); res.Name == "" {
		t.Fatalf("no default name: %+v", res)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("profile.get created the identity: %v", err)
	}
}

// 头像只收合格的图：不是 PNG/JPEG、摘要对不上的都拒绝。
func TestProfileAvatarValidation(t *testing.T) {
	d, _ := newTestDaemon(t)
	if _, err := callErr(d.profileSet, map[string]any{"data": []byte("not an image")}); err == nil {
		t.Fatal("bad image accepted")
	}
	if _, err := callErr(d.profileSet, map[string]any{"name": ""}); err == nil {
		t.Fatal("empty name accepted")
	}
	img, err := collaboration.NormalizeAvatar(testPNG(t, 1000, 200, color.White))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(img))
	if cfg.Width != 200 || cfg.Height != 200 {
		t.Fatalf("crop %dx%d", cfg.Width, cfg.Height)
	}
	if err := collaboration.CheckAvatar(img, "00"); err == nil {
		t.Fatal("bad digest accepted")
	}
}

// 成员改名后，房主讨论列表里最后一条消息的预览写新名字（消息存的是发送时的名字）。
func TestProfileRenameUpdatesListPreview(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	room, memberFP := p.disc.Key, m.store.Self()
	call(t, m.profileSet, map[string]any{"name": "Old Name"})
	waitFor(t, "host sees name", func() bool { return person(t, h, room, memberFP).Name == "Old Name" })
	call(t, m.discPost, map[string]any{"discussion": room, "body": "hello", "clientId": "c-preview"})
	last := func() string {
		list, err := h.store.DiscussionSummaries()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range list {
			if s.Key == room && s.Last != nil {
				return s.Last.AuthorName
			}
		}
		return ""
	}
	waitFor(t, "host sees the message", func() bool { return last() == "Old Name" })
	call(t, m.profileSet, map[string]any{"name": "New Name"})
	waitFor(t, "preview uses the new name", func() bool { return last() == "New Name" })
}
