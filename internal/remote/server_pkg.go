package remote

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"posse/internal/daemon"
)

const (
	defaultServerBaseURL       = "https://posse.shaoyan.uk/server"
	defaultServerPublicKey     = "XyNlKbJMuyckf118edk00zLSKEVR3mJWJAXEHNa34p8="
	maxServerArchiveBytes      = 128 << 20
	maxServerUncompressedBytes = 256 << 20
	maxServerManifestBytes     = 64 << 10
	maxServerArchiveEntries    = 4
)

// ServerPackage 描述一份已完整验签/校验的私有只读快照，供后续 SSH 推送使用，
// 保证实际上传的字节与校验通过的字节完全一致。调用方用完后需调 Close 清理临时快照。
type ServerPackage struct {
	Path        string
	Version     string
	Commit      string
	BuiltAt     time.Time
	Tag         string
	OS          string
	Arch        string
	SHA256      string
	LibName     string
	PosseSHA256 string
	LibSHA256   string
	snapshot    string
}

// Close 清理为本次传输创建的私有临时快照文件。
func (p *ServerPackage) Close() {
	if p != nil && p.snapshot != "" {
		_ = os.Remove(p.snapshot)
		p.snapshot = ""
	}
}

// ServerResolveOptions 控制如何获取目标平台对应的无头服务端压缩包。
type ServerResolveOptions struct {
	TargetOS      string
	TargetArch    string
	Version       string
	LocalHome     string
	ExplicitFile  string
	SearchDirs    []string
	BaseURL       string
	PublicKey     string
	HTTPClient    *http.Client
	DisableRemote bool
}

// NormalizeArch 把 uname -m 规范为 Go 架构名。
func NormalizeArch(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "arm64", "aarch64":
		return "arm64", nil
	case "x86_64", "amd64":
		return "amd64", nil
	default:
		return "", fmt.Errorf("unsupported remote architecture %q", raw)
	}
}

// NormalizeOS 把 uname -s 规范为 Go 平台名。
func NormalizeOS(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "darwin":
		return "darwin", nil
	case "linux":
		return "linux", nil
	default:
		return "", fmt.Errorf("unsupported remote platform %q", raw)
	}
}

// ServerArchiveName 返回标准无头服务端压缩包文件名。
func ServerArchiveName(version, goos, goarch string) string {
	return fmt.Sprintf("posse-server-%s-%s-%s.tar.gz", version, goos, goarch)
}

// ResolveServerPackage 按优先级定位、校验（必要时下载并验签）目标平台的无头服务端压缩包，
// 并返回已核验的私有快照，保证上传字节与校验字节一致。
func ResolveServerPackage(ctx context.Context, opts ServerResolveOptions) (ServerPackage, error) {
	if err := ctx.Err(); err != nil {
		return ServerPackage{}, err
	}
	goos, err := NormalizeOS(opts.TargetOS)
	if err != nil {
		return ServerPackage{}, err
	}
	goarch, err := NormalizeArch(opts.TargetArch)
	if err != nil {
		return ServerPackage{}, err
	}
	if goos != "darwin" {
		return ServerPackage{}, fmt.Errorf("automatic server install is currently supported for macOS targets only (got %s/%s)", goos, goarch)
	}
	ver := opts.Version
	if ver == "" {
		ver = daemon.Version
	}
	if opts.ExplicitFile != "" {
		return inspectAndSnapshot(ctx, opts.ExplicitFile, ver, goos, goarch, "")
	}

	name := ServerArchiveName(ver, goos, goarch)
	for _, dir := range serverSearchDirs(opts) {
		cand := filepath.Join(dir, name)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return inspectAndSnapshot(ctx, cand, ver, goos, goarch, "")
		}
	}

	if opts.DisableRemote {
		return ServerPackage{}, fmt.Errorf("headless server bundle %s not found locally; run scripts/build-server.sh or pass --file", name)
	}
	return downloadServerPackage(ctx, opts, ver, goos, goarch)
}

// serverSearchDirs 仅包含显式受信的本地包目录，不自动信任任意当前工作目录的 build/server。
func serverSearchDirs(opts ServerResolveOptions) []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(d string) {
		if d == "" {
			return
		}
		d = filepath.Clean(d)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	if envDir := os.Getenv("POSSE_SERVER_DIR"); envDir != "" {
		add(envDir)
	}
	for _, d := range opts.SearchDirs {
		add(d)
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		exeDir := filepath.Dir(exe)
		// 打包版 Posse.app/Contents/MacOS/Posse -> Contents/Resources/server
		add(filepath.Join(exeDir, "..", "Resources", "server"))
		// 仓库内 bin/posse -> ../build/server
		if filepath.Base(exeDir) == "bin" {
			add(filepath.Join(exeDir, "..", "build", "server"))
		}
		// 仓库内 build/(darwin-arm64|darwin-amd64|dev)/*.app -> build/server
		contentsDir := filepath.Dir(exeDir)
		appDir := filepath.Dir(contentsDir)
		platDir := filepath.Dir(appDir)
		buildDir := filepath.Dir(platDir)
		if filepath.Base(exeDir) == "MacOS" && filepath.Base(contentsDir) == "Contents" && strings.HasSuffix(appDir, ".app") && filepath.Base(buildDir) == "build" {
			switch filepath.Base(platDir) {
			case "darwin-arm64", "darwin-amd64", "dev":
				add(filepath.Join(buildDir, "server"))
			}
		}
	}
	return dirs
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// inspectAndSnapshot 先把源文件复制到私有 0600 临时快照并计算全文件 SHA-256，
// 再从同一份快照完整消费 gzip（校验 CRC32/ISIZE 尾与无截断/尾随数据）及严格 tar 结构。
func inspectAndSnapshot(ctx context.Context, archivePath, fallbackVer, goos, goarch, wantSHA string) (ServerPackage, error) {
	src, err := os.Open(archivePath)
	if err != nil {
		return ServerPackage{}, err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return ServerPackage{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxServerArchiveBytes {
		return ServerPackage{}, fmt.Errorf("invalid server archive size %d", info.Size())
	}

	snap, err := os.CreateTemp("", "posse-server-snap-*.tar.gz")
	if err != nil {
		return ServerPackage{}, err
	}
	snapPath := snap.Name()
	keepSnap := false
	defer func() {
		snap.Close()
		if !keepSnap {
			_ = os.Remove(snapPath)
		}
	}()
	if err := snap.Chmod(0o600); err != nil {
		return ServerPackage{}, err
	}

	fileHash := sha256.New()
	written, err := io.Copy(io.MultiWriter(snap, fileHash), io.LimitReader(ctxReader{ctx: ctx, r: src}, maxServerArchiveBytes+1))
	if err != nil {
		return ServerPackage{}, err
	}
	if written <= 0 || written > maxServerArchiveBytes {
		return ServerPackage{}, fmt.Errorf("invalid server archive size %d", written)
	}
	sum := hex.EncodeToString(fileHash.Sum(nil))
	if wantSHA != "" && !strings.EqualFold(sum, wantSHA) {
		return ServerPackage{}, errors.New("downloaded server bundle checksum mismatch")
	}
	if _, err := snap.Seek(0, io.SeekStart); err != nil {
		return ServerPackage{}, err
	}

	pkg, err := validateSnapshotStream(ctx, snap, snapPath, sum, fallbackVer, goos, goarch)
	if err != nil {
		return ServerPackage{}, err
	}
	if err := snap.Close(); err != nil {
		return ServerPackage{}, err
	}
	keepSnap = true
	return pkg, nil
}

func validateSnapshotStream(ctx context.Context, snap *os.File, snapPath, sum, fallbackVer, goos, goarch string) (ServerPackage, error) {
	br := bufio.NewReader(ctxReader{ctx: ctx, r: snap})
	gr, err := gzip.NewReader(br)
	if err != nil {
		return ServerPackage{}, fmt.Errorf("invalid server archive gzip: %w", err)
	}
	// 关闭多流合并，防止拼接多个 gzip 流隐藏额外数据
	gr.Multistream(false)

	needLib := "libghostty-vt.dylib"
	if goos == "linux" {
		needLib = "libghostty-vt.so"
	}
	seen := map[string]bool{}
	var posseSHA, libSHA string
	var totalUncompressed int64
	entries := 0
	ver := fallbackVer
	commit := ""
	var builtAt time.Time
	var manifestSHAs map[string]string
	hasManifest := false

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = gr.Close()
			return ServerPackage{}, fmt.Errorf("invalid server archive tar: %w", err)
		}
		if err := ctx.Err(); err != nil {
			_ = gr.Close()
			return ServerPackage{}, err
		}
		entries++
		if entries > maxServerArchiveEntries {
			_ = gr.Close()
			return ServerPackage{}, errors.New("server archive contains too many entries")
		}
		if hdr.Typeflag != tar.TypeReg {
			_ = gr.Close()
			return ServerPackage{}, fmt.Errorf("unexpected non-regular entry %q in server archive", hdr.Name)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		clean := path.Clean(name)
		if name != clean || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/") {
			_ = gr.Close()
			return ServerPackage{}, fmt.Errorf("unsafe path %q in server archive", hdr.Name)
		}
		if seen[clean] {
			_ = gr.Close()
			return ServerPackage{}, fmt.Errorf("duplicate entry %q in server archive", clean)
		}
		seen[clean] = true
		if hdr.Size < 0 || hdr.Size > maxServerUncompressedBytes || totalUncompressed+hdr.Size > maxServerUncompressedBytes {
			_ = gr.Close()
			return ServerPackage{}, errors.New("server archive exceeds uncompressed size limit")
		}

		switch clean {
		case "posse":
			if hdr.Size == 0 {
				_ = gr.Close()
				return ServerPackage{}, errors.New("server archive posse executable is empty")
			}
			h := sha256.New()
			n, rerr := io.Copy(h, io.LimitReader(tr, maxServerUncompressedBytes-totalUncompressed+1))
			if rerr != nil {
				_ = gr.Close()
				return ServerPackage{}, fmt.Errorf("invalid server archive tar: %w", rerr)
			}
			totalUncompressed += n
			if totalUncompressed > maxServerUncompressedBytes {
				_ = gr.Close()
				return ServerPackage{}, errors.New("server archive exceeds uncompressed size limit")
			}
			posseSHA = hex.EncodeToString(h.Sum(nil))
		case needLib:
			if hdr.Size == 0 {
				_ = gr.Close()
				return ServerPackage{}, fmt.Errorf("server archive %s is empty", needLib)
			}
			h := sha256.New()
			n, rerr := io.Copy(h, io.LimitReader(tr, maxServerUncompressedBytes-totalUncompressed+1))
			if rerr != nil {
				_ = gr.Close()
				return ServerPackage{}, fmt.Errorf("invalid server archive tar: %w", rerr)
			}
			totalUncompressed += n
			if totalUncompressed > maxServerUncompressedBytes {
				_ = gr.Close()
				return ServerPackage{}, errors.New("server archive exceeds uncompressed size limit")
			}
			libSHA = hex.EncodeToString(h.Sum(nil))
		case "manifest.json":
			if hdr.Size > maxServerManifestBytes {
				_ = gr.Close()
				return ServerPackage{}, errors.New("server archive manifest is too large")
			}
			raw, rerr := io.ReadAll(io.LimitReader(tr, maxServerManifestBytes+1))
			if rerr != nil {
				_ = gr.Close()
				return ServerPackage{}, fmt.Errorf("invalid server archive tar: %w", rerr)
			}
			totalUncompressed += int64(len(raw))
			var m struct {
				Version string            `json:"version"`
				Commit  string            `json:"commit"`
				BuiltAt string            `json:"builtAt"`
				OS      string            `json:"os"`
				Arch    string            `json:"arch"`
				Edition string            `json:"edition"`
				SHA256  map[string]string `json:"sha256"`
			}
			if uerr := json.Unmarshal(raw, &m); uerr != nil {
				_ = gr.Close()
				return ServerPackage{}, fmt.Errorf("invalid server archive manifest: %w", uerr)
			}
			if m.OS != goos {
				_ = gr.Close()
				return ServerPackage{}, fmt.Errorf("server archive is for %s, want %s", m.OS, goos)
			}
			if m.Arch != goarch {
				_ = gr.Close()
				return ServerPackage{}, fmt.Errorf("server archive is for %s, want %s", m.Arch, goarch)
			}
			if m.Edition != "headless" {
				_ = gr.Close()
				return ServerPackage{}, fmt.Errorf("server archive edition %q is not headless", m.Edition)
			}
			if !ValidServerVersion(m.Version) {
				_ = gr.Close()
				return ServerPackage{}, fmt.Errorf("invalid server version %q", m.Version)
			}
			ver = m.Version
			commit = strings.TrimSpace(m.Commit)
			if ts := strings.TrimSpace(m.BuiltAt); ts != "" {
				if parsed, perr := time.Parse(time.RFC3339, ts); perr == nil {
					builtAt = parsed
				}
			}
			manifestSHAs = m.SHA256
			hasManifest = true
		default:
			_ = gr.Close()
			return ServerPackage{}, fmt.Errorf("unexpected file %q in server archive", clean)
		}
	}

	// 必须将 gzip.Reader 完整读到 EOF 并关闭，强制触发 CRC32 与 ISIZE 尾部校验（拦截截断文件）
	if _, err := io.Copy(io.Discard, io.LimitReader(gr, 1<<20)); err != nil {
		_ = gr.Close()
		return ServerPackage{}, fmt.Errorf("invalid server archive gzip: %w", err)
	}
	if err := gr.Close(); err != nil {
		return ServerPackage{}, fmt.Errorf("invalid server archive gzip: %w", err)
	}
	// 确认底层文件没有多出来的尾随垃圾或第二段未校验流（需读同一个 bufio.Reader 以覆盖预读缓冲区）
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		return ServerPackage{}, errors.New("invalid server archive gzip: unexpected trailing data")
	}

	if posseSHA == "" || libSHA == "" {
		return ServerPackage{}, fmt.Errorf("server archive missing required files (posse=%v, %s=%v)", posseSHA != "", needLib, libSHA != "")
	}
	if !hasManifest || manifestSHAs == nil {
		return ServerPackage{}, errors.New("server archive missing manifest.json")
	}
	if want := manifestSHAs["posse"]; want == "" || !strings.EqualFold(want, posseSHA) {
		return ServerPackage{}, errors.New("server archive manifest posse checksum mismatch")
	}
	if want := manifestSHAs[needLib]; want == "" || !strings.EqualFold(want, libSHA) {
		return ServerPackage{}, fmt.Errorf("server archive manifest %s checksum mismatch", needLib)
	}
	if !ValidServerVersion(ver) {
		return ServerPackage{}, fmt.Errorf("invalid server version %q", ver)
	}
	tag := ver + "-" + sum[:12]
	return ServerPackage{
		Path:        snapPath,
		Version:     ver,
		Commit:      commit,
		BuiltAt:     builtAt,
		Tag:         tag,
		OS:          goos,
		Arch:        goarch,
		SHA256:      sum,
		LibName:     needLib,
		PosseSHA256: posseSHA,
		LibSHA256:   libSHA,
		snapshot:    snapPath,
	}, nil
}

func sanitizeTag(v string) string {
	var b strings.Builder
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "0"
	}
	return b.String()
}

func validDownloadURL(rawURL, baseURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("invalid server download URL")
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || base.User != nil {
		return errors.New("invalid server base URL")
	}
	if u.Scheme == "https" {
		if !strings.EqualFold(u.Host, base.Host) {
			return fmt.Errorf("server download host %q does not match manifest host %q", u.Host, base.Host)
		}
		return nil
	}
	// 仅允许本地回环测试使用 http
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
	}
	if u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1") && strings.EqualFold(u.Host, base.Host) {
		return nil
	}
	return fmt.Errorf("unsupported server download URL scheme %q", u.Scheme)
}

func downloadServerPackage(ctx context.Context, opts ServerResolveOptions, _ string, goos, goarch string) (ServerPackage, error) {
	baseURL := strings.TrimRight(opts.BaseURL, "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(os.Getenv("POSSE_SERVER_URL"), "/")
	}
	if baseURL == "" {
		baseURL = defaultServerBaseURL
	}
	if err := validDownloadURL(baseURL, baseURL); err != nil {
		return ServerPackage{}, err
	}
	pubB64 := opts.PublicKey
	if pubB64 == "" {
		pubB64 = defaultServerPublicKey
	}
	pubKey, err := ParseUpdatePublicKey(pubB64)
	if err != nil {
		return ServerPackage{}, err
	}
	baseClient := opts.HTTPClient
	if baseClient == nil {
		baseClient = &http.Client{Timeout: 2 * time.Minute}
	}
	client := *baseClient
	origCheckRedirect := baseClient.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if err := validDownloadURL(req.URL.String(), baseURL); err != nil {
			return err
		}
		if origCheckRedirect != nil {
			return origCheckRedirect(req, via)
		}
		return nil
	}

	manifestURL := fmt.Sprintf("%s/server-%s-%s.json", baseURL, goos, goarch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return ServerPackage{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ServerPackage{}, fmt.Errorf("download server manifest failed (build locally with scripts/build-server.sh or check network): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ServerPackage{}, fmt.Errorf("server manifest unavailable (HTTP %d); build locally with scripts/build-server.sh or pass --file", resp.StatusCode)
	}
	m, err := DecodeServerManifest(resp.Body)
	if err != nil {
		return ServerPackage{}, err
	}
	if err := VerifyServerManifest(m, goos, goarch, baseURL, pubKey); err != nil {
		return ServerPackage{}, err
	}

	// 按完整 SHA-256 存为不可变对象，每次读取都重新核对签名、全文件摘要与内外版本一致
	objDir := filepath.Join(os.TempDir(), "posse-server-cache", "objects")
	if opts.LocalHome != "" {
		objDir = filepath.Join(opts.LocalHome, "server-cache", "objects")
	}
	if err := os.MkdirAll(objDir, 0o700); err != nil {
		return ServerPackage{}, err
	}
	objPath := filepath.Join(objDir, strings.ToLower(m.SHA256)+".tar.gz")
	if pkg, err := inspectAndSnapshot(ctx, objPath, m.Version, goos, goarch, m.SHA256); err == nil {
		if pkg.Version == m.Version {
			return pkg, nil
		}
		pkg.Close()
	}

	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return ServerPackage{}, err
	}
	dlResp, err := client.Do(dlReq)
	if err != nil {
		return ServerPackage{}, fmt.Errorf("download server bundle failed: %w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK {
		return ServerPackage{}, fmt.Errorf("download server bundle failed (HTTP %d)", dlResp.StatusCode)
	}
	tmp, err := os.CreateTemp(objDir, ".dl-*.tar.gz")
	if err != nil {
		return ServerPackage{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(ctxReader{ctx: ctx, r: dlResp.Body}, maxServerArchiveBytes+1)); err != nil {
		tmp.Close()
		return ServerPackage{}, err
	}
	if err := tmp.Close(); err != nil {
		return ServerPackage{}, err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), m.SHA256) {
		return ServerPackage{}, errors.New("downloaded server bundle checksum mismatch")
	}
	pkg, err := inspectAndSnapshot(ctx, tmpPath, m.Version, goos, goarch, m.SHA256)
	if err != nil {
		return ServerPackage{}, err
	}
	if pkg.Version != m.Version {
		pkg.Close()
		return ServerPackage{}, fmt.Errorf("downloaded server archive version %q does not match signed manifest %q", pkg.Version, m.Version)
	}
	// 内容寻址对象不可变：原子移入缓存后仍由 pkg 私有快照上传，避免缓存变动影响本次推送
	if err := os.Rename(tmpPath, objPath); err != nil {
		pkg.Close()
		return ServerPackage{}, err
	}
	return pkg, nil
}

func serverManifestSignPayload(m ServerManifest) []byte {
	return []byte(fmt.Sprintf("posse-server-v1\n%s\n%s\n%s\n%s\n%s\n%s\n", m.Version, m.Commit, m.OS, m.Arch, strings.ToLower(m.SHA256), m.URL))
}
