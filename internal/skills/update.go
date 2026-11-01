package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// 检查更新照 npx skills（NPXVersion）update 的判断做：它的 check 是 update 的别名，一跑就真的更新了，
// 没有只查不改的办法。GitHub 来源比技能目录的树哈希（每个仓库一次 Trees API），well-known 来源比
// 索引里的摘要，旧版索引没有摘要时按它的算法把文件下下来算。别的来源它也要克隆仓库才知道，这里不查。

// Updates 是一次检查的结果。
type Updates struct {
	// Latest 是 名字 → 来源上现在的版本，和 Skill.LockHash 不同就是有新版本。没查到的不在里面。
	Latest map[string]string
	// Gone 是来源里找不到的技能（上游删了、改了名或挪进了别的目录）：npx skills update 跳过它们，更新不了。
	Gone map[string]bool
	// Failed 是查不了的来源（网络不通、超出 GitHub 不登录的次数、仓库没了）。
	Failed []string
	At     time.Time
}

// HasUpdate 说技能有没有新版本：查到了，且和装上时的不同。
func (u *Updates) HasUpdate(sk Skill) bool {
	if u == nil || sk.LockHash == "" {
		return false
	}
	latest, ok := u.Latest[sk.Name]
	return ok && latest != sk.LockHash
}

// IsGone 说技能在来源里还找不找得到。
func (u *Updates) IsGone(sk Skill) bool {
	return u != nil && sk.LockHash != "" && u.Gone[sk.Name]
}

// checkTimeout 是一次检查最多等多久。
const checkTimeout = 60 * time.Second

// githubAPI 和 httpClient 在测试里换成本地服务。
var (
	githubAPI  = "https://api.github.com"
	httpClient = &http.Client{Timeout: 20 * time.Second}
)

// CheckUpdates 查 home 下 npx skills 装的技能有没有新版本。
func CheckUpdates(ctx context.Context, home string) Updates {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	u := Updates{Latest: map[string]string{}, Gone: map[string]bool{}, At: time.Now()}
	type group struct {
		names   []string
		entries []lockEntry
	}
	github, wellKnown := map[string]*group{}, map[string]*group{}
	var order []string
	for name, e := range readLock(home) {
		var m map[string]*group
		key := ""
		switch {
		case e.SourceType == "github" && e.SkillFolderHash != "" && e.SkillPath != "":
			m, key = github, e.Source+"\n"+e.Ref
		case e.SourceType == "well-known" && e.SourceBaseURL != "" && e.WellKnownDigest != "":
			m, key = wellKnown, e.SourceBaseURL
		default:
			continue
		}
		g := m[key]
		if g == nil {
			g = &group{}
			m[key] = g
			order = append(order, key)
		}
		g.names, g.entries = append(g.names, name), append(g.entries, e)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	token := githubToken()
	for _, key := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var latest map[string]string
			var gone []string
			var err error
			src := strings.SplitN(key, "\n", 2)[0]
			if g := github[key]; g != nil {
				latest, gone, err = checkGitHub(ctx, token, g.entries[0].Source, g.entries[0].Ref, g.names, g.entries)
			} else {
				g := wellKnown[key]
				want := map[string]string{}
				for _, n := range g.names {
					want[n] = filepath.Join(home, ".agents", "skills", n)
				}
				latest, err = checkWellKnown(ctx, key, want)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				u.Failed = append(u.Failed, src+"："+err.Error())
				return
			}
			for k, v := range latest {
				u.Latest[k] = v
			}
			for _, n := range gone {
				u.Gone[n] = true
			}
		}()
	}
	wg.Wait()
	slices.Sort(u.Failed)
	return u
}

// githubToken 是 GitHub 的令牌：带上后每小时能查 5000 次（不带 60 次），私有仓库也查得到。
// 先看环境变量，再问 gh（窗口进程从访达启动时 PATH 里没有它的目录）。
func githubToken() string {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		for _, p := range []string{"/opt/homebrew/bin/gh", "/usr/local/bin/gh"} {
			if _, err := os.Stat(p); err == nil {
				gh = p
				break
			}
		}
	}
	if gh == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, gh, "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkGitHub 取仓库的整棵树（没记分支时依次试 HEAD、main、master，和 npx 一样），找各技能目录的树哈希。
// 锁文件记的 SKILL.md 不在树里时，npx 会克隆仓库按名字重新找：别处有同名目录的当作挪了位置，用那个目录
// （npx 会照新路径更新）；找不到的记进 gone，npx 认为上游删了，跳过不更新。
func checkGitHub(ctx context.Context, token, repo, ref string, names []string, entries []lockEntry) (latest map[string]string, gone []string, err error) {
	refs := []string{"HEAD", "main", "master"}
	if ref != "" {
		refs = []string{ref}
	}
	var tree struct {
		SHA  string `json:"sha"`
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	for _, r := range refs {
		u := githubAPI + "/repos/" + repo + "/git/trees/" + url.PathEscape(r) + "?recursive=1"
		var b []byte
		if b, err = getURL(ctx, u, token); err == nil {
			err = json.Unmarshal(b, &tree)
		}
		// 只有分支不存在才换下一个：限流、断网时再试也是白白多用次数
		if err == nil || !errors.Is(err, errNotFound) {
			break
		}
	}
	if err != nil {
		return nil, nil, err
	}
	blobs := map[string]bool{}
	for _, t := range tree.Tree {
		if t.Type == "blob" {
			blobs[t.Path] = true
		}
	}
	dirSHA := func(dir string) string {
		if dir == "" {
			return tree.SHA
		}
		for _, t := range tree.Tree {
			if t.Type == "tree" && t.Path == dir {
				return t.SHA
			}
		}
		return ""
	}
	latest = map[string]string{}
	for i, e := range entries {
		dir := skillDir(e.SkillPath)
		file := strings.TrimPrefix(path.Join(dir, "SKILL.md"), "./")
		// 树被截断（仓库太大）时看不全，不下结论：npx 这时会克隆仓库再找，这里当没查到
		if !blobs[file] && !tree.Truncated {
			// 同名目录不止一个时 npx 不知道是哪个，跳过不更新，这里也当找不到
			var found []string
			for p := range blobs {
				if strings.EqualFold(path.Base(p), "SKILL.md") && path.Base(path.Dir(p)) == names[i] {
					found = append(found, path.Dir(p))
				}
			}
			if len(found) == 1 {
				dir = found[0]
			} else {
				gone = append(gone, names[i])
				continue
			}
		}
		if sha := dirSHA(dir); sha != "" {
			latest[names[i]] = sha
		}
	}
	return latest, gone, nil
}

// skillDir 是 SKILL.md 所在的目录，在仓库根上时为空。
func skillDir(skillPath string) string {
	p := strings.ReplaceAll(skillPath, `\`, "/")
	if strings.EqualFold(path.Base(p), "SKILL.md") {
		p = path.Dir(p)
	}
	if p == "." {
		return ""
	}
	return strings.TrimSuffix(p, "/")
}

// wellKnownPaths 是 well-known 索引所在的目录，按 npx 的顺序试。
var wellKnownPaths = []string{".well-known/agent-skills", ".well-known/skills"}

// discoveryV2 是带摘要的新版索引。
const discoveryV2 = "https://schemas.agentskills.io/discovery/0.2.0/schema.json"

// checkWellKnown 读来源的索引：新版直接给摘要；旧版只列文件，要把 dirs（名字 → 本地目录）里那几个技能的
// 文件都下下来，按 npx 的算法（文件按路径排序，依次是 路径、\0、内容、\0）算 sha256。有文件下不下来的当没查到。
// 有的文件每次下载内容不同（飞书把机房代号写进 HTML），和本地那份不同的再下几次，碰上相同的就用它，
// 免得本来没变的被报成有新版本。
func checkWellKnown(ctx context.Context, base string, dirs map[string]string) (map[string]string, error) {
	var index struct {
		Schema string `json:"$schema"`
		Skills []struct {
			Name   string   `json:"name"`
			Digest string   `json:"digest"`
			Files  []string `json:"files"`
		} `json:"skills"`
	}
	base = strings.TrimSuffix(base, "/")
	dir := ""
	var err error
	for _, p := range wellKnownPaths {
		var b []byte
		if b, err = getURL(ctx, base+"/"+p+"/index.json", ""); err == nil {
			if err = json.Unmarshal(b, &index); err == nil && index.Skills != nil {
				dir = base + "/" + p
				break
			}
			err = fmt.Errorf("索引格式不对")
		}
	}
	if dir == "" {
		return nil, err
	}
	out := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, s := range index.Skills {
		local, ok := dirs[s.Name]
		if !ok {
			continue
		}
		if index.Schema == discoveryV2 {
			out[s.Name] = s.Digest
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := sha256.New()
			for _, f := range slices.Sorted(slices.Values(s.Files)) {
				var mine []byte
				if filepath.IsLocal(filepath.FromSlash(f)) {
					mine, _ = os.ReadFile(filepath.Join(local, filepath.FromSlash(f)))
				}
				var b []byte
				for range 5 {
					var err error
					sem <- struct{}{}
					b, err = getURL(ctx, dir+"/"+s.Name+"/"+f, "")
					<-sem
					if err != nil {
						return
					}
					if mine == nil || bytes.Equal(b, mine) {
						break
					}
				}
				h.Write([]byte(f))
				h.Write([]byte{0})
				h.Write(b)
				h.Write([]byte{0})
			}
			mu.Lock()
			out[s.Name] = "sha256:" + hex.EncodeToString(h.Sum(nil))
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out, nil
}

var errNotFound = errors.New("HTTP 404")

func getURL(ctx context.Context, u, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "posse")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("超时")
		}
		return nil, fmt.Errorf("连不上")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-Ratelimit-Remaining") == "0" {
			return nil, fmt.Errorf("超出 GitHub 不登录时每小时的次数，登录 gh 后再查")
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}
