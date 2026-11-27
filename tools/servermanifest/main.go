// Command servermanifest 校验无头服务端压缩包并签发/验签 server-<os>-<arch>.json。
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"posse/internal/remote"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "servermanifest: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("servermanifest", flag.ContinueOnError)
	verify := fs.Bool("verify", false, "verify an existing server manifest JSON")
	manifestPath := fs.String("manifest", "", "path to manifest JSON (for --verify)")
	baseURL := fs.String("base-url", "", "expected manifest base URL (for --verify)")
	archive := fs.String("archive", "", "path to posse-server tar.gz archive")
	goos := fs.String("os", "darwin", "target OS")
	goarch := fs.String("arch", "", "target architecture (arm64|amd64)")
	version := fs.String("version", "", "expected release version")
	commit := fs.String("commit", "", "git commit short hash")
	dlURL := fs.String("url", "", "public download URL of the archive")
	pubKey := fs.String("public-key", "", "expected base64 Ed25519 public key")
	keyFile := fs.String("key-file", "", "path to base64 Ed25519 private key (defaults to MYGO_UPDATER_PRIVATE_KEY env)")
	outPath := fs.String("out", "", "output manifest JSON path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}

	if *verify {
		if *manifestPath == "" || *goarch == "" || *baseURL == "" || strings.TrimSpace(*pubKey) == "" {
			return errors.New("required flags for --verify: --manifest, --arch, --base-url, --public-key")
		}
		pub, err := remote.ParseUpdatePublicKey(*pubKey)
		if err != nil {
			return err
		}
		f, err := os.Open(*manifestPath)
		if err != nil {
			return err
		}
		defer f.Close()
		m, err := remote.DecodeServerManifest(f)
		if err != nil {
			return err
		}
		if err := remote.VerifyServerManifest(m, *goos, *goarch, *baseURL, pub); err != nil {
			return err
		}
		if *version != "" && m.Version != *version {
			return fmt.Errorf("manifest version %q does not match expected %q", m.Version, *version)
		}
		fmt.Printf("%s\t%s\t%s\n", m.Version, m.SHA256, m.URL)
		return nil
	}

	if *archive == "" || *goarch == "" || *version == "" || *dlURL == "" || *outPath == "" {
		return errors.New("required flags: --archive, --arch, --version, --url, --out")
	}

	rawKey := os.Getenv("MYGO_UPDATER_PRIVATE_KEY")
	if *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			return err
		}
		rawKey = string(b)
	}
	if strings.TrimSpace(rawKey) == "" {
		return errors.New("signing key required: set MYGO_UPDATER_PRIVATE_KEY or pass --key-file")
	}
	priv, err := remote.ParseUpdatePrivateKey(rawKey)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*pubKey) != "" {
		gotPub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
		if gotPub != strings.TrimSpace(*pubKey) {
			return errors.New("signing private key does not match --public-key")
		}
	}

	m, err := remote.SignServerManifest(context.Background(), *archive, *goos, *goarch, *version, *commit, *dlURL, priv)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(*outPath), ".server-manifest-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, *outPath)
}
