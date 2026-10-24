package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hive/internal/desk"
)

func init() {
	commands = append(commands,
		command{"desk helper", "Capture the main display and write encoded frames to stdout", "[--seconds N] [--codec hevc|h264] [--bench] [--probe] [--mov FILE] [--exclude-app BUNDLE_ID] [--selftest]", cmdDeskHelper},
		command{"desk spawn", "Run the desktop helper as its own responsible process", "[--seconds N] [--codec hevc|h264] [--bench] [--probe] [--mov FILE] [--exclude-app BUNDLE_ID]", cmdDeskSpawn},
		command{"desk cast", "Send the desktop helper to a UDP receiver", "[--addr HOST:PORT] [--seconds N] [--codec hevc|h264] [--exclude-app BUNDLE_ID]", cmdDeskCast},
	)
}

func deskFlags(args []string) (seconds int, codec string, bench, probe bool, mov string, selfTest bool, addr string, exclude []string, err error) {
	fs := flag.NewFlagSet("desk", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sec := fs.Int("seconds", 0, "")
	c := fs.String("codec", "hevc", "")
	b := fs.Bool("bench", false, "")
	p := fs.Bool("probe", false, "")
	m := fs.String("mov", "", "")
	st := fs.Bool("selftest", false, "")
	ad := fs.String("addr", "127.0.0.1:47991", "")
	var ex multiFlag
	fs.Var(&ex, "exclude-app", "")
	if _, err = parse(fs, args); err != nil {
		return 0, "", false, false, "", false, "", nil, err
	}
	if *c != "hevc" && *c != "h264" {
		return 0, "", false, false, "", false, "", nil, fmt.Errorf("--codec is hevc or h264")
	}
	for _, id := range ex {
		if id == "" {
			return 0, "", false, false, "", false, "", nil, fmt.Errorf("--exclude-app needs a bundle id")
		}
	}
	return *sec, *c, *b, *p, *m, *st, *ad, []string(ex), nil
}

func cmdDeskHelper(_ *env, args []string) error {
	seconds, codec, bench, probe, mov, selfTest, _, exclude, err := deskFlags(args)
	if err != nil {
		return err
	}
	return desk.Run(desk.Options{Seconds: seconds, Codec: codec, Bench: bench, Probe: probe, Mov: mov, SelfTest: selfTest, Exclude: exclude})
}

func cmdDeskSpawn(_ *env, args []string) error {
	seconds, codec, bench, probe, mov, _, _, exclude, err := deskFlags(args)
	if err != nil {
		return err
	}
	return runSpawn(seconds, codec, bench, probe, mov, exclude)
}

func cmdDeskCast(_ *env, args []string) error {
	seconds, codec, _, _, _, _, addr, exclude, err := deskFlags(args)
	if err != nil {
		return err
	}
	var child []string
	if codec != "hevc" {
		child = append(child, "--codec", codec)
	}
	for _, id := range exclude {
		child = append(child, "--exclude-app", id)
	}
	proc, err := desk.Spawn(child)
	if err != nil {
		return err
	}
	defer proc.Control.Close()
	defer proc.Frames.Close()
	done := make(chan error, 1)
	go func() { done <- desk.Cast(proc.Frames, proc.Control, addr) }()
	if seconds > 0 {
		time.AfterFunc(time.Duration(seconds)*time.Second, func() { proc.Control.Close() })
	}
	err = <-done
	proc.Control.Close()
	waitErr := proc.Wait()
	if err != nil && err != io.EOF {
		return err
	}
	return waitErr
}

func runSpawn(seconds int, codec string, bench, probe bool, mov string, exclude []string) error {
	var childArgs []string
	if codec != "hevc" {
		childArgs = append(childArgs, "--codec", codec)
	}
	if bench {
		childArgs = append(childArgs, "--bench")
	}
	if probe {
		childArgs = append(childArgs, "--probe")
	}
	if mov != "" {
		childArgs = append(childArgs, "--mov", mov)
	}
	for _, id := range exclude {
		childArgs = append(childArgs, "--exclude-app", id)
	}
	proc, err := desk.Spawn(childArgs)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "desk: spawned helper pid %d\n", proc.Pid)
	frames := make(chan error, 1)
	go func() { frames <- countFrames(proc.Frames) }()
	if bench || probe {
		err = proc.Wait()
		proc.Control.Close()
		<-frames
		return err
	}
	if seconds > 0 {
		time.Sleep(time.Duration(seconds) * time.Second)
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
