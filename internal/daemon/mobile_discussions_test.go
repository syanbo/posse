package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	pngenc "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/rpc"
	"posse/internal/store"
)

func mobileDiscCall(d *Daemon, device, method string, p any) (any, error) {
	b, _ := json.Marshal(p)
	return (mobileHost{d}).Discussions(context.Background(), device, "discussion."+method, b)
}
func mustMobileDisc(t *testing.T, d *Daemon, device, method string, p any) map[string]any {
	t.Helper()
	res, err := mobileDiscCall(d, device, method, p)
	if err != nil {
		t.Fatal(err)
	}
	return res.(map[string]any)
}

func TestMobileDiscussionBridge(t *testing.T) {
	d, w, disc := newDiscDaemon(t)
	for _, method := range []string{"read", "ack", "pending", "attachments", "unknown"} {
		if _, err := mobileDiscCall(d, "phone", method, map[string]any{}); err == nil {
			t.Fatalf("allowed %s", method)
		}
	}
	if _, err := mobileDiscCall(d, "phone", "create", map[string]any{"title": "phone", "workspace": w.ID}); err == nil {
		t.Fatal("accepted ambiguous workspace")
	}
	created := mustMobileDisc(t, d, "phone", "create", map[string]any{"title": "phone", "workspace": w.ProjectID + "::" + w.ID, "caller": "nonexistent"})
	if created["workspaceId"] != w.ProjectID+"::"+w.ID {
		t.Fatal(created)
	}
	if _, ok := created["projectPath"]; ok {
		t.Fatal("leaked project path")
	}
	for i := 0; i < 105; i++ {
		postTo(t, d, disc, strings.Repeat("a", 300), "", "")
	}
	page := mustMobileDisc(t, d, "phone", "show", map[string]any{"discussion": disc.Key, "since": 1, "limit": 10000})
	if page["reset"] != true || len(page["messages"].([]any)) != 100 {
		t.Fatal("unbounded delta", page["reset"])
	}
	list := mustMobileDisc(t, d, "phone", "list", map[string]any{})
	for _, v := range list["items"].([]any) {
		if last, ok := v.(map[string]any)["last"].(map[string]any); ok && len(last["body"].(string)) > 200 {
			t.Fatal("full body in list")
		}
	}
}

func TestMobileDiscussionUploads(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	stage := mustMobileDisc(t, d, "phone-a", "upload.begin", map[string]any{"name": "note.txt", "size": 5})["staged"].(string)
	if _, err := mobileDiscCall(d, "phone-b", "upload.commit", map[string]any{"staged": stage}); err == nil {
		t.Fatal("cross-device stage")
	}
	if _, err := mobileDiscCall(d, "phone-a", "upload.chunk", map[string]any{"staged": stage, "offset": 1, "content": []byte("hello")}); err == nil {
		t.Fatal("bad offset")
	}
	mustMobileDisc(t, d, "phone-a", "upload.chunk", map[string]any{"staged": stage, "offset": 0, "content": []byte("hello")})
	mustMobileDisc(t, d, "phone-a", "upload.commit", map[string]any{"staged": stage})
	// 新 mobileHost 和连接仍能认出相同设备的磁盘暂存。
	p := map[string]any{"discussion": disc.Key, "body": "attachment", "clientId": "mobile-once", "attachments": []any{map[string]any{"staged": stage}}}
	res := mustMobileDisc(t, d, "phone-a", "post", p)
	m := res["message"].(map[string]any)
	a := m["attachments"].([]any)[0].(map[string]any)
	if _, ok := a["path"]; ok {
		t.Fatal("leaked path")
	}
	root, _ := d.mobileStage("phone-a")
	if _, err := os.Stat(filepath.Join(root, stage)); !os.IsNotExist(err) {
		t.Fatal("stage not removed")
	}
	dup := mustMobileDisc(t, d, "phone-a", "post", p)
	if dup["duplicate"] != true {
		t.Fatal("duplicate sent again")
	}
	read := mustMobileDisc(t, d, "phone-a", "attachment.read", map[string]any{"discussion": disc.Key, "message": m["id"], "attachment": a["id"]})
	if string(read["content"].([]byte)) != "hello" {
		t.Fatal(read)
	}
	if _, err := mobileDiscCall(d, "phone-a", "attachment.thumb", map[string]any{"discussion": disc.Key, "message": m["id"], "attachment": a["id"]}); err == nil {
		t.Fatal("thumb of a text file")
	}
	other := call(t, d.discCreate, map[string]any{"title": "other", "project": disc.ProjectID}).(store.Discussion)
	if _, err := mobileDiscCall(d, "phone-a", "attachment.read", map[string]any{"discussion": other.Key, "message": m["id"], "attachment": a["id"]}); err == nil {
		t.Fatal("cross-room attachment")
	}
	if _, err := mobileDiscCall(d, "phone-a", "post", map[string]any{"discussion": disc.Key, "body": "bad", "clientId": "bad", "attachments": []any{map[string]any{"path": "/etc/passwd"}}}); err == nil {
		t.Fatal("arbitrary file path")
	}
	raw, _ := d.store.DiscMessage(m["id"].(string))
	path := filepath.Join(disc.ProjectPath, raw.Attachments[0].Path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", path); err != nil {
		t.Fatal(err)
	}
	if _, err := mobileDiscCall(d, "phone-a", "attachment.read", map[string]any{"discussion": disc.Key, "message": m["id"], "attachment": a["id"]}); err == nil {
		t.Fatal("symlink escape")
	}
}

func TestMobileDiscussionThumb(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	var png bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1600, 800))
	if err := pngenc.Encode(&png, img); err != nil {
		t.Fatal(err)
	}
	stage := mustMobileDisc(t, d, "phone-a", "upload.begin", map[string]any{"name": "shot.png", "size": png.Len()})["staged"].(string)
	mustMobileDisc(t, d, "phone-a", "upload.chunk", map[string]any{"staged": stage, "offset": 0, "content": png.Bytes()})
	mustMobileDisc(t, d, "phone-a", "upload.commit", map[string]any{"staged": stage})
	res := mustMobileDisc(t, d, "phone-a", "post", map[string]any{"discussion": disc.Key, "body": "", "clientId": "thumb", "attachments": []any{map[string]any{"staged": stage}}})
	m := res["message"].(map[string]any)
	a := m["attachments"].([]any)[0].(map[string]any)
	th := mustMobileDisc(t, d, "phone-a", "attachment.thumb", map[string]any{"discussion": disc.Key, "message": m["id"], "attachment": a["id"]})
	if th["width"] != 1600 || th["height"] != 800 {
		t.Fatal(th["width"], th["height"])
	}
	got, err := jpeg.Decode(bytes.NewReader(th["content"].([]byte)))
	if err != nil || got.Bounds().Dx() != discThumbWidth {
		t.Fatal(err, got.Bounds())
	}
}

func TestMobileDiscussionUploadLimitsAndExpiry(t *testing.T) {
	d, _, _ := newDiscDaemon(t)
	for _, size := range []int{-1, discMaxAttachSize + 1} {
		if _, err := mobileDiscCall(d, "phone", "upload.begin", map[string]any{"name": "a", "size": size}); err == nil {
			t.Fatal("bad size")
		}
	}
	stage := mustMobileDisc(t, d, "phone", "upload.begin", map[string]any{"name": "a", "size": 2})["staged"].(string)
	if _, err := mobileDiscCall(d, "phone", "upload.commit", map[string]any{"staged": stage}); err == nil {
		t.Fatal("committed incomplete data")
	}
	root, _ := d.mobileStage("phone")
	dir := filepath.Join(root, stage)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	cleanMobileStages(root)
	_, err := mobileDiscCall(d, "phone", "upload.commit", map[string]any{"staged": stage})
	if re, ok := err.(*rpc.Error); !ok || re.Code != "upload_expired" {
		t.Fatal(err)
	}
}

func TestMobileDiscussionSendingDoesNotBlockUploads(t *testing.T) {
	d, _, _ := newDiscDaemon(t)
	staged := mustMobileDisc(t, d, "phone", "upload.begin", map[string]any{"name": "note", "size": 0})["staged"].(string)
	mustMobileDisc(t, d, "phone", "upload.commit", map[string]any{"staged": staged})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := d.withMobileStages("phone", []string{staged}, func(atts []discAttachIn) (any, error) {
			close(entered)
			<-release
			_, err := os.Stat(atts[0].Path)
			return nil, err
		})
		done <- err
	}()
	<-entered
	other := make(chan error, 1)
	go func() {
		_, err := mobileDiscCall(d, "phone", "upload.begin", map[string]any{"name": "other", "size": 0})
		other <- err
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("network send held upload lock")
	}
	if _, err := mobileDiscCall(d, "phone", "upload.abort", map[string]any{"staged": staged}); err == nil {
		t.Fatal("deleted a sending attachment")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	root, _ := d.mobileStage("phone")
	if _, err := os.Stat(filepath.Join(root, staged)); !os.IsNotExist(err) {
		t.Fatal("sent stage remains")
	}
}

func TestMobileDiscussionUploadChunkRetry(t *testing.T) {
	d, _, _ := newDiscDaemon(t)
	staged := mustMobileDisc(t, d, "phone", "upload.begin", map[string]any{"name": "note", "size": 5})["staged"].(string)
	p := map[string]any{"staged": staged, "offset": 0, "content": []byte("hello")}
	mustMobileDisc(t, d, "phone", "upload.chunk", p)
	mustMobileDisc(t, d, "phone", "upload.chunk", p)
	p["content"] = []byte("other")
	if _, err := mobileDiscCall(d, "phone", "upload.chunk", p); err == nil {
		t.Fatal("changed duplicate chunk accepted")
	}
	mustMobileDisc(t, d, "phone", "upload.commit", map[string]any{"staged": staged})
}

func TestMobileDiscussionAnswerRetryAndReport(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	m := addMember(t, d, disc, "reviewer", "claude")
	post := postTo(t, d, disc, "review", m.ID, "")
	x := waitExec(t, d, post.Execution.ID, store.ExecRunning)
	readAll(t, d, x)
	asked := make(chan error, 1)
	go func() {
		_, err := callErr(d.taskAsk, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "question": "which?", "timeoutMs": 5000})
		asked <- err
	}()
	waitExec(t, d, x.ID, store.ExecWaiting)
	msgs, err := d.store.DiscMessages(disc.Key, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var question string
	for _, msg := range msgs {
		if msg.Kind == store.DiscQuestion {
			question = msg.Question
		}
	}
	if question == "" {
		t.Fatal("no question")
	}
	p := map[string]any{"question": question, "body": "main", "clientId": "answer-once"}
	mustMobileDisc(t, d, "phone", "answer", p)
	if err := <-asked; err != nil {
		t.Fatal(err)
	}
	mustMobileDisc(t, d, "phone", "answer", p)
	report := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(report, []byte("# Full report"), 0600); err != nil {
		t.Fatal(err)
	}
	call(t, d.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "done", "summary": "done", "report": report})
	msgs, err = d.store.DiscMessages(disc.Key, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var reply store.DiscMessage
	for _, msg := range msgs {
		if msg.Kind == store.DiscReply {
			reply = msg
		}
	}
	data := mustMobileDisc(t, d, "phone", "report.read", map[string]any{"discussion": disc.Key, "message": reply.ID})
	if string(data["content"].([]byte)) != "# Full report" {
		t.Fatal(data)
	}
	if _, err := mobileDiscCall(d, "phone", "report.read", map[string]any{"discussion": disc.Key, "message": post.Message.ID}); err == nil {
		t.Fatal("report from message without one")
	}
}

func TestMobileDiscussionLocalMemberRetry(t *testing.T) {
	d, w, disc := newDiscDaemon(t)
	p := map[string]any{"discussion": disc.Key, "workspace": w.ProjectID + "::" + w.ID, "name": "reviewer", "agent": "claude", "clientId": "member-once"}
	first := mustMobileDisc(t, d, "phone", "member.add", p)
	second := mustMobileDisc(t, d, "phone", "member.add", p)
	if first["id"] != second["id"] {
		t.Fatal("duplicate member", first, second)
	}
}
