package usage

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// The limit screen (W4-A3). When a claude.ai usage limit stops a session
// (H20: ape reads Claude Code's limit line in the pane and ends the run,
// exit 14), ape keeps the raw PTY bytes behind that screen — the first one
// per Claude Code version, as the startup probe keeps a broken startup —
// with the last usage reading beside it: what Claude Code drew at the
// limit, and what its status line said just before. Nothing provokes a
// limit to get one.

// versionSafe is what a version may hold in a file name.
var versionSafe = regexp.MustCompile(`[^0-9A-Za-z.+-]`)

// LimitScreenPaths are <user cache>/ape/claude-limit-<version>.bin and its
// .json; version "" is "unknown".
func LimitScreenPaths(claudeVersion string) (bin, reading string) {
	v := versionSafe.ReplaceAllString(claudeVersion, "_")
	if v == "" {
		v = "unknown"
	}
	base := filepath.Join(Dir(), "claude-limit-"+v)
	return base + ".bin", base + ".json"
}

// SaveLimitScreen keeps raw as the limit screen of claudeVersion, with
// last (nil: none) beside it, unless one is kept already: the first per
// version stays. It returns the paths that exist afterwards ("" for one
// that does not), and whether this call wrote them.
func SaveLimitScreen(claudeVersion string, raw []byte, last *Reading) (bin, reading string, wrote bool, err error) {
	bin, reading = LimitScreenPaths(claudeVersion)
	if len(raw) > 0 {
		if err = os.MkdirAll(Dir(), 0o755); err != nil {
			return "", "", false, err //nolint:wrapcheck // reported verbatim
		}
		f, openErr := os.OpenFile(bin, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		switch {
		case openErr == nil:
			_, err = f.Write(raw)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				_ = os.Remove(bin)
				return "", "", false, err //nolint:wrapcheck // reported verbatim
			}
			wrote = true
			if last != nil {
				data, _ := json.MarshalIndent(last, "", "  ")
				_ = os.WriteFile(reading, append(data, '\n'), 0o644) //nolint:gosec // diagnostic artifact in the user's own cache
			} else {
				_ = os.Remove(reading)
			}
		case !errors.Is(openErr, fs.ErrExist):
			return "", "", false, openErr //nolint:wrapcheck // reported verbatim
		}
	}
	if !exists(bin) {
		bin = ""
	}
	if !exists(reading) {
		reading = ""
	}
	return bin, reading, wrote, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
