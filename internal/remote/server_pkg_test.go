package remote

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTestServerArchive(t *testing.T, dir, version, goos, goarch string) (string, string) {
	t.Helper()
	return makeTestServerArchiveBytes(t, dir, version, goos, goarch, []byte("#!/bin/sh\necho ok\n"), []byte("fake-dylib"))
}

func makeTestServerArchiveBytes(t *testing.T, dir, version, goos, goarch string, posseBody, libBody []byte) (string, string) {
	t.Helper()
	p := filepath.Join(dir, ServerArchiveName(version, goos, goarch))
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	writeFile := func(name string, mode int64, body []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	pSum := sha256.Sum256(posseBody)
	lSum := sha256.Sum256(libBody)
	writeFile("posse", 0o755, posseBody)
	writeFile("libghostty-vt.dylib", 0o644, libBody)
	manifest, _ := json.Marshal(map[string]any{
		"version": version, "os": goos, "arch": goarch, "edition": "headless",
		"sha256": map[string]string{
			"posse":               hex.EncodeToString(pSum[:]),
			"libghostty-vt.dylib": hex.EncodeToString(lSum[:]),
		},
	})
	writeFile("manifest.json", 0o644, manifest)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return p, hex.EncodeToString(sum[:])
}

func TestResolveServerPackageLocalAndDownload(t *testing.T) {
	dir := t.TempDir()
	archivePath, wantSHA := makeTestServerArchive(t, dir, "0.9.0", "darwin", "arm64")

	// 本地目录命中并返回私有快照（即使源文件随后被删或篡改，快照内容不受影响）
	pkg, err := ResolveServerPackage(context.Background(), ServerResolveOptions{
		TargetOS:      "Darwin",
		TargetArch:    "arm64",
		Version:       "0.9.0",
		SearchDirs:    []string{dir},
		DisableRemote: true,
	})
	if err != nil {
		t.Fatalf("local resolve failed: %v", err)
	}
	defer pkg.Close()
	if pkg.SHA256 != wantSHA || pkg.Version != "0.9.0" || pkg.PosseSHA256 == "" || pkg.LibSHA256 == "" {
		t.Fatalf("pkg = %+v, want sha=%s", pkg, wantSHA)
	}
	if pkg.Path == archivePath {
		t.Fatalf("expected private snapshot path, got original %s", archivePath)
	}

	// 线上下载 + Ed25519 验签 + 内容寻址对象缓存
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rawArchive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/server-darwin-arm64.json":
			m := ServerManifest{
				Version: "0.9.0", OS: "darwin", Arch: "arm64",
				URL:    srv.URL + "/bundle.tar.gz",
				SHA256: wantSHA,
			}
			m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, serverManifestSignPayload(m)))
			_ = json.NewEncoder(w).Encode(m)
		case "/bundle.tar.gz":
			_, _ = w.Write(rawArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	dlPkg, err := ResolveServerPackage(context.Background(), ServerResolveOptions{
		TargetOS:   "darwin",
		TargetArch: "arm64",
		Version:    "0.9.0",
		LocalHome:  home,
		BaseURL:    srv.URL,
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("download resolve failed: %v", err)
	}
	defer dlPkg.Close()
	if dlPkg.SHA256 != wantSHA {
		t.Fatalf("downloaded pkg = %+v", dlPkg)
	}
	cachedObj := filepath.Join(home, "server-cache", "objects", wantSHA+".tar.gz")
	if _, err := os.Stat(cachedObj); err != nil {
		t.Fatalf("expected content-addressed cache object %s: %v", cachedObj, err)
	}
}

func TestInspectServerArchiveRejectsTruncatedGzipAndExtraEntries(t *testing.T) {
	dir := t.TempDir()
	archivePath, _ := makeTestServerArchive(t, dir, "0.9.0", "darwin", "arm64")
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	// 截断 gzip 尾部（去掉最后 8 字节 CRC32+ISIZE），必须拒绝
	truncated := filepath.Join(dir, "truncated.tar.gz")
	if err := os.WriteFile(truncated, raw[:len(raw)-8], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectAndSnapshot(context.Background(), truncated, "0.9.0", "darwin", "arm64", ""); err == nil {
		t.Fatal("truncated gzip archive was accepted")
	}

	// 追加尾随垃圾字节，必须拒绝
	trailing := filepath.Join(dir, "trailing.tar.gz")
	if err := os.WriteFile(trailing, append(append([]byte{}, raw...), "junk"...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectAndSnapshot(context.Background(), trailing, "0.9.0", "darwin", "arm64", ""); err == nil {
		t.Fatal("gzip archive with trailing data was accepted")
	}

	// 含未预期额外文件或符号链接的 tar，必须拒绝
	extra := filepath.Join(dir, "extra.tar.gz")
	f, err := os.Create(extra)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, item := range []struct {
		name string
		body []byte
	}{
		{"posse", []byte("bin")},
		{"libghostty-vt.dylib", []byte("lib")},
		{"evil.sh", []byte("rm -rf /")},
	} {
		_ = tw.WriteHeader(&tar.Header{Name: item.name, Mode: 0o755, Size: int64(len(item.body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(item.body)
	}
	_ = tw.Close()
	_ = gw.Close()
	_ = f.Close()
	if _, err := inspectAndSnapshot(context.Background(), extra, "0.9.0", "darwin", "arm64", ""); err == nil {
		t.Fatal("archive with unexpected extra file was accepted")
	}
}

func TestSignAndVerifyServerManifest(t *testing.T) {
	dir := t.TempDir()
	archivePath, wantSHA := makeTestServerArchive(t, dir, "0.9.0", "darwin", "arm64")
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dlURL := "https://posse.shaoyan.uk/server/" + ServerArchiveName("0.9.0", "darwin", "arm64")
	m, err := SignServerManifest(context.Background(), archivePath, "darwin", "arm64", "0.9.0", "abc1234", dlURL, priv)
	if err != nil {
		t.Fatalf("SignServerManifest failed: %v", err)
	}
	if m.SHA256 != wantSHA || m.Version != "0.9.0" || m.Commit != "abc1234" {
		t.Fatalf("manifest = %+v, want sha=%s", m, wantSHA)
	}
	if err := VerifyServerManifest(m, "darwin", "arm64", defaultServerBaseURL, pub); err != nil {
		t.Fatalf("VerifyServerManifest failed: %v", err)
	}
	// 版本不匹配必须被拒签
	if _, err := SignServerManifest(context.Background(), archivePath, "darwin", "arm64", "9.9.9", "", dlURL, priv); err == nil {
		t.Fatal("SignServerManifest accepted mismatched archive version")
	}
	// 篡改 SHA256 或 Commit 后验签必须失败
	tampered := m
	tampered.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := VerifyServerManifest(tampered, "darwin", "arm64", defaultServerBaseURL, pub); err == nil {
		t.Fatal("VerifyServerManifest accepted tampered SHA256")
	}
	tamperedCommit := m
	tamperedCommit.Commit = "fffffff"
	if err := VerifyServerManifest(tamperedCommit, "darwin", "arm64", defaultServerBaseURL, pub); err == nil {
		t.Fatal("VerifyServerManifest accepted tampered commit")
	}
	// 非法版本号（点开头或超长）必须被拒签和拒验
	for _, badVer := range []string{"", ".1.0", "1.0/evil", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if _, err := SignServerManifest(context.Background(), archivePath, "darwin", "arm64", badVer, "", dlURL, priv); err == nil {
			t.Fatalf("SignServerManifest accepted invalid version %q", badVer)
		}
	}
	// 含尾随垃圾的清单 JSON 必须拒绝
	if _, err := DecodeServerManifest(strings.NewReader(`{"version":"1.0.0"}{"extra":true}`)); err == nil {
		t.Fatal("DecodeServerManifest accepted trailing JSON value")
	}
	// 错误公钥必须验签失败
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyServerManifest(m, "darwin", "arm64", defaultServerBaseURL, otherPub); err == nil {
		t.Fatal("VerifyServerManifest accepted wrong public key")
	}
	// 私钥/公钥解析往返一致
	parsedPriv, err := ParseUpdatePrivateKey(base64.StdEncoding.EncodeToString(priv))
	if err != nil || !parsedPriv.Equal(priv) {
		t.Fatalf("ParseUpdatePrivateKey roundtrip failed: %v", err)
	}
	parsedPub, err := ParseUpdatePublicKey(base64.StdEncoding.EncodeToString(pub))
	if err != nil || !parsedPub.Equal(pub) {
		t.Fatalf("ParseUpdatePublicKey roundtrip failed: %v", err)
	}
}

func TestDefaultServerConfigMatchesMygoJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "mygo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Updates struct {
			PublicKey string `json:"publicKey"`
			URL       string `json:"url"`
		} `json:"updates"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if defaultServerPublicKey != cfg.Updates.PublicKey {
		t.Fatalf("defaultServerPublicKey = %q, mygo.json updates.publicKey = %q", defaultServerPublicKey, cfg.Updates.PublicKey)
	}
	wantURL := cfg.Updates.URL + "/server"
	if defaultServerBaseURL != wantURL {
		t.Fatalf("defaultServerBaseURL = %q, want %q", defaultServerBaseURL, wantURL)
	}
}
