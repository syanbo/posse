package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"posse/internal/agent"
)

func openZCodeReader(path string) (*sql.DB, error) {
	if path == "" {
		return nil, nil
	}
	if fi, err := os.Stat(path); errors.Is(err, os.ErrNotExist) || err == nil && !fi.Mode().IsRegular() {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(500)"}
	return sql.Open("sqlite", u.String())
}

func ZCodeHasSoftDelete(ctx context.Context, tx *sql.Tx) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pragma_table_info('session') WHERE name = 'time_deleted')`).Scan(&exists)
	return exists, err
}

// ZCodeRuntimeSession 只认同一进程唯一的顶层会话，排除子 agent 和工作流会话。
func ZCodeRuntimeSession(ctx context.Context, path string, pid int) (id, title string, ambiguous bool) {
	db, err := openZCodeReader(path)
	if err != nil || db == nil {
		return "", "", false
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT s.id, s.title
		FROM session_runtime_presence AS p JOIN session AS s ON s.id = p.session_id
		WHERE p.process_id = ? AND s.task_type IN ('interactive', 'fork') AND s.time_deleted IS NULL LIMIT 2`, pid)
	if err != nil {
		return "", "", false
	}
	defer rows.Close()
	if !rows.Next() || rows.Scan(&id, &title) != nil {
		return "", "", false
	}
	if rows.Next() {
		return "", "", true
	}
	if rows.Err() != nil || !agent.IsZCodeSession(id) {
		return "", "", false
	}
	return id, title, false
}

// ReadZCodeUsage 只读指定会话的原生工具与模型用量。一次查询共用同一份 SQLite 快照。
func ReadZCodeUsage(ctx context.Context, path, session string) (SessionUsage, error) {
	if !agent.IsZCodeSession(session) {
		return SessionUsage{}, nil
	}
	db, err := openZCodeReader(path)
	if err != nil || db == nil {
		return SessionUsage{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SessionUsage{}, err
	}
	defer tx.Rollback()
	softDelete, err := ZCodeHasSoftDelete(ctx, tx)
	if err != nil {
		return SessionUsage{}, err
	}
	query := `SELECT directory FROM session WHERE id = ?`
	if softDelete {
		query += ` AND time_deleted IS NULL`
	}
	var cwd string
	err = tx.QueryRowContext(ctx, query, session).Scan(&cwd)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionUsage{}, nil
	}
	if err != nil {
		return SessionUsage{}, err
	}
	var toolLedger, modelLedger bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'tool_usage'),
		EXISTS (SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'model_usage')`).Scan(&toolLedger, &modelLedger)
	if err != nil {
		return SessionUsage{}, err
	}
	usage, err := zcodeTools(ctx, tx, session, cwd, toolLedger)
	if err != nil {
		return SessionUsage{}, err
	}
	if modelLedger {
		usage.Tokens, err = zcodeTokens(ctx, tx, session)
	}
	return SessionUsage{Found: true, Usage: usage}, err
}

func zcodeTools(ctx context.Context, tx *sql.Tx, session, cwd string, ledger bool) (Usage, error) {
	// 原生用量负责计数和状态。消息片段只补用量表没有的参数，分叉复制的片段没有子会话的用量记录。
	const parts = `WITH parts AS NOT MATERIALIZED (
		SELECT id, time_created, time_updated, CASE WHEN json_valid(data) THEN data ELSE '{}' END AS part
		FROM part WHERE session_id = ?1
	), tools AS (
		SELECT id, part ->> '$.callID' AS call_id,
			row_number() OVER (PARTITION BY coalesce(nullif(part ->> '$.callID', ''), id)
				ORDER BY time_updated DESC, id DESC) AS latest
		FROM parts WHERE part ->> '$.type' = 'tool'
		AND coalesce(part ->> '$.state.input.source', '') <> 'browser_turn_end'
	)`
	const details = `substr(coalesce(part ->> '$.state.input', '{}'), 1, 32768),
		coalesce(part ->> '$.state.metadata.display.serverName', part ->> '$.metadata.display.serverName', ''),
		coalesce(part ->> '$.state.metadata.display.toolName', part ->> '$.metadata.display.toolName', ''),
		coalesce(part ->> '$.state.metadata.providerIsError', part ->> '$.metadata.providerIsError', 0)`
	query := parts + ` SELECT coalesce(part ->> '$.tool', ''), part ->> '$.state.status',
		coalesce(part ->> '$.state.time.start', time_created), ` + details + `
		FROM tools JOIN parts USING (id) WHERE latest = 1 AND part ->> '$.state.status' IN ('completed', 'error')
		ORDER BY time_created, id`
	if ledger {
		query = parts + ` SELECT u.tool_name, u.status, u.started_at, ` + details + `
			FROM tool_usage AS u
			LEFT JOIN tools ON tools.call_id = u.tool_call_id AND tools.latest = 1
			LEFT JOIN parts ON parts.id = tools.id
			WHERE u.session_id = ?1 AND u.status IN ('completed', 'error', 'cancelled')
			ORDER BY u.started_at, u.id`
	}
	rows, err := tx.QueryContext(ctx, query, session)
	if err != nil {
		return Usage{}, err
	}
	defer rows.Close()
	s := &UsageScanner{}
	s.reset()
	counts := map[string]int{}
	for rows.Next() {
		var name, status, input, server, tool string
		var at int64
		var failed bool
		if err := rows.Scan(&name, &status, &at, &input, &server, &tool, &failed); err != nil {
			return Usage{}, err
		}
		s.at = time.UnixMilli(at)
		failed = failed || status != "completed"
		if server == "" {
			if full, ok := strings.CutPrefix(name, "mcp__"); ok {
				server, tool, _ = strings.Cut(full, "__")
			}
		}
		if server != "" && tool != "" {
			s.mcpCall(name, server, tool)
			s.mcpResult(name, failed)
			if !failed {
				s.toolURL([]byte(input), server+"."+tool)
			}
			continue
		}
		if name == "" {
			continue
		}
		counts[name]++
		if failed {
			continue
		}
		var args struct {
			Command  string `json:"command"`
			FilePath string `json:"file_path"`
			Skill    string `json:"skill"`
		}
		if json.Unmarshal([]byte(input), &args) != nil {
			continue
		}
		switch name {
		case "Bash":
			s.command(args.Command)
		case "Read":
			s.skillRead(args.FilePath, cwd)
		case "Skill":
			if args.Skill != "" {
				s.addSkill(args.Skill)
			}
		default:
			s.webTool(name, []byte(input))
		}
	}
	u := s.result()
	for name, n := range counts {
		u.Tools = append(u.Tools, UsageItem{Name: name, Count: n})
	}
	sortItems(u.Tools)
	return u, rows.Err()
}

func zcodeTokens(ctx context.Context, tx *sql.Tx, session string) (*TokenUsage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT query_source, input_tokens, output_tokens,
		cache_creation_input_tokens, cache_read_input_tokens, coalesce(provider_total_tokens, computed_total_tokens)
		FROM model_usage WHERE session_id = ? ORDER BY started_at, id`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var u TokenUsage
	baselines := map[string]int64{}
	for rows.Next() {
		var source string
		var input, output, creation, read, total int64
		if err := rows.Scan(&source, &input, &output, &creation, &read, &total); err != nil {
			return nil, err
		}
		cache := creation + read
		// 旧版可能把缓存单列，新版已包含在 input_tokens。与 ZCode 原生会话用量使用相同判断。
		if input <= 0 || cache > 0 && total > 0 && zcodeAbs(total-input-output-cache) < zcodeAbs(total-input-output) {
			input += cache
		}
		added := input
		switch source {
		case "main_turn", "subagent", "workflow_child":
			added = max(0, input-baselines[source])
			baselines[source] = input
		}
		u.Input += added
		u.Output += output
		u.Total += added + max(0, total-input)
		u.Requests++
	}
	if u.Requests == 0 {
		return nil, rows.Err()
	}
	return &u, rows.Err()
}

func zcodeAbs(n int64) int64 { return max(n, -n) }
