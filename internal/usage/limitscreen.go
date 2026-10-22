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
		if wrote, err = keepFirst(bin, raw); err != nil {
			return "", "", false, err
		}
	}
	if wrote {
		_ = os.Remove(reading)
		if last != nil {
			if data, merr := json.MarshalIndent(last, "", "  "); merr == nil {
				_ = os.WriteFile(reading, append(data, '\n'), 0o644) //nolint:gosec // diagnostic artifact in the user's own cache
			}
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

// keepFirst writes data to path unless path exists; it reports whether it
// wrote.
func keepFirst(path string, data []byte) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err //nolint:wrapcheck // reported verbatim
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err //nolint:wrapcheck // reported verbatim
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return false, err //nolint:wrapcheck // reported verbatim
	}
	return true, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
