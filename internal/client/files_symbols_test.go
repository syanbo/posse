package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestRemoteFileSymbolsAndDefinition(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.ts")
	if err := os.WriteFile(path, []byte("export function LocalOnly() {}"), 0600); err != nil {
		t.Fatal(err)
	}
	symbolsParams := rpc.FileSymbolsParams{Root: root, SingleRepo: true, Query: files.SymbolQuery{Text: "Config", TypesOnly: true, Dir: "src"}, Overlay: map[string]string{"src/config.ts": "class Config {}"}}
	definitionParams := rpc.FileDefinitionParams{Root: root, SingleRepo: true, Path: path, Text: "LocalOnly();", Offset: 2, Overlay: map[string]string{"source.ts": "LocalOnly();"}}
	c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.symbols":
			var got rpc.FileSymbolsParams
			if err := json.Unmarshal(p.Params, &got); err != nil || !reflect.DeepEqual(got, symbolsParams) {
				t.Errorf("symbols params: %+v %v", got, err)
			}
			return rpc.FileSymbolsResult{Symbols: []files.Symbol{{Name: "RemoteConfig", Path: "target.ts"}}, Truncated: true}, nil
		case "files.definition":
			var got rpc.FileDefinitionParams
			if err := json.Unmarshal(p.Params, &got); err != nil || !reflect.DeepEqual(got, definitionParams) {
				t.Errorf("definition params: %+v %v", got, err)
			}
			return rpc.FileDefinitionResult{Path: "/target/only.ts", Line: 7}, nil
		default:
			t.Errorf("unexpected method %s", p.Method)
			return nil, nil
		}
	})
	symbols, err := c.SearchSymbols(context.Background(), symbolsParams)
	if err != nil || len(symbols.Symbols) != 1 || symbols.Symbols[0].Name != "RemoteConfig" || !symbols.Truncated {
		t.Fatalf("symbols: %+v %v", symbols, err)
	}
	definition, err := c.FindDefinition(context.Background(), definitionParams)
	if err != nil || definition.Path != "/target/only.ts" || definition.Line != 7 || len(definition.Definitions) != 0 {
		t.Fatalf("definition: %+v %v", definition, err)
	}
}

func TestRemoteFileSymbolsDefinitionErrorsAndCancel(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.ts")
	if err := os.WriteFile(path, []byte("export function LocalOnly() {}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"files.symbols", "files.definition"} {
		t.Run(method, func(t *testing.T) {
			call := func(ctx context.Context, c *Client) error {
				if method == "files.symbols" {
					out, err := c.SearchSymbols(ctx, rpc.FileSymbolsParams{Root: root, Query: files.SymbolQuery{Text: "LocalOnly"}})
					if len(out.Symbols) != 0 {
						t.Error("used local symbols after target error")
					}
					return err
				}
				out, err := c.FindDefinition(ctx, rpc.FileDefinitionParams{Root: root, Path: path, Text: "LocalOnly();", Offset: 1})
				if out.Path != "" || len(out.Definitions) != 0 {
					t.Error("used local definitions after target error")
				}
				return err
			}
			c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
				if p.Method != method {
					t.Errorf("method=%s", p.Method)
				}
				return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
			})
			var re *rpc.Error
			if err := call(context.Background(), c); !errors.As(err, &re) || re.Code != rpc.CodePermissionDenied {
				t.Fatalf("target error lost: %v", err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			c = fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
				close(entered)
				<-release
				return nil, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- call(ctx, c) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not arrive")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancel did not stop request")
			}
		})
	}
}
