package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"posse/internal/remote"
)

func cmdRemoteAdd(e *env, args []string) error {
	fs := flag.NewFlagSet("remote add", flag.ContinueOnError)
	h := remote.Host{}
	fs.StringVar(&h.User, "user", "", "")
	fs.IntVar(&h.Port, "port", 0, "")
	fs.StringVar(&h.Home, "home", "", "")
	fs.StringVar(&h.Executable, "executable", "", "")
	fs.StringVar(&h.ID, "id", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("usage: posse remote add <name> <target> [--user U] [--port N] [--home DIR] [--executable PATH] [--id ID]")
	}
	h.Name, h.Target = pos[0], pos[1]
	if h.ID != "" {
		if prev, ferr := remote.Find(e.home, h.ID); ferr == nil {
			h.Icon, h.IconImage = prev.Icon, prev.IconImage
		}
	}
	h, err = remote.Put(e.home, h)
	if err != nil {
		return err
	}
	return e.emit(h, func() { fmt.Fprintf(e.out, "%s\t%s\t%s\n", h.ID, h.Name, h.Target) })
}

func cmdRemoteList(e *env, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: posse remote list")
	}
	hosts, err := remote.Load(e.home)
	if err != nil {
		return err
	}
	if hosts == nil {
		hosts = []remote.Host{}
	}
	return e.emit(hosts, func() {
		for _, h := range hosts {
			fmt.Fprintf(e.out, "%s\t%s\t%s\n", h.ID, h.Name, h.Target)
		}
	})
}

func cmdRemoteRemove(e *env, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: posse remote rm <host>")
	}
	h, err := remote.Find(e.home, args[0])
	if err != nil {
		return err
	}
	if err := remote.Remove(e.home, h.ID); err != nil {
		return err
	}
	return e.emit(map[string]string{"removed": h.ID}, func() { fmt.Fprintln(e.out, h.ID) })
}

func cmdRemoteCheck(e *env, args []string) error {
	fs := flag.NewFlagSet("remote check", flag.ContinueOnError)
	start := fs.Bool("start", false, "")
	install := fs.Bool("install", false, "")
	file := fs.String("file", "", "")
	batch := fs.Bool("batch", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: posse remote check <host> [--start] [--install] [--file TAR] [--batch]")
	}
	h, err := remote.Find(e.home, pos[0])
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	opts := remote.Options{
		Start:         *start || *install,
		AutoInstall:   *install,
		LocalHome:     e.home,
		ServerArchive: *file,
		Required:      []string{"rpc.v1"},
	}
	if !*batch {
		opts.Prompt = remoteTerminalPrompt
	}
	s, err := remote.Connect(ctx, h, opts)
	if err != nil {
		return err
	}
	defer s.Close()
	return e.emit(s.Info, func() {
		edition := s.Info.Edition
		if edition == "" {
			edition = "full"
		}
		fmt.Fprintf(e.out, "Connected to %s\nhome: %s\ndaemon: %s (%s, %s)\ninstance: %s\ncapabilities: %v\n", h.Name, s.Info.Home, s.Info.Version, s.Info.Build, edition, s.Info.InstanceID, s.Info.Capabilities)
	})
}

func cmdRemoteInstall(e *env, args []string) error {
	fs := flag.NewFlagSet("remote install", flag.ContinueOnError)
	file := fs.String("file", "", "")
	start := fs.Bool("start", true, "")
	launchd := fs.Bool("launchd", false, "")
	systemd := fs.Bool("systemd", false, "")
	batch := fs.Bool("batch", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (*launchd && *systemd) {
		return errors.New("usage: posse remote install <host> [--file TAR] [--start] [--launchd | --systemd] [--batch]")
	}
	h, err := remote.Find(e.home, pos[0])
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	opts := remote.Options{
		Start:          *start,
		InstallLaunchd: *launchd,
		InstallSystemd: *systemd,
		LocalHome:      e.home,
		ServerArchive:  *file,
	}
	if !*batch {
		opts.Prompt = remoteTerminalPrompt
	}
	res, err := remote.Install(ctx, h, opts)
	if err != nil {
		return err
	}
	return e.emit(res, func() {
		state := "installed"
		switch {
		case res.HandedOff:
			state = "installed and handed off (terminals preserved)"
		case res.Started:
			state = "installed and started"
		}
		fmt.Fprintf(e.out, "%s on %s (%s/%s)\nversion: %s (%s)\nexecutable: %s\n", state, h.Name, res.OS, res.Arch, res.Version, res.Tag, res.Executable)
		if res.Launchd != nil {
			lstate := "loaded"
			if !res.Launchd.Loaded {
				lstate = "installed (will load on GUI login)"
			}
			fmt.Fprintf(e.out, "launchd: %s (%s)\n", lstate, res.Launchd.Plist)
			if res.Launchd.Note != "" {
				fmt.Fprintf(e.out, "note: %s\n", res.Launchd.Note)
			}
		}
		if res.Systemd != nil {
			sstate := "enabled and active"
			if !res.Systemd.Enabled {
				sstate = "installed (not yet enabled)"
			} else if !res.Systemd.Active {
				sstate = "enabled (inactive)"
			}
			fmt.Fprintf(e.out, "systemd: %s (%s)\n", sstate, res.Systemd.ServicePath)
			if res.Systemd.Note != "" {
				fmt.Fprintf(e.out, "note: %s\n", res.Systemd.Note)
			}
		}
	})
}

func cmdRemoteAskpass(e *env, args []string) error {
	if len(args) != 1 {
		return errors.New("SSH authentication prompt required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	answer, err := remote.AnswerPrompt(ctx, args[0])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(e.out, answer)
	return err
}
