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

	"hive/internal/desk"
	"hive/internal/mobile"
	"hive/internal/rpc"
)

// 手机看 Mac 桌面（方案见 docs/plans/phone-desktop.md）：同一时间只有一台手机在看。
// 采集在子进程里（desk.Spawn），常驻进程只转发、加密、收发 UDP，不碰 ScreenCaptureKit。

// deskStartTimeout 内子进程要交出第一帧的配置，或者说明为什么不行（没授权、已锁屏）。
const deskStartTimeout = 5 * time.Second

// deskProc 是采集子进程。测试里换成假的。
type deskProc interface {
	Frames() io.ReadCloser
	Control() io.WriteCloser
	Wait() error
}

type realDeskProc struct{ p *desk.Proc }

func (r realDeskProc) Frames() io.ReadCloser   { return r.p.Frames }
func (r realDeskProc) Control() io.WriteCloser { return r.p.Control }
func (r realDeskProc) Wait() error             { return r.p.Wait() }

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

func (d *Daemon) deskStart(ctx context.Context, req mobile.DeskRequest, notify func(mobile.DeskEvent)) (mobile.DeskReady, error) {
	codec, err := deskCodec(req.Codecs)
	if err != nil {
		return mobile.DeskReady{}, err
	}
	d.desk.mu.Lock()
	if d.desk.viewer != "" {
		d.desk.mu.Unlock()
		return mobile.DeskReady{}, mobile.DeskError("busy", fmt.Sprintf("%s 正在看这台电脑的桌面", d.desk.viewer))
	}
	d.desk.viewer = req.Device
	if d.desk.viewer == "" {
		d.desk.viewer = "手机"
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
	args := []string{"--long-edge", strconv.Itoa(min(max(req.LongEdge, 2), 1920))}
	if codec == "h264" {
		args = append(args, "--codec", "h264")
	}
	// 测试用：在 iOS 模拟器上看时排除模拟器窗口，不然它被录进画面形成回环
	for _, id := range strings.Split(os.Getenv("HIVE_DESK_EXCLUDE"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			args = append(args, "--exclude-app", id)
		}
	}
	proc, err := deskSpawn(args)
	if err != nil {
		return mobile.DeskReady{}, mobile.DeskError("capture_failed", err.Error())
	}
	abort := func() {
		proc.Control().Close()
		proc.Frames().Close()
		proc.Wait()
	}
	head, status, err := deskFirstFrames(proc.Frames())
	if status != 0 {
		d.deskNoteStatus(status)
	}
	switch {
	case status == desk.StatusNoScreen:
		abort()
		return mobile.DeskReady{}, mobile.DeskError("permission_screen", "还没允许 Hive 录制屏幕")
	case status == desk.StatusLocked:
		abort()
		return mobile.DeskReady{}, mobile.DeskError("locked", "Mac 已锁屏")
	case err != nil:
		abort()
		return mobile.DeskReady{}, mobile.DeskError("capture_failed", err.Error())
	}
	d.deskClearMissing("screen")
	conn, err := net.ListenPacket("udp", ":0")
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
	port := conn.LocalAddr().(*net.UDPAddr).Port
	log.Printf("desk: %s watching, udp %d, %s", req.Device, port, codec)

	var locked bool
	var lmu sync.Mutex
	done := make(chan error, 1)
	control := desk.LockWriter(proc.Control())
	go func() {
		done <- desk.Serve(io.MultiReader(bytes.NewReader(head), proc.Frames()), control, desk.CastConfig{
			Conn: conn, ToMobile: toMobile, ToMac: toMac,
			OnInput: desk.NewInputForwarder(control),
			OnStatus: func(code byte) {
				d.deskNoteStatus(code)
				switch code {
				case desk.StatusLocked:
					// 子进程锁屏时停了采集，解锁也不会自己接着采：结束这次，手机解锁后重新订阅
					lmu.Lock()
					locked = true
					lmu.Unlock()
					cancel()
				case desk.StatusNoAccessibility:
					notify(mobile.DeskEvent{Type: "state", State: "no_accessibility"})
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
		proc.Control().Close()
		conn.Close()
		if !served {
			serveErr = <-done
		}
		proc.Frames().Close()
		proc.Wait()
		cancel()
		d.deskAwake(false)
		d.desk.mu.Lock()
		byUser := d.desk.stopped
		d.desk.viewer, d.desk.cancel, d.desk.stopped = "", nil, false
		d.desk.mu.Unlock()
		lmu.Lock()
		wasLocked := locked
		lmu.Unlock()
		reason := deskEndReason(serveErr, wasLocked, byUser, ctx.Err() != nil)
		log.Printf("desk: %s stopped: %s (%v)", req.Device, reason, serveErr)
		notify(mobile.DeskEvent{Type: "end", Reason: reason})
	}()
	return mobile.DeskReady{Port: port, Codec: codec}, nil
}

// deskCodec 在手机能解的编码里选一个。手机不报时按 HEVC：iPhone 都能硬解。
func deskCodec(codecs []string) (string, error) {
	if len(codecs) == 0 || slices.Contains(codecs, "hevc") {
		return "hevc", nil
	}
	if slices.Contains(codecs, "h264") {
		return "h264", nil
	}
	return "", mobile.DeskError("unsupported_codec", "手机报的编码电脑都不支持")
}

// deskFirstFrames 读到子进程的第一帧配置为止，返回读过的帧（原样交给 Serve 接着处理）。
// 配置之前出现的状态（没授权、已锁屏）作为 status 返回。
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
				if len(f.Payload) > 0 && f.Payload[0] != desk.StatusNoAccessibility {
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
func deskEndReason(err error, locked, byUser, unsubscribed bool) string {
	switch {
	case locked:
		return "locked"
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
