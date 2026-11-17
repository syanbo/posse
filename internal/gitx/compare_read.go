package gitx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

const CompareReadLimit = 1000

var (
	ErrCompareQuery   = errors.New("invalid Git comparison query")
	ErrCompareOutput  = errors.New("invalid Git comparison output")
	ErrCompareChanged = errors.New("Git comparison inputs changed; refresh the comparison")
)

type CompareRead struct {
	CurrentOID  string       `json:"currentOID"`
	OtherOID    string       `json:"otherOID"`
	OnlyCurrent []CommitInfo `json:"onlyCurrent"`
	OnlyOther   []CommitInfo `json:"onlyOther"`
	MoreCurrent bool         `json:"moreCurrent"`
	MoreOther   bool         `json:"moreOther"`
	Baseline    string       `json:"baseline"`
}

func ValidCompareQuery(current, other string, limit int) bool {
	return ValidReadRevision(current) && ValidReadRevision(other) && limit >= 1 && limit <= CompareReadLimit
}

func compareRevision(ctx context.Context, dir, revision string) (string, error) {
	out, err := historyRead(ctx, dir, nil, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSuffix(string(out), "\n")
	if !ValidBlobOID(oid) || strings.Trim(oid, "0") == "" {
		return "", ErrCompareOutput
	}
	return oid, nil
}

func compareStarts(ctx context.Context, dir, current, other string) (string, string, error) {
	a, err := compareRevision(ctx, dir, current)
	if err != nil {
		return "", "", err
	}
	if current == other {
		return a, a, nil
	}
	b, err := compareRevision(ctx, dir, other)
	if err != nil {
		return "", "", err
	}
	if len(a) != len(b) {
		return "", "", ErrCompareOutput
	}
	return a, b, nil
}

func compareCommits(ctx context.Context, dir, from, to string, limit int) ([]CommitInfo, bool, error) {
	out, err := historyRead(ctx, dir, nil, "log", "--no-patch", "--no-ext-diff", "--no-textconv", "--no-show-signature", "--no-color", "--format=%H%x00%D", "-z", "--date-order", "-n", strconv.Itoa(limit+1), "--end-of-options", from+".."+to, "--")
	if err != nil {
		return nil, false, err
	}
	ids, decorations, err := parseHistoryIDs(out)
	if err != nil {
		return nil, false, err
	}
	if len(ids) > limit+1 {
		return nil, false, ErrCompareOutput
	}
	for _, id := range ids {
		if len(id) != len(to) || id == from {
			return nil, false, ErrCompareOutput
		}
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	commits, err := readCommitObjects(ctx, dir, ids, decorations)
	return commits, more, err
}

// 双向列表只使用解析好的 OID；引用与浅边界前后复查，不拼接可移动修订的两次结果。
func ReadCompareChecked(ctx context.Context, dir, current, other string, limit int) (CompareRead, error) {
	var result CompareRead
	if !ValidCompareQuery(current, other, limit) {
		return result, ErrCompareQuery
	}
	refs, _, err := historyRefs(ctx, dir)
	if err != nil {
		return result, err
	}
	result.CurrentOID, result.OtherOID, err = compareStarts(ctx, dir, current, other)
	if err != nil {
		return CompareRead{}, err
	}
	result.OnlyCurrent, result.OnlyOther = []CommitInfo{}, []CommitInfo{}
	var readErr error
	if result.CurrentOID != result.OtherOID {
		result.OnlyOther, result.MoreOther, readErr = compareCommits(ctx, dir, result.CurrentOID, result.OtherOID, limit)
		if readErr == nil {
			result.OnlyCurrent, result.MoreCurrent, readErr = compareCommits(ctx, dir, result.OtherOID, result.CurrentOID, limit)
		}
	}
	if ctx.Err() != nil {
		return CompareRead{}, ctx.Err()
	}
	if errors.Is(readErr, ErrReadLimit) {
		return CompareRead{}, readErr
	}
	after, _, err := historyRefs(ctx, dir)
	if err != nil {
		return CompareRead{}, compareCheckError(ctx, err)
	}
	a, b, err := compareStarts(ctx, dir, current, other)
	if err != nil {
		return CompareRead{}, compareCheckError(ctx, err)
	}
	if !bytes.Equal(refs, after) || a != result.CurrentOID || b != result.OtherOID {
		return CompareRead{}, ErrCompareChanged
	}
	if readErr != nil {
		return CompareRead{}, readErr
	}
	seen := map[string]bool{}
	for _, side := range [][]CommitInfo{result.OnlyCurrent, result.OnlyOther} {
		for _, commit := range side {
			if seen[commit.Hash] {
				return CompareRead{}, ErrCompareOutput
			}
			seen[commit.Hash] = true
		}
	}
	baseline, _ := json.Marshal([]string{string(refs), current, other, result.CurrentOID, result.OtherOID})
	sum := sha256.Sum256(baseline)
	result.Baseline = hex.EncodeToString(sum[:])
	return result, ctx.Err()
}

func compareCheckError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrReadLimit) {
		return err
	}
	return ErrCompareChanged
}
