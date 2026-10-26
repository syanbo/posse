package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"rsc.io/qr"
)

type mobileDevice struct {
	ID, Name           string
	PairedAt, LastSeen time.Time
}

func cmdMobilePair(e *env, args []string) error {
	fs := flag.NewFlagSet("mobile pair", flag.ContinueOnError)
	rotate := fs.Bool("new", false, "")
	addr := fs.String("address", "", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var info struct{ URL, Code, Endpoint string }
	return e.do("mobile.pair", map[string]any{"rotate": *rotate, "address": *addr}, &info, func() {
		if err := printQR(e.out, info.URL); err != nil {
			fmt.Fprintln(e.errOut, err)
		}
		fmt.Fprintf(e.out, "\nScan with Posse Mobile. If it will not scan, paste this into the app:\n%s\n\nPhone connects to %s; it must be able to reach this address.\n", info.Code, info.Endpoint)
	})
}

func cmdMobileDevices(e *env, args []string) error {
	var ds []mobileDevice
	return e.do("mobile.devices", nil, &ds, func() {
		if len(ds) == 0 {
			fmt.Fprintln(e.out, "no paired phones; run `posse mobile pair`")
			return
		}
		tw := e.table()
		fmt.Fprintln(tw, "ID\tNAME\tPAIRED\tLAST SEEN")
		for _, d := range ds {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.ID, d.Name, d.PairedAt.Local().Format("2006-01-02 15:04"), d.LastSeen.Local().Format("2006-01-02 15:04"))
		}
		tw.Flush()
	})
}

func cmdMobileRm(e *env, args []string) error {
	if err := need(args, 1, "mobile rm"); err != nil {
		return err
	}
	return e.do("mobile.rm", map[string]any{"device": args[0]}, nil, func() {
		fmt.Fprintf(e.out, "removed %s\n", args[0])
	})
}

type relayStatus struct {
	Configured, Admin bool
	Name, State       string
}

// relayStates 是 relay.status 的 state 对应的说明。
var relayStates = map[string]string{
	"none":        "not configured; run `posse mobile relay set <code>`",
	"ready":       "ready",
	"denied":      "the relay rejected this key",
	"revoked":     "this key has been revoked by the relay admin",
	"clock":       "this computer's clock is off by more than 5 minutes",
	"unreachable": "the relay did not answer (server down, or UDP blocked here)",
}

func printRelay(e *env, st *relayStatus) func() {
	return func() {
		who := ""
		switch {
		case st.Admin:
			who = " (admin)"
		case st.Name != "":
			who = " (" + st.Name + ")"
		}
		fmt.Fprintf(e.out, "relay%s: %s\n", who, relayStates[st.State])
	}
}

func cmdMobileRelay(e *env, args []string) error {
	var st relayStatus
	if len(args) == 0 {
		return e.do("relay.status", nil, &st, printRelay(e, &st))
	}
	switch {
	case args[0] == "set" && len(args) == 2:
		return e.do("relay.set", map[string]any{"code": args[1]}, &st, printRelay(e, &st))
	case args[0] == "clear" && len(args) == 1:
		return e.do("relay.clear", nil, &st, func() { fmt.Fprintln(e.out, "relay cleared") })
	case args[0] == "invite" && len(args) == 2:
	default:
		return need(nil, 1, "mobile relay")
	}
	var out struct{ Code string }
	return e.do("relay.invite", map[string]any{"name": args[1]}, &out, func() {
		fmt.Fprintf(e.out, "%s\n\nOn the other computer: Posse > 手机 > 外网中转, paste this code (or `posse mobile relay set <code>`).\n", out.Code)
	})
}

// printQR 用半格字符画二维码，两行模块合成一行字符；显式指定黑底白块，浅色主题的终端里也扫得出。
func printQR(w io.Writer, text string) error {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return err
	}
	const quiet = 2
	n := code.Size
	light := func(x, y int) bool {
		if x < 0 || y < 0 || x >= n || y >= n {
			return true
		}
		return !code.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		b.WriteString("\x1b[97;40m")
		for x := -quiet; x < n+quiet; x++ {
			top, bot := light(x, y), light(x, y+1)
			switch {
			case top && bot:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bot:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}
