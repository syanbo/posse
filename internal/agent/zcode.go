package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var zcodeEvents = []string{"SessionStart", "UserPromptSubmit", "Stop"}

type ZCodeStorage struct {
	Dir      string
	DB       string
	LegacyDB string
}

func CurrentZCodeStorage() ZCodeStorage {
	return ZCodeStorage{Dir: os.Getenv("ZCODE_STORAGE_DIR"), DB: os.Getenv("ZCODE_SESSION_DB_PATH"), LegacyDB: os.Getenv("ZCODE_SESSION_DB")}
}

// ZCodeStorageRoot 返回用户配置和会话产物共用的根目录。
func ZCodeStorageRoot(home string) string {
	return CurrentZCodeStorage().Root(home)
}

func (s ZCodeStorage) Root(home string) string {
	if dir := strings.TrimSpace(s.Dir); dir != "" {
		if rel, ok := strings.CutPrefix(dir, "~/"); ok {
			return filepath.Join(home, rel)
		}
		return dir
	}
	return filepath.Join(home, ".zcode")
}

func ZCodeDatabase(home string) string {
	return CurrentZCodeStorage().Database(home)
}

// Database 按 CLI 的环境和用户配置查找会话库。
func (s ZCodeStorage) Database(home string) string {
	path := s.DB
	if path == "" {
		path = s.LegacyDB
	}
	if path == "" {
		var conf struct {
			Storage struct {
				DB string `json:"sessionDbPath"`
			} `json:"storage"`
		}
		if raw, err := os.ReadFile(filepath.Join(s.Root(home), "cli", "config.json")); err == nil && json.Unmarshal(raw, &conf) == nil {
			path = conf.Storage.DB
		}
	}
	if path == "" {
		return filepath.Join(s.Root(home), "cli", "db", "db.sqlite")
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return path
}

// WriteZCodePlugin 生成可加入 ZCode plugins.dirs 的本地插件，启用由用户自己的配置决定。
func (l Launcher) WriteZCodePlugin() (string, error) {
	dir := filepath.Join(l.Home, "zcode-plugin")
	manifest := map[string]string{"name": "posse-status", "version": "0.1.0", "description": "Report ZCode session status to Posse"}
	hooks := map[string][]map[string]any{}
	for _, event := range zcodeEvents {
		hooks[event] = []map[string]any{{"hooks": []map[string]any{{
			"type": "process", "command": l.Exe, "args": []string{"hook", "zcode", event}, "timeoutMs": 3000,
		}}}}
	}
	for path, doc := range map[string]any{
		filepath.Join(dir, ".zcode-plugin", "plugin.json"): manifest,
		filepath.Join(dir, "hooks", "hooks.json"):          map[string]any{"hooks": hooks},
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return "", err
		}
		if prior, err := os.ReadFile(path); err == nil && string(prior) == string(data) {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".posse-zcode-")
		if err != nil {
			return "", err
		}
		defer os.Remove(tmp.Name())
		_, err = tmp.Write(data)
		if err := errors.Join(err, tmp.Close()); err != nil {
			return "", err
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			return "", err
		}
	}
	return dir, nil
}

type ZCodeHook struct {
	Name      string    `json:"hookEventName"`
	SessionID string    `json:"sessionId"`
	TurnID    string    `json:"turnId"`
	Timestamp time.Time `json:"timestamp"`
	Response  string    `json:"last_assistant_message"`
}
