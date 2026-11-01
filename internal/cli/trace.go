package cli

import (
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

type traceInfo struct {
	Path        string    `json:"path"`
	Kind        string    `json:"kind"`
	WorkspaceID string    `json:"workspaceId"`
	TerminalID  string    `json:"terminalId"`
	Agent       string    `json:"agent"`
	Source      string    `json:"source"`
	At          time.Time `json:"at"`
	IsDir       bool      `json:"isDir"`
	Running     bool      `json:"running"`
}

func cmdTraceList(e *env, args []string) error {
	fs := flag.NewFlagSet("trace list", flag.ContinueOnError)
	ws := fs.String("workspace", "", "")
	project := fs.String("project", "", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var ts []traceInfo
	return e.do("trace.list", map[string]any{"workspace": *ws, "project": *project}, &ts, func() { printTraces(e.out, ts) })
}

func printTraces(out io.Writer, ts []traceInfo) {
	if len(ts) == 0 {
		fmt.Fprintln(out, "no traces")
		return
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tAGENT\tTERMINAL\tSOURCE\tWHEN\tPATH")
	for _, t := range ts {
		path := t.Path
		if t.IsDir {
			path += "/"
		}
		term := t.TerminalID
		if t.Running {
			term += "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", t.Kind, t.Agent, term, t.Source, t.At.Local().Format("01-02 15:04"), path)
	}
	w.Flush()
}
