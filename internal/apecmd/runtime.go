package apecmd

import (
	"os/exec"
	"runtime"
)

// runtimeGOOS is split out so tests can override; today it just wraps
// runtime.GOOS. Used by openBrowser to pick the platform launcher.
func runtimeGOOS() string { return runtime.GOOS }

// openBrowser launches the user's default browser pointed at url.
// Returns an error if the platform launcher exits non-zero; callers
// typically ignore the return value because failure to open a browser
// is non-fatal (the URL is also printed to stderr).
//
// PLAN-6 / Phase G: relocated from internal/apecmd/chat.go (since
// removed). The web pipeline mode and `ape sessions open` both call
// this helper.
func openBrowser(url string) error {
	var (
		bin  string
		args []string
	)
	switch runtimeGOOS() {
	case "darwin":
		bin = "open"
		args = []string{url}
	case "windows":
		bin = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		bin = "xdg-open"
		args = []string{url}
	}
	return exec.Command(bin, args...).Start() //nolint:gosec // launcher binaries are platform standards, never user-controlled
}
