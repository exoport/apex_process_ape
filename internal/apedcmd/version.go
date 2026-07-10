package apedcmd

// Build metadata, stamped by goreleaser via -ldflags (mirrors internal/apecmd).
var (
	Version   = "dev"
	BuildDate = "unknown"
	GitCommit = "none"
)

// Exit codes (a small, stable table).
const (
	exitOK        = 0
	exitRunFailed = 1
	exitUsage     = 2
)
