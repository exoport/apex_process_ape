package apecmd

import "runtime"

// runtimeGOOS is split out so tests can override; today it just wraps
// runtime.GOOS. Used by openBrowser in chat.go.
func runtimeGOOS() string { return runtime.GOOS }
