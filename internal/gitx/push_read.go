package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

const PushPreviewReadLimit = 500

type pushPreviewReadKey struct{}

// WithPushPreviewRead 让预览前后的身份查询也净化 Git 初始化时回显的推送配置值。
func WithPushPreviewRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, pushPreviewReadKey{}, true)
}

var (
	ErrPushQuery   = errors.New("invalid Git push preview query")
	ErrPushOutput  = errors.New("invalid Git push preview output")
	ErrPushChanged = errors.New("Git push preview inputs changed; refresh the preview")
	ErrPushConfig  = errors.New("Git push preview cannot safely represent this remote configuration")
)

// PushPreviewRead 只描述本地已有引用；写入方必须重新核对版本和目标，不能把它作为推送许可。
type PushPreviewRead struct {
	Plan            PushPlan `json:"plan"`
	BranchOID       string   `json:"branchOID"`
	TrackingRef     string   `json:"trackingRef"`
	TrackingOID     string   `json:"trackingOID"`
	TrackingMissing bool     `json:"trackingMissing"`
	Baseline        string   `json:"baseline"`
	More            bool     `json:"more"`
}

func ValidPushBranch(branch string) bool {
	return branch != "HEAD" && ValidReadRevision(branch) && !strings.ContainsAny(branch, "~^")
}
func ValidPushRemote(remote string) bool {
	return remote != "." && ValidReadRevision(remote) && !strings.ContainsAny(remote, "~^")
}
func ValidPushPreviewQuery(branch string, limit int) bool {
	return ValidPushBranch(branch) && limit >= 1 && limit <= PushPreviewReadLimit
}

func pushBaseline(state pushInputs) string {
	raw, _ := json.Marshal(struct {
		Refs, Config, Atom []byte
		Preview            PushPreviewRead
		Excludes           []string
	}{state.refs, state.config, state.atom, state.preview, state.excludes})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func ReadPushPreviewChecked(ctx context.Context, dir, branch string, limit int) (PushPreviewRead, error) {
	if !ValidPushPreviewQuery(branch, limit) {
		return PushPreviewRead{}, ErrPushQuery
	}
	ctx = WithPushPreviewRead(ctx)
	state, err := readPushInputs(ctx, dir, branch)
	if err != nil {
		return PushPreviewRead{}, err
	}
	result := state.preview
	result.Baseline = pushBaseline(state)
	var readErr error
	if result.TrackingMissing {
		var input strings.Builder
		input.WriteString(result.BranchOID)
		input.WriteByte('\n')
		for _, oid := range state.excludes {
			input.WriteByte('^')
			input.WriteString(oid)
			input.WriteByte('\n')
		}
		raw, err := historyRead(ctx, dir, []byte(input.String()), "log", "--no-patch", "--no-ext-diff", "--no-textconv", "--no-show-signature", "--no-color", "--format=%H%x00%D", "-z", "--date-order", "-n", strconv.Itoa(limit+1), "--stdin", "--")
		readErr = err
		if readErr == nil {
			ids, decorations, err := parseHistoryIDs(raw)
			readErr = err
			if readErr == nil {
				if len(ids) > limit+1 {
					readErr = ErrPushOutput
				} else {
					result.More = len(ids) > limit
					if result.More {
						ids = ids[:limit]
					}
					result.Plan.Commits, readErr = readCommitObjects(ctx, dir, ids, decorations)
				}
			}
		}
	} else {
		result.Plan.Commits, result.More, readErr = compareCommits(ctx, dir, result.TrackingOID, result.BranchOID, limit)
	}
	if ctx.Err() != nil {
		return PushPreviewRead{}, ctx.Err()
	}
	if errors.Is(readErr, ErrReadLimit) {
		return PushPreviewRead{}, readErr
	}
	after, err := readPushInputs(ctx, dir, branch)
	if err != nil {
		if ctx.Err() != nil {
			return PushPreviewRead{}, ctx.Err()
		}
		if errors.Is(err, ErrReadLimit) {
			return PushPreviewRead{}, err
		}
		return PushPreviewRead{}, ErrPushChanged
	}
	if pushBaseline(after) != result.Baseline {
		return PushPreviewRead{}, ErrPushChanged
	}
	if readErr != nil {
		return PushPreviewRead{}, readErr
	}
	return result, ctx.Err()
}
