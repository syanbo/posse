package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	DivergenceCompleted   = "completed"
	DivergenceNotNeeded   = "not_needed"
	DivergenceUnavailable = "unavailable"

	DivergenceAttributes    = "attributes"
	DivergenceConfig        = "config"
	DivergenceSubmodule     = "submodule"
	DivergenceShallow       = "shallow"
	DivergenceMultipleBases = "multiple_bases"
	DivergenceUnrelated     = "unrelated"
	DivergenceLimit         = "limit"
	DivergenceGit           = "git"
)

var (
	ErrDivergenceQuery   = errors.New("invalid Git divergence query")
	ErrDivergenceOutput  = errors.New("invalid Git divergence output")
	ErrDivergenceChanged = errors.New("Git divergence inputs changed; refresh the comparison")
)

type DivergenceRead struct {
	Head       string   `json:"head"`
	Base       string   `json:"base"`
	Ahead      int      `json:"ahead"`
	Behind     int      `json:"behind"`
	Prediction string   `json:"prediction"`
	Reason     string   `json:"reason"`
	Conflicts  []string `json:"conflicts"`
	Baseline   string   `json:"baseline"`
	// Unchanged 只确认输入基线；计数与预测由窗口保留，服务不接受客户端的预测内容。
	Unchanged bool `json:"unchanged"`
}

func ValidDivergenceQuery(base, previous string) bool {
	return ValidReadRevision(base) && (previous == "" || ValidLogBaseline(previous))
}

func ValidDivergenceReason(reason string) bool {
	switch reason {
	case DivergenceAttributes, DivergenceConfig, DivergenceSubmodule, DivergenceShallow, DivergenceMultipleBases, DivergenceUnrelated, DivergenceLimit, DivergenceGit:
		return true
	}
	return false
}

func ReadDivergenceChecked(ctx context.Context, id RepositoryIdentity, base, previous string) (DivergenceRead, error) {
	if !ValidDivergenceQuery(base, previous) {
		return DivergenceRead{}, ErrDivergenceQuery
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	shares, refund, err := splitReadBudget(ctx, 3)
	if err != nil {
		return DivergenceRead{}, err
	}
	defer refund()
	state, err := readDivergenceInputsWithin(shares[0], id, base)
	if err != nil {
		return DivergenceRead{}, err
	}
	raw, _ := json.Marshal(state.baseline)
	sum := sha256.Sum256(raw)
	result := DivergenceRead{Head: state.baseline.Head, Base: state.baseline.Base, Baseline: hex.EncodeToString(sum[:])}
	if previous == result.Baseline {
		result.Unchanged = true
	} else {
		result.Ahead, result.Behind = state.baseline.Ahead, state.baseline.Behind
		result.Conflicts = []string{}
		switch {
		case result.Ahead == 0 || result.Behind == 0:
			result.Prediction = DivergenceNotNeeded
		case state.reason != "":
			result.Prediction, result.Reason = DivergenceUnavailable, state.reason
		default:
			result.Conflicts, result.Reason, err = predictDivergence(shares[1], id, state)
			if errors.Is(err, ErrReadLimit) {
				result.Conflicts, result.Reason, err = []string{}, DivergenceLimit, nil
			}
			if err != nil {
				return DivergenceRead{}, err
			}
			result.Prediction = DivergenceCompleted
			if result.Reason != "" {
				result.Prediction = DivergenceUnavailable
			}
		}
	}
	after, err := readDivergenceInputsWithin(shares[2], id, base)
	if err != nil {
		if ctx.Err() != nil {
			return DivergenceRead{}, ctx.Err()
		}
		if errors.Is(err, ErrReadLimit) {
			return DivergenceRead{}, err
		}
		return DivergenceRead{}, ErrDivergenceChanged
	}
	rawAfter, _ := json.Marshal(after.baseline)
	if string(raw) != string(rawAfter) || state.reason != after.reason {
		return DivergenceRead{}, ErrDivergenceChanged
	}
	return result, ctx.Err()
}

// 两遍输入各占同样大小的份额，超出时停在同一步，复查才能得到同样的基线；用剩的份额退回调用方。
func splitReadBudget(ctx context.Context, parts int) ([]context.Context, func(), error) {
	parent, _ := ctx.Value(readLimitKey{}).(*readBudget)
	if parent == nil {
		return nil, nil, ErrReadLimit
	}
	parent.Lock()
	share := parent.remaining / parts
	parent.remaining -= share * parts
	parent.Unlock()
	shares, budgets := make([]context.Context, parts), make([]*readBudget, parts)
	for i := range shares {
		budgets[i] = &readBudget{remaining: share}
		shares[i] = context.WithValue(ctx, readLimitKey{}, budgets[i])
	}
	refund := func() {
		left := 0
		for _, b := range budgets {
			b.Lock()
			left += b.remaining
			b.remaining = 0
			b.Unlock()
		}
		parent.Lock()
		parent.remaining += left
		parent.Unlock()
	}
	return shares, refund, nil
}

// 计数之后的预测输入超出份额只让预测不可用；只留计数前的字段，两遍的基线才一致。
func readDivergenceInputsWithin(ctx context.Context, id RepositoryIdentity, base string) (divergenceInputs, error) {
	s, err := readDivergenceInputs(ctx, id, base)
	if !errors.Is(err, ErrReadLimit) || !s.counted || ctx.Err() != nil {
		return s, err
	}
	b := s.baseline
	return divergenceInputs{
		baseline: divergenceBaseline{Version: b.Version, Head: b.Head, Base: b.Base, BaseRef: b.BaseRef, Ahead: b.Ahead, Behind: b.Behind, Shallow: b.Shallow},
		counted:  true,
		reason:   DivergenceLimit,
	}, nil
}

func validDivergenceOID(s string) bool {
	return ValidBlobOID(s) && strings.Trim(s, "0") != ""
}
