package client

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestRemoteVersionedFile(t *testing.T) {
	for _, mode := range []string{"raw", "mixed", "permissions", "missing", "created", "offline", "invalid-path", "binary"} {
		t.Run(mode, func(t *testing.T) {
			data := append(bytes.Repeat([]byte{'a'}, files.RangeLimit), []byte("\r\nraw\xff")...)
			if mode == "binary" {
				data[0] = 0
			}
			st := files.Stamp{Size: int64(len(data)), Ino: 42, Mode: 0o640}
			sum := sha256.Sum256(data)
			version := files.Version{Path: "/resolved/file", Stamp: st, Hash: hex.EncodeToString(sum[:])}
			reads := 0
			c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "files.stat":
					stamp := st
					if mode == "missing" || mode == "created" {
						stamp = files.Stamp{Missing: true}
					}
					return []rpc.FileStatResult{{Path: "/target/link", Stamp: stamp}}, nil
				case "files.readRange":
					var q rpc.FileRangeParams
					if err := rpc.Decode(p.Params, &q); err != nil {
						t.Fatal(err)
					}
					end := min(int(q.Offset)+q.Limit, len(data))
					part := bytes.Clone(data[int(q.Offset):end])
					if mode == "mixed" && q.Offset == 0 {
						part[0] = 'b'
					}
					return rpc.FileRangeResult{Stamp: st, Data: part, EOF: end == len(data)}, nil
				case "files.version":
					reads++
					v := version
					switch mode {
					case "permissions":
						v.Stamp.Mode = 0o600
					case "missing":
						v.Stamp, v.Hash = files.Stamp{Missing: true}, ""
					case "offline":
						return nil, rpc.Errorf(rpc.CodeInternal, "offline")
					case "invalid-path":
						v.Path = "relative"
					}
					return v, nil
				default:
					t.Errorf("unexpected %s", p.Method)
					return nil, nil
				}
			})
			content, got, err := c.ReadFileVersion(t.Context(), "/target/link")
			switch mode {
			case "raw":
				if err != nil || content.Text != string(data) || !got.Same(version) || reads != 1 {
					t.Fatalf("raw baseline: %+v %v reads=%d", got, err, reads)
				}
			case "missing":
				if !os.IsNotExist(err) || !got.Stamp.Missing || got.Path != version.Path || got.Hash != "" || !content.Stamp.Missing {
					t.Fatalf("missing baseline: %+v %v", got, err)
				}
			case "binary":
				if err != nil || content.Kind != files.Binary || got.Path != "" || reads != 0 {
					t.Fatalf("binary read: %+v %v reads=%d", got, err, reads)
				}
			default:
				if err == nil || os.IsNotExist(err) || content.Text != "" || got.Path != "" {
					t.Fatalf("failed baseline exposed text: %+v %v", got, err)
				}
			}
		})
	}
}
