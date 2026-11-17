package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// ReadFileVersion 只把与原始正文一致的磁盘版本交给编辑器，不能用较新的版本保护较旧的正文。
func ReadFileVersion(ctx context.Context, path string) (Content, Version, error) {
	if err := ctx.Err(); err != nil {
		return Content{}, Version{}, err
	}
	content, err := ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Content{}, Version{}, err
	}
	if err == nil && content.Kind != Text {
		return content, Version{}, ctx.Err()
	}
	version, versionErr := ReadVersion(ctx, path)
	if versionErr != nil {
		return Content{}, Version{}, versionErr
	}
	if err != nil {
		if !version.Stamp.Missing {
			return Content{}, Version{}, ErrChanged
		}
		return Content{Stamp: version.Stamp}, version, err
	}
	sum := sha256.Sum256([]byte(content.Text))
	if !content.Stamp.Same(version.Stamp) || content.Stamp.Mode != version.Stamp.Mode || hex.EncodeToString(sum[:]) != version.Hash {
		return Content{}, Version{}, ErrChanged
	}
	return content, version, nil
}
