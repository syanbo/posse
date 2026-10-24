package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"hive/internal/desk"
)

func init() {
	commands = append(commands,
		command{"desk helper", "Capture the main display and write encoded frames to stdout", "[--seconds N] [--codec hevc|h264] [--long-edge N] [--bench] [--probe] [--mov FILE] [--exclude-app BUNDLE_ID] [--selftest] [--input-test]", cmdDeskHelper},
		command{"desk spawn", "Run the desktop helper as its own responsible process", "[--seconds N] [--codec hevc|h264] [--long-edge N] [--bench] [--probe] [--mov FILE] [--exclude-app BUNDLE_ID] [--input-test]", cmdDeskSpawn},
		command{"desk cast", "Send the desktop helper to a UDP receiver", "[--addr HOST:PORT] [--seconds N] [--codec hevc|h264] [--long-edge N] [--exclude-app BUNDLE_ID]", cmdDeskCast},
	)
}

// deskOpts 是 desk helper、spawn、cast 共用的参数。
type deskOpts struct {
	seconds   int
	codec     string
	bench     bool
	probe     bool
	mov       string
	selfTest  bool
	inputTest bool
	addr      string
	longEdge  int
	exclude   []string
}

func deskFlags(args []string) (deskOpts, error) {
	var o deskOpts
	fs := flag.NewFlagSet("desk", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&o.seconds, "seconds", 0, "")
	fs.StringVar(&o.codec, "codec", "hevc", "")
	fs.BoolVar(&o.bench, "bench", false, "")
	fs.BoolVar(&o.probe, "probe", false, "")
	fs.StringVar(&o.mov, "mov", "", "")
	fs.BoolVar(&o.selfTest, "selftest", false, "")
	fs.BoolVar(&o.inputTest, "input-test", false, "")
	fs.StringVar(&o.addr, "addr", "127.0.0.1:47991", "")
	fs.IntVar(&o.longEdge, "long-edge", 0, "")
	var ex multiFlag
	fs.Var(&ex, "exclude-app", "")
	if _, err := parse(fs, args); err != nil {
		return o, err
	}
	if o.codec != "hevc" && o.codec != "h264" {
		return o, fmt.Errorf("--codec is hevc or h264")
	}
	if o.longEdge < 0 {
		return o, fmt.Errorf("--long-edge must be positive")
	}
	for _, id := range ex {
		if id == "" {
			return o, fmt.Errorf("--exclude-app needs a bundle id")
		}
	}
	o.exclude = ex
	return o, nil
}

// helperArgs 是交给子进程 desk helper 的参数。
func (o deskOpts) helperArgs() []string {
	var args []string
	if o.codec != "hevc" {
		args = append(args, "--codec", o.codec)
	}
	if o.bench {
		args = append(args, "--bench")
	}
	if o.probe {
		args = append(args, "--probe")
	}
	if o.mov != "" {
		args = append(args, "--mov", o.mov)
	}
	if o.inputTest {
		args = append(args, "--input-test")
	}
	if o.longEdge > 0 {
		args = append(args, "--long-edge", strconv.Itoa(o.longEdge))
	}
	for _, id := range o.exclude {
		args = append(args, "--exclude-app", id)
	}
	return args
}

func cmdDeskHelper(_ *env, args []string) error {
	o, err := deskFlags(args)
	if err != nil {
		return err
	}
	return desk.Run(desk.Options{Seconds: o.seconds, Codec: o.codec, Bench: o.bench, Probe: o.probe, Mov: o.mov, SelfTest: o.selfTest, InputTest: o.inputTest, LongEdge: o.longEdge, Exclude: o.exclude})
}

func cmdDeskSpawn(_ *env, args []string) error {
	o, err := deskFlags(args)
	if err != nil {
		return err
	}
	return runSpawn(o)
}

func cmdDeskCast(_ *env, args []string) error {
	o, err := deskFlags(args)
	if err != nil {
		return err
	}
	proc, err := desk.Spawn(o.helperArgs())
	if err != nil {
		return err
	}
	defer proc.Control.Close()
	defer proc.Frames.Close()
	done := make(chan error, 1)
	go func() { done <- desk.Cast(proc.Frames, proc.Control, o.addr) }()
	if o.seconds > 0 {
		time.AfterFunc(time.Duration(o.seconds)*time.Second, func() { proc.Control.Close() })
	}
	err = <-done
	proc.Control.Close()
	waitErr := proc.Wait()
	if err != nil && err != io.EOF {
		return err
	}
	return waitErr
}

func runSpawn(o deskOpts) error {
	proc, err := desk.Spawn(o.helperArgs())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "desk: spawned helper pid %d\n", proc.Pid)
	frames := make(chan error, 1)
	go func() { frames <- countFrames(proc.Frames) }()
	if o.bench || o.probe || o.inputTest {
		err = proc.Wait()
		proc.Control.Close()
		<-frames
		return err
	}
	if o.seconds > 0 {
		time.Sleep(time.Duration(o.seconds) * time.Second)
	} else {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		signal.Stop(ch)
	}
	proc.Control.Close()
	waitErr := proc.Wait()
	frameErr := <-frames
	if waitErr != nil {
		return waitErr
	}
	return frameErr
}

func countFrames(r io.Reader) error {
	var n, key, beats int
	for {
		f, err := desk.ReadFrame(r)
		if err == io.EOF {
			fmt.Fprintf(os.Stderr, "desk: %d frames, %d keyframes, %d heartbeats\n", n, key, beats)
			return nil
		}
		if err != nil {
			return err
		}
		switch f.Kind {
		case desk.KindVideo:
			n++
			if f.Keyframe {
				key++
			}
		case desk.KindHeartbeat:
			beats++
		case desk.KindStatus:
			if len(f.Payload) > 0 {
				fmt.Fprintf(os.Stderr, "desk: status %d\n", f.Payload[0])
			}
		}
	}
}
