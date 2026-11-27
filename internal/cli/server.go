package cli

import (
	"errors"
	"flag"
	"fmt"

	"posse/internal/spawn"
)

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
