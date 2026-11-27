package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"posse/internal/remote"
)

func TestServerManifestCLI(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "posse-server-0.9.0-darwin-arm64.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	posseBody, libBody := []byte("bin"), []byte("lib")
	pSum, lSum := sha256.Sum256(posseBody), sha256.Sum256(libBody)
	writeEntry := func(name string, mode int64, body []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	writeEntry("posse", 0o755, posseBody)
	writeEntry("libghostty-vt.dylib", 0o644, libBody)
	mf, _ := json.Marshal(map[string]any{
		"version": "0.9.0", "os": "darwin", "arch": "arm64", "edition": "headless",
		"sha256": map[string]string{
			"posse":               hex.EncodeToString(pSum[:]),
			"libghostty-vt.dylib": hex.EncodeToString(lSum[:]),
		},
	})
	writeEntry("manifest.json", 0o644, mf)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "update.key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "server-darwin-arm64.json")
	dlURL := "https://posse.shaoyan.uk/server/posse-server-0.9.0-darwin-arm64.tar.gz"
	err = run([]string{
		"--archive", archive,
		"--os", "darwin",
		"--arch", "arm64",
		"--version", "0.9.0",
		"--commit", "deadbee",
		"--url", dlURL,
		"--public-key", base64.StdEncoding.EncodeToString(pub),
		"--key-file", keyFile,
		"--out", outPath,
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var got remote.ServerManifest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := remote.VerifyServerManifest(got, "darwin", "arm64", "https://posse.shaoyan.uk/server", pub); err != nil {
		t.Fatalf("generated manifest failed verification: %v", err)
	}
	// --verify 模式应验证通过并拒绝不匹配版本与多余参数
	if err := run([]string{
		"--verify",
		"--manifest", outPath,
		"--os", "darwin",
		"--arch", "arm64",
		"--base-url", "https://posse.shaoyan.uk/server",
		"--public-key", base64.StdEncoding.EncodeToString(pub),
		"--version", "0.9.0",
	}); err != nil {
		t.Fatalf("--verify failed: %v", err)
	}
	if err := run([]string{
		"--verify",
		"--manifest", outPath,
		"--os", "darwin",
		"--arch", "arm64",
		"--base-url", "https://posse.shaoyan.uk/server",
		"--public-key", base64.StdEncoding.EncodeToString(pub),
		"unexpected-positional",
	}); err == nil {
		t.Fatal("extra positional argument was accepted")
	}
	// 公钥不匹配必须拒绝签发
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"--archive", archive,
		"--os", "darwin",
		"--arch", "arm64",
		"--version", "0.9.0",
		"--url", dlURL,
		"--public-key", base64.StdEncoding.EncodeToString(otherPub),
		"--key-file", keyFile,
		"--out", outPath,
	}); err == nil {
		t.Fatal("mismatched --public-key was accepted")
	}
}
