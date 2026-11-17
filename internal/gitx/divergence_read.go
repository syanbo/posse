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
	state, err := readDivergenceInputs(ctx, id, base)
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
			result.Conflicts, result.Reason, err = predictDivergence(ctx, id, state)
			if err != nil {
				return DivergenceRead{}, err
			}
			result.Prediction = DivergenceCompleted
			if result.Reason != "" {
				result.Prediction = DivergenceUnavailable
			}
		}
	}
	after, err := readDivergenceInputs(ctx, id, base)
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

func validDivergenceOID(s string) bool {
	return ValidBlobOID(s) && strings.Trim(s, "0") != ""
}
