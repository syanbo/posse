package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"posse/internal/desk"
	"posse/internal/deskrelay"
	"posse/internal/errtext"
	"posse/internal/mobile"
	"posse/internal/rpc"
)

// 手机看 Mac 桌面（方案见 docs/plans/phone-desktop.md）：同一时间只有一台手机在看。
// 采集在子进程里（desk.Spawn），常驻进程只转发、加密、收发 UDP，不碰 ScreenCaptureKit。

// deskStartTimeout 内子进程要交出第一帧的配置，或者说明为什么不行（没授权）。
const deskStartTimeout = 5 * time.Second

// deskProc 是采集子进程。测试里换成假的。
type deskProc interface {
	Frames() io.ReadCloser
	Control() io.WriteCloser
	Wait() error
	Kill() error
}

type realDeskProc struct{ p *desk.Proc }

func (r realDeskProc) Frames() io.ReadCloser   { return r.p.Frames }
func (r realDeskProc) Control() io.WriteCloser { return r.p.Control }
func (r realDeskProc) Wait() error             { return r.p.Wait() }
func (r realDeskProc) Kill() error             { return r.p.Kill() }

var deskSpawn = func(args []string) (deskProc, error) {
	p, err := desk.Spawn(args)
	if err != nil {
		return nil, err
	}
	return realDeskProc{p}, nil
}

type deskState struct {
	mu      sync.Mutex
	viewer  string // 正在看的手机名字，空是没人在看
	stopped bool   // 是桌面手机页点了停止
	cancel  context.CancelFunc
	// 最近一次知道的缺失授权：screen（屏幕录制）、accessibility（辅助功能）。只有看过一次才知道。
	missing []string
}

func (h mobileHost) Desk(ctx context.Context, req mobile.DeskRequest, notify func(mobile.DeskEvent)) (mobile.DeskReady, error) {
	return h.d.deskStart(ctx, req, notify)
}

// srcDefaultViewer 是没报名字的手机在看桌面时的叫法。
const srcDefaultViewer = "Phone"

func (d *Daemon) deskStart(ctx context.Context, req mobile.DeskRequest, notify func(mobile.DeskEvent)) (mobile.DeskReady, error) {
	codec, err := deskCodec(req.Codecs)
	if err != nil {
		return mobile.DeskReady{}, err
	}
	d.desk.mu.Lock()
	if d.desk.viewer != "" {
		d.desk.mu.Unlock()
		return mobile.DeskReady{}, mobile.DeskError("busy", fmt.Sprintf("%s is watching this computer’s desktop", d.desk.viewer))
	}
	d.desk.viewer = req.Device
	if d.desk.viewer == "" {
		// 没报名字的手机：这个名字窗口和别的手机都会看到，常驻进程按用户选的语言写
		d.desk.viewer = errtext.MessageIn(errtext.SystemLang(), srcDefaultViewer)
	}
	d.desk.stopped = false
	d.desk.mu.Unlock()
	ready, err := d.deskLaunch(ctx, req, codec, notify)
	if err != nil {
		d.desk.mu.Lock()
		d.desk.viewer = ""
		d.desk.mu.Unlock()
	}
	return ready, err
}

func (d *Daemon) deskLaunch(ctx context.Context, req mobile.DeskRequest, codec string, notify func(mobile.DeskEvent)) (mobile.DeskReady, error) {
	args := []string{"--quiet", "--long-edge", strconv.Itoa(min(max(req.LongEdge, 2), 1920))}
	if codec == "h264" {
		args = append(args, "--codec", "h264")
	}
	// 测试用：在 iOS 模拟器上看时排除模拟器窗口，不然它被录进画面形成回环
	for _, id := range strings.Split(os.Getenv("POSSE_DESK_EXCLUDE"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			args = append(args, "--exclude-app", id)
		}
	}
	spawn := func(display uint32) (deskProc, []byte, byte, error) {
		proc, head, status, err := deskSpawnFirst(deskSpawnArgs(args, display))
		if status != 0 {
			d.deskNoteStatus(status)
		}
		return proc, head, status, err
	}
	proc, head, status, err := spawn(req.Display)
	switch {
	case status == desk.StatusNoScreen:
		return mobile.DeskReady{}, mobile.DeskError("permission_screen", "Posse is not allowed to record the screen yet")
	case err != nil:
		return mobile.DeskReady{}, mobile.DeskError("capture_failed", err.Error())
	}
	abort := func() { stopDeskProc(proc) }
	d.deskClearMissing("screen")
	relayCfg := d.deskRelay()
	conn, err := desk.ListenRoutes(relayCfg.Relay, relayCfg.Credential())
	if err != nil {
		abort()
		return mobile.DeskReady{}, mobile.DeskError("capture_failed", err.Error())
	}
	toMobile, toMac, err := desk.DeriveKeys(req.Secret, req.Nonce)
	if err != nil {
		conn.Close()
		abort()
		return mobile.DeskReady{}, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	d.desk.mu.Lock()
	d.desk.cancel = cancel
	d.desk.mu.Unlock()
	d.deskAwake(true)
	port := conn.Port()
	relay, relayToken := conn.Relay()
	log.Printf("desk: %s watching, udp %d, %s", req.Device, port, codec)

	done := make(chan error, 1)
	frames, out := io.Pipe()
	helper := newDeskHelper(spawn, proc, req.Display, out)
	go helper.run()
	control := desk.LockWriter(helper)
	// 子进程回的窗口列表；一次只问一个，问的人等不到就不要了
	windows := make(chan []byte, 1)
	var wmu sync.Mutex
	fpsChanges := make(chan uint32)
	go func() {
		done <- desk.Serve(io.MultiReader(bytes.NewReader(head), frames), control, desk.CastConfig{
			Conn: conn, ToMobile: toMobile, ToMac: toMac, IsRelay: conn.IsRelay,
			FPS: req.FPS, FPSChanges: fpsChanges,
			OnInput: desk.NewInputForwarder(control),
			OnDisplays: func(ds desk.Displays) {
				notify(mobile.DeskEvent{Type: "displays", Current: ds.Current, Displays: ds.List, Window: ds.Window, Gone: ds.Gone})
			},
			OnWindows: func(b []byte) {
				select {
				case windows <- bytes.Clone(b):
				default:
				}
			},
			OnStatus: func(code byte) {
				d.deskNoteStatus(code)
				switch code {
				case desk.StatusLocked:
					// 锁屏时照常采集，手机在锁屏界面上输密码解锁
					notify(mobile.DeskEvent{Type: "state", State: "locked"})
				case desk.StatusUnlocked:
					notify(mobile.DeskEvent{Type: "state", State: "unlocked"})
				case desk.StatusPasswordTyped:
					notify(mobile.DeskEvent{Type: "state", State: "password_typed"})
				case desk.StatusNoAccessibility:
					notify(mobile.DeskEvent{Type: "state", State: "no_accessibility"})
				case desk.StatusAccessibility:
					notify(mobile.DeskEvent{Type: "state", State: "accessibility"})
				}
			},
		})
	}()
	go func() {
		var serveErr error
		served := false
		select {
		case serveErr = <-done:
			served = true
		case <-runCtx.Done():
		}
		// 关控制管道子进程就退出、松开按着的键；关 UDP 让 Serve 返回
		helper.close()
		conn.Close()
		if !served {
			serveErr = <-done
		}
		frames.Close()
		<-helper.done
		cancel()
		d.deskAwake(false)
		d.desk.mu.Lock()
		byUser := d.desk.stopped
		d.desk.viewer, d.desk.cancel, d.desk.stopped = "", nil, false
		d.desk.mu.Unlock()
		reason := deskEndReason(serveErr, byUser, ctx.Err() != nil)
		log.Printf("desk: %s stopped: %s (%v)", req.Device, reason, serveErr)
		notify(mobile.DeskEvent{Type: "end", Reason: reason})
	}()
	ready := mobile.DeskReady{Port: port, Codec: codec, Relay: relay, RelayToken: relayToken,
		Display: func(id uint32) error {
			return desk.WriteControl(control, desk.Control{Kind: desk.CtrlDisplay, Display: id})
		},
		Window: func(id uint32) error {
			return desk.WriteControl(control, desk.Control{Kind: desk.CtrlWindow, Window: id})
		},
		FPS: func(fps uint32) error {
			select {
			case fpsChanges <- fps:
				return nil
			case <-runCtx.Done():
				return mobile.DeskError("unavailable", "not watching the desktop")
			}
		},
		Windows: func(ctx context.Context) (json.RawMessage, error) {
			wmu.Lock()
			defer wmu.Unlock()
			// 上一次问了没等到的回答可能这时才到，先清掉
			select {
			case <-windows:
			default:
			}
			if err := desk.WriteControl(control, desk.Control{Kind: desk.CtrlWindows}); err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			select {
			case b := <-windows:
				return b, nil
			case <-ctx.Done():
				return nil, mobile.DeskError("timeout", "the capture helper did not list windows")
			}
		},
	}
	if relay != "" {
		ready.Punch = func(ctx context.Context, phone string) (string, error) {
			return deskPunch(ctx, conn, phone)
		}
	}
	return ready, nil
}

// deskPunchFor 是两端互发打洞包的时长，和手机 QUIC 打洞一样。
const deskPunchFor = 4 * time.Second

// deskPunch 是看桌面打洞的 Mac 一侧：问出出口 socket 的公网地址回给手机，同时从它向手机连发打洞包。
// 手机打通后从公网地址发来的 hello、report 解得开，Serve 就改发直连（notePeer）。
func deskPunch(ctx context.Context, conn *desk.Routes, phone string) (string, error) {
	peer, err := net.ResolveUDPAddr("udp4", phone)
	if err != nil || peer.IP == nil || peer.Port == 0 {
		return "", mobile.DeskError("bad_params", fmt.Sprintf("phone address %q", phone))
	}
	pub, err := conn.Public(ctx)
	if err != nil {
		log.Printf("desk: punch: %v", err)
		return "", mobile.DeskError("unavailable", err.Error())
	}
	log.Printf("desk: punch: phone at %s, mac at %s", peer, pub)
	go conn.PunchTo(peer, deskPunchFor)
	return pub.String(), nil
}

// deskCodec 在手机能解的编码里选一个。手机不报时按 HEVC：iPhone 都能硬解。
func deskCodec(codecs []string) (string, error) {
	if len(codecs) == 0 || slices.Contains(codecs, "hevc") {
		return "hevc", nil
	}
	if slices.Contains(codecs, "h264") {
		return "h264", nil
	}
	return "", mobile.DeskError("unsupported_codec", "this computer supports none of the codecs the phone reported")
}

// deskFirstFrames 读到子进程的第一帧配置为止，返回读过的帧（原样交给 Serve 接着处理）。
// 配置之前出现的没授权作为 status 返回；锁屏、缺辅助功能不挡开始，留在 head 里由 Serve 照常处理。
func deskFirstFrames(r io.Reader) (head []byte, status byte, err error) {
	type result struct {
		head   []byte
		status byte
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		var buf bytes.Buffer
		var st byte
		for {
			f, err := desk.ReadFrame(r)
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = errors.New("capture exited before the first frame")
				}
				ch <- result{buf.Bytes(), st, err}
				return
			}
			desk.WriteFrame(&buf, f)
			switch f.Kind {
			case desk.KindStatus:
				if len(f.Payload) > 0 && f.Payload[0] == desk.StatusNoScreen {
					st = f.Payload[0]
				}
			case desk.KindConfig:
				ch <- result{buf.Bytes(), st, nil}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.head, r.status, r.err
	case <-time.After(deskStartTimeout):
		return nil, 0, errors.New("capture did not start in time")
	}
}

// deskEndReason 是推给手机的结束理由，固定的几个词。
func deskEndReason(err error, byUser, unsubscribed bool) string {
	switch {
	case byUser:
		return "stopped"
	case unsubscribed:
		return "unsubscribed"
	case errors.Is(err, desk.ErrNoReport), errors.Is(err, desk.ErrNoHello):
		return "no_report"
	default:
		return "capture_failed"
	}
}

func (d *Daemon) deskNoteStatus(code byte) {
	switch code {
	case desk.StatusNoScreen:
		d.deskAddMissing("screen")
	case desk.StatusNoAccessibility:
		d.deskAddMissing("accessibility")
	case desk.StatusAccessibility:
		d.deskClearMissing("accessibility")
	}
}

func (d *Daemon) deskAddMissing(what string) {
	d.desk.mu.Lock()
	defer d.desk.mu.Unlock()
	if !slices.Contains(d.desk.missing, what) {
		d.desk.missing = append(d.desk.missing, what)
	}
}

func (d *Daemon) deskClearMissing(what string) {
	d.desk.mu.Lock()
	defer d.desk.mu.Unlock()
	d.desk.missing = slices.DeleteFunc(d.desk.missing, func(s string) bool { return s == what })
}

func (d *Daemon) deskAwake(on bool) {
	if d.awake == nil {
		return
	}
	d.awake.setViewing(on)
	d.refreshAwake()
}

// deskStatus 给 daemon.status：谁在看，缺哪几项授权。
func (d *Daemon) deskStatus() (viewer string, missing []string) {
	d.desk.mu.Lock()
	defer d.desk.mu.Unlock()
	return d.desk.viewer, slices.Clone(d.desk.missing)
}

// deskStop 是桌面手机页的「停止」。
func (d *Daemon) deskStop(ctx context.Context, _ json.RawMessage) (any, error) {
	d.desk.mu.Lock()
	cancel := d.desk.cancel
	if cancel != nil {
		d.desk.stopped = true
	}
	d.desk.mu.Unlock()
	if cancel == nil {
		return nil, rpc.Errorf(rpc.CodeNotFound, "nobody is watching the desktop")
	}
	cancel()
	return map[string]bool{"stopped": true}, nil
}

// deskRelay 读 $POSSE_HOME/desk.json 里的中转设置（地址和登记用的密钥）。没有就是不用中转：
// 只有手机经隧道连着、又打不通洞时才用得上，得有一台公网服务器跑 tools/deskrelay。
func (d *Daemon) deskRelay() deskrelay.Config {
	cfg, err := deskrelay.LoadConfig(d.cfg.Home)
	if err != nil {
		log.Printf("desk: %v", err)
	}
	return cfg
}
