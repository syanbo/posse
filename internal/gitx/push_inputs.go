package gitx

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

type pushInputs struct {
	refs, config, atom []byte
	preview            PushPreviewRead
	excludes           []string
}

var pushTrackPattern = regexp.MustCompile(`^\[(?:ahead [1-9][0-9]*(?:, behind [1-9][0-9]*)?|behind [1-9][0-9]*)\]$`)

func pushConfig(ctx context.Context, dir string) ([]byte, map[string][]string, error) {
	raw, err := historyRead(ctx, dir, nil, "config", "--null", "--get-regexp", `^(branch\..*\.(remote|pushremote|merge)|remote\..*|push\..*|url\..*\.(insteadof|pushinsteadof))$`)
	if err != nil {
		if pushNoMatch(err) {
			raw = nil
		} else {
			return nil, nil, pushConfigError(ctx, err)
		}
	}
	values := map[string][]string{}
	if len(raw) == 0 {
		return raw, values, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, nil, ErrPushOutput
	}
	for _, record := range bytes.Split(raw[:len(raw)-1], []byte{0}) {
		key, value, ok := strings.Cut(string(record), "\n")
		if !ok || key == "" {
			return nil, nil, ErrPushOutput
		}
		values[key] = append(values[key], value)
	}
	return raw, values, nil
}
func pushNoMatch(err error) bool {
	var exit *exec.ExitError
	var output *Error
	return errors.As(err, &exit) && exit.ExitCode() == 1 && errors.As(err, &output) && output.Msg == exit.Error()
}
func pushConfigError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrReadLimit) {
		return err
	}
	// Git 的配置错误可能引用包含凭据的 URL/refspec，目标只给出不含原值的错误。
	return ErrPushConfig
}
func pushLast(values map[string][]string, key string) (string, bool) {
	v := values[key]
	if len(v) == 0 {
		return "", false
	}
	return v[len(v)-1], true
}

func pushRemoteConfig(values map[string][]string, remote, branch string) error {
	prefix := "remote." + remote + "."
	urls := values[prefix+"pushurl"]
	if len(urls) == 0 {
		urls = values[prefix+"url"]
	}
	if len(urls) != 1 || urls[0] == "" {
		return ErrPushConfig
	}
	if mirror, ok := pushLast(values, prefix+"mirror"); ok && mirror != "false" && mirror != "no" && mirror != "0" {
		return ErrPushConfig
	}
	fetch := values[prefix+"fetch"]
	if len(fetch) > 1 {
		return ErrPushConfig
	}
	if len(fetch) == 1 && strings.TrimPrefix(fetch[0], "+") != "refs/heads/*:refs/remotes/"+remote+"/*" {
		return ErrPushConfig
	}
	specs := values[prefix+"push"]
	if len(specs) > 1 {
		return ErrPushConfig
	}
	if len(specs) == 1 {
		source, dest, ok := strings.Cut(strings.TrimPrefix(specs[0], "+"), ":")
		if !ok || source != "refs/heads/"+branch || !strings.HasPrefix(dest, "refs/heads/") || !ValidPushBranch(strings.TrimPrefix(dest, "refs/heads/")) {
			return ErrPushConfig
		}
	}
	return nil
}

func readPushInputs(ctx context.Context, dir, branch string) (pushInputs, error) {
	var state pushInputs
	var err error
	state.refs, _, err = historyRefs(ctx, dir)
	if err != nil {
		return state, err
	}
	var values map[string][]string
	state.config, values, err = pushConfig(ctx, dir)
	if err != nil {
		return state, err
	}
	remotes, err := historyRead(ctx, dir, nil, "remote")
	if err != nil {
		return state, pushConfigError(ctx, err)
	}
	names := []string{}
	known := map[string]bool{}
	if len(remotes) > 0 {
		if remotes[len(remotes)-1] != '\n' {
			return state, ErrPushOutput
		}
		for _, name := range strings.Split(string(remotes[:len(remotes)-1]), "\n") {
			if !ValidPushRemote(name) || known[name] {
				return state, ErrPushConfig
			}
			known[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return state, errors.New("this repository has no remote")
	}
	for _, key := range []string{"branch." + branch + ".pushremote", "remote.pushdefault", "branch." + branch + ".remote"} {
		if name, ok := pushLast(values, key); ok && (!known[name] || !ValidPushRemote(name)) {
			return state, ErrPushConfig
		}
	}
	if mode, ok := pushLast(values, "push.default"); ok {
		switch mode {
		case "simple", "current", "upstream", "tracking", "matching", "nothing":
		default:
			return state, ErrPushConfig
		}
	}
	format := "--format=%(refname)%00%(objectname)%00%(objecttype)%00%(push:remotename)%00%(push:remoteref)%00%(upstream:remotename)%00%(upstream:remoteref)%00%(upstream:track)%00"
	state.atom, err = historyRead(ctx, dir, nil, "for-each-ref", format, "refs/heads/"+branch)
	if err != nil {
		return state, pushConfigError(ctx, err)
	}
	if len(state.atom) == 0 {
		return state, ErrPushQuery
	}
	if !bytes.HasSuffix(state.atom, []byte{0, '\n'}) {
		return state, ErrPushOutput
	}
	f := strings.Split(string(state.atom[:len(state.atom)-2]), "\x00")
	if len(f) != 8 || f[0] != "refs/heads/"+branch || !ValidBlobOID(f[1]) || strings.Trim(f[1], "0") == "" || f[2] != "commit" {
		return state, ErrPushOutput
	}
	pushRemote, pushRef, upRemote, upRef, track := f[3], f[4], f[5], f[6], f[7]
	if pushRemote != "" && !known[pushRemote] || upRemote != "" && !known[upRemote] || upRemote == "" && upRef != "" {
		return state, ErrPushConfig
	}
	for _, ref := range []string{pushRef, upRef} {
		if ref != "" && (!strings.HasPrefix(ref, "refs/heads/") || !ValidPushBranch(strings.TrimPrefix(ref, "refs/heads/"))) {
			return state, ErrPushConfig
		}
	}
	if track != "" && track != "[gone]" && !pushTrackPattern.MatchString(track) {
		return state, ErrPushOutput
	}
	p := PushPreviewRead{Plan: PushPlan{Branch: branch, Remote: pushRemote, RemoteBranch: branch, Gone: track == "[gone]", SetUpstream: upRef == "" || track == "[gone]", Commits: []CommitInfo{}}, BranchOID: f[1]}
	switch {
	case pushRef != "":
		p.Plan.RemoteBranch = strings.TrimPrefix(pushRef, "refs/heads/")
	case pushRemote == upRemote && upRef != "":
		p.Plan.RemoteBranch = strings.TrimPrefix(upRef, "refs/heads/")
	}
	if p.Plan.Remote == "" {
		p.Plan.Remote = names[0]
		if known["origin"] {
			p.Plan.Remote = "origin"
		}
	}
	if err := pushRemoteConfig(values, p.Plan.Remote, branch); err != nil {
		return state, err
	}
	p.TrackingRef = "refs/remotes/" + p.Plan.Remote + "/" + p.Plan.RemoteBranch
	// 只把 show-ref 明确的不存在当作新分支；损坏对象、权限与取消不降级。
	_, err = historyRead(ctx, dir, nil, "show-ref", "--verify", "--quiet", p.TrackingRef)
	if err != nil {
		if !pushNoMatch(err) {
			return state, err
		}
		p.TrackingMissing = true
		p.Plan.NewBranch = true
	} else {
		p.TrackingOID, err = compareRevision(ctx, dir, p.TrackingRef)
		if err != nil {
			return state, err
		}
		if len(p.TrackingOID) != len(p.BranchOID) {
			return state, ErrPushOutput
		}
	}
	if p.TrackingMissing {
		raw, err := historyRead(ctx, dir, nil, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(objecttype)", "refs/remotes/")
		if err != nil {
			return state, err
		}
		seen := map[string]bool{}
		for _, line := range bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			fields := strings.Split(string(line), "\x00")
			if len(fields) != 3 || !strings.HasPrefix(fields[0], "refs/remotes/") || !ValidReadRevision(fields[0]) || !ValidBlobOID(fields[1]) {
				return state, ErrPushOutput
			}
			oid := fields[1]
			if fields[2] != "commit" {
				oid, err = compareRevision(ctx, dir, fields[0])
				if err != nil {
					return state, err
				}
			}
			if len(oid) != len(p.BranchOID) {
				return state, ErrPushOutput
			}
			if !seen[oid] {
				state.excludes = append(state.excludes, oid)
				seen[oid] = true
			}
		}
	}
	state.preview = p
	return state, nil
}
