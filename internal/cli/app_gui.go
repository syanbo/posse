//go:build !headless

package cli

import "posse/internal/app"

func cmdApp(e *env, args []string) error { return app.Run(e.client()) }
