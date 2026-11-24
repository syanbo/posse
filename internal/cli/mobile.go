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
		fmt.Fprintf(e.out, "%s\n\nOn the other computer: Posse > Mobile > Relay, paste this code (or `posse mobile relay set <code>`).\n", out.Code)
	})
}

type tailscaleStatus struct {
	Enabled              bool     `json:"enabled"`
	State                string   `json:"state"`
	IP                   string   `json:"ip"`
	DNSName              string   `json:"dnsName"`
	Hostname             string   `json:"hostname"`
	ControlURL           string   `json:"controlUrl"`
	AuthURL              string   `json:"authUrl"`
	DisableBindInterface bool     `json:"disableBindInterface"`
	Error                string   `json:"error"`
	Health               []string `json:"health"`
}

func printTailscale(e *env, st *tailscaleStatus) func() {
	return func() {
		fmt.Fprintf(e.out, "tailscale: %s\n", st.State)
		if st.IP != "" {
			fmt.Fprintf(e.out, "ip: %s\n", st.IP)
		}
		if st.Hostname != "" {
			fmt.Fprintf(e.out, "hostname: %s\n", st.Hostname)
		}
		if st.ControlURL != "" {
			fmt.Fprintf(e.out, "control: %s\n", st.ControlURL)
		}
		if st.AuthURL != "" {
			fmt.Fprintf(e.out, "login: %s\n", st.AuthURL)
		}
		if st.Error != "" {
			fmt.Fprintf(e.out, "error: %s\n", st.Error)
		}
	}
}

type checkedStringFlag struct {
	name string
	val  string
	set  bool
}

func (f *checkedStringFlag) String() string { return f.val }

func (f *checkedStringFlag) Set(v string) error {
	if strings.HasPrefix(v, "-") {
		return fmt.Errorf("missing value for --%s", f.name)
	}
	f.val = v
	f.set = true
	return nil
}

func cmdMobileTailscale(e *env, args []string) error {
	var st tailscaleStatus
	if len(args) == 0 {
		return e.do("tailscale.status", nil, &st, printTailscale(e, &st))
	}
	switch args[0] {
	case "on":
		fs := flag.NewFlagSet("mobile tailscale on", flag.ContinueOnError)
		hostname := &checkedStringFlag{name: "hostname"}
		controlURL := &checkedStringFlag{name: "control-url"}
		authKey := &checkedStringFlag{name: "auth-key"}
		fs.Var(hostname, "hostname", "")
		fs.Var(controlURL, "control-url", "")
		fs.Var(authKey, "auth-key", "")
		disableBind := fs.Bool("disable-bind-interface", false, "")
		rest, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) > 0 {
			return need(nil, 1, "mobile tailscale")
		}
		params := map[string]any{"enabled": true}
		if hostname.set {
			params["hostname"] = hostname.val
		}
		if controlURL.set {
			params["controlUrl"] = controlURL.val
		}
		if authKey.set {
			params["authKey"] = authKey.val
		}
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "disable-bind-interface" {
				params["disableBindInterface"] = *disableBind
			}
		})
		return e.do("tailscale.set", params, &st, printTailscale(e, &st))
	case "off":
		if len(args) != 1 {
			return need(nil, 1, "mobile tailscale")
		}
		return e.do("tailscale.set", map[string]any{"enabled": false}, &st, printTailscale(e, &st))
	case "login":
		if len(args) != 1 {
			return need(nil, 1, "mobile tailscale")
		}
		return e.do("tailscale.login", nil, &st, printTailscale(e, &st))
	case "reset":
		if len(args) != 1 {
			return need(nil, 1, "mobile tailscale")
		}
		var out struct {
			Status       tailscaleStatus `json:"status"`
			LoggedOut    bool            `json:"loggedOut"`
			LogoutError  string          `json:"logoutError"`
			LocalCleared bool            `json:"localCleared"`
		}
		return e.do("tailscale.reset", nil, &out, func() {
			fmt.Fprintln(e.out, "tailscale identity reset")
			if out.LogoutError != "" {
				fmt.Fprintf(e.errOut, "note: remote logout failed: %s\n", out.LogoutError)
			}
		})
	default:
		return need(nil, 1, "mobile tailscale")
	}
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
