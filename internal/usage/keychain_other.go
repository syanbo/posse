//go:build !darwin

package usage

func keychainPassword(service, account string) (string, error) {
	return "", ErrCursorDenied
}
