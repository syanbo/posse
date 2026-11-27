//go:build headless

package cli

import "errors"

func cmdApp(e *env, args []string) error {
	return errors.New("desktop window is not included in the headless Posse build")
}
