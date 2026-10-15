package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

func cmdImportOrca(e *env, args []string) error {
	fs := flag.NewFlagSet("import orca", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if !*dry {
		if err := e.ensureDaemon(); err != nil {
			return err
		}
	}
	report, err := e.client().ImportOrca(context.Background(), *dry)
	if err != nil {
		return err
	}
	return e.emit(report, func() { fmt.Fprintln(e.out, strings.Join(report, "\n")) })
}
