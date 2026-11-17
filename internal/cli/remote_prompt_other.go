//go:build !darwin

package cli

import (
	"context"
	"errors"

	"posse/internal/remote"
)

func remoteTerminalPrompt(context.Context, remote.Prompt) (string, error) {
	return "", errors.New("interactive SSH authentication requires macOS")
}
