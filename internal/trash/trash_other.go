//go:build !darwin

package trash

import "errors"

func move(string) (string, error) {
	return "", errors.New("trash: moving items to trash is not supported on this platform")
}
