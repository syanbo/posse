package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"posse/internal/daemon"
	"posse/internal/spawn"
)

func resolveServerHomeFlag(raw string) string {
	if raw == "" || strings.HasPrefix(raw, "~/") {
		return daemon.ResolveHome(raw)
	}
	return raw
}

func cmdServerInstall(e *env, args []string) error {
	fs := flag.NewFlagSet("server install", flag.ContinueOnError)
	launchd := fs.Bool("launchd", false, "")
	home := fs.String("home", e.home, "")
	interval := fs.Int("interval", 60, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || !*launchd {
		return errors.New("usage: posse server install --launchd [--interval SEC] [--home DIR]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := spawn.InstallLaunchAgent(ctx, spawn.LaunchAgentOptions{
		Home:     resolveServerHomeFlag(*home),
		Interval: *interval,
	})
	if err != nil {
		return err
	}
	return e.emit(st, func() {
		state := "installed and loaded"
		if !st.Loaded {
			state = "installed (not yet loaded)"
		}
		fmt.Fprintf(e.out, "LaunchAgent %s\nlabel: %s\nplist: %s\nexecutable: %s\nhome: %s\n", state, st.Label, st.Plist, st.Executable, st.Home)
		if st.Note != "" {
			fmt.Fprintf(e.out, "note: %s\n", st.Note)
		}
	})
}

func cmdServerUninstall(e *env, args []string) error {
	fs := flag.NewFlagSet("server uninstall", flag.ContinueOnError)
	launchd := fs.Bool("launchd", false, "")
	home := fs.String("home", e.home, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || !*launchd {
		return errors.New("usage: posse server uninstall --launchd [--home DIR]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := spawn.UninstallLaunchAgent(ctx, spawn.LaunchAgentOptions{Home: resolveServerHomeFlag(*home)})
	if err != nil {
		return err
	}
	return e.emit(st, func() {
		fmt.Fprintf(e.out, "LaunchAgent removed\nlabel: %s\nplist: %s\n", st.Label, st.Plist)
	})
}

func cmdServerStatus(e *env, args []string) error {
	fs := flag.NewFlagSet("server status", flag.ContinueOnError)
	launchd := fs.Bool("launchd", true, "")
	home := fs.String("home", e.home, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || !*launchd {
		return errors.New("usage: posse server status [--launchd] [--home DIR]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := spawn.QueryLaunchAgent(ctx, spawn.LaunchAgentOptions{Home: resolveServerHomeFlag(*home)})
	if err != nil {
		return err
	}
	return e.emit(st, func() {
		fmt.Fprintf(e.out, "label: %s\nplist: %s\ninstalled: %v\nloaded: %v\nhome: %s\n", st.Label, st.Plist, st.Installed, st.Loaded, st.Home)
	})
}

func cmdServerCommit(e *env, args []string) error {
	fs := flag.NewFlagSet("server commit", flag.ContinueOnError)
	home := fs.String("home", e.home, "")
	stage := fs.String("stage", "", "")
	tag := fs.String("tag", "", "")
	lib := fs.String("lib", "", "")
	posseSHA := fs.String("posse-sha", "", "")
	libSHA := fs.String("lib-sha", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || *stage == "" || *tag == "" || *lib == "" {
		return errors.New("usage: posse server commit --stage DIR --tag TAG --lib NAME [--home DIR] [--posse-sha SHA] [--lib-sha SHA]")
	}
	exe, err := spawn.CommitServerVersion(*home, *stage, *tag, *lib, *posseSHA, *libSHA)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.out, "POSSE_REMOTE_INSTALLED:%s\n", exe)
	return err
}

func cmdServerActivate(e *env, args []string) error {
	fs := flag.NewFlagSet("server activate", flag.ContinueOnError)
	home := fs.String("home", e.home, "")
	tag := fs.String("tag", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || *tag == "" {
		return errors.New("usage: posse server activate --tag TAG [--home DIR]")
	}
	exe, err := spawn.ActivateServerVersion(*home, *tag)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.out, "POSSE_REMOTE_ACTIVATED:%s\n", exe)
	return err
}
