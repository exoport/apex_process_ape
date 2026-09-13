// Package claudeprobe checks, once per Claude Code version, that the
// installed claude still drives the way ape expects — before a run finds out
// the hard way.
//
// Resilience remedy R3. ape's real dependency is the auto-updating `claude`
// binary, and the gates that verify it (`make check-harness`) run only when
// someone runs them, before a release. Nothing reacted to an update: v0.0.68
// was tagged on 2026-09-09 with every gate green, claude 2.1.269 installed
// itself two days later and broke every spawn in an untrusted directory, and
// the first thing to notice was an eval run. The gate would have caught it —
// it fails on 2.1.269 and passes on 2.1.268 — it just was not run.
//
// So the first run on a claude version ape has not seen probes it
// (repl.ProbeClaude: a zero-token spawn through the trust dialog to a ready
// REPL, about five seconds) and remembers the answer for that claude version
// and that ape version. A new ape re-probes too, since the walk it verifies
// is ape's.
package claudeprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/repl"
)

// EnvProbe turns the probe off when set to "off" — the escape hatch for a
// claude the probe misjudges. Skipping says so on every run; it is never
// silent.
const EnvProbe = "APE_CLAUDE_PROBE"

// versionRe is the shape `claude --version` prints, "2.1.270 (Claude Code)".
// A binary that prints anything else is not Claude Code — a test stand-in, a
// wrapper — and is not probed: its behaviour is not the contract.
var versionRe = regexp.MustCompile(`^(\d+\.\d+\.\d+\S*)\s+\(Claude Code\)$`)

// Options configures Ensure. The zero value of each func field selects the
// real implementation; tests replace them.
type Options struct {
	ClaudeBin  string
	ApeVersion string
	// CacheDir holds the verified-versions file and any saved probe bytes.
	// Empty means the user cache directory's `ape` folder.
	CacheDir string
	// Out receives the progress and warning lines.
	Out io.Writer

	Version func(ctx context.Context, claudeBin string) string
	Probe   func(ctx context.Context, claudeBin string) repl.ProbeResult
	Now     func() time.Time
}

// BrokenError is a claude the probe found ape cannot drive.
type BrokenError struct {
	ClaudeVersion string
	Result        repl.ProbeResult
	// SavedTo is where the raw PTY bytes were written, or "".
	SavedTo string
}

func (e *BrokenError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "claude %s failed ape's startup check: %s", e.ClaudeVersion, e.Result.Detail)
	if e.Result.Pane != "" {
		fmt.Fprintf(&b, "\nscreen:\n%s", e.Result.Pane)
	}
	if e.SavedTo != "" {
		fmt.Fprintf(&b, "\nraw PTY output: %s", e.SavedTo)
	}
	fmt.Fprintf(&b, "\nEvery run on this claude would meet the same screen. Install a claude version ape "+
		"has verified, update ape, or set %s=off to run anyway.", EnvProbe)
	return b.String()
}

// cacheFile is the verified pairs, newest last.
type cacheFile struct {
	Verified []verifiedPair `json:"verified"`
}

type verifiedPair struct {
	ClaudeVersion string `json:"claudeVersion"`
	ApeVersion    string `json:"apeVersion"`
	VerifiedAt    string `json:"verifiedAt"`
}

// maxCached bounds the file: claude releases often, and only recent pairs
// can still be installed.
const maxCached = 50

// Ensure probes the installed claude unless this claude version has already
// passed under this ape version. It returns a *BrokenError when the probe
// finds a claude ape cannot drive, and nil otherwise — including when the
// probe cannot decide, which it reports on Out without caching.
func Ensure(ctx context.Context, o Options) error {
	o = withDefaults(o)
	if strings.EqualFold(os.Getenv(EnvProbe), "off") {
		fmt.Fprintf(o.Out, "ape: claude startup check skipped (%s=off) — this claude is unverified\n", EnvProbe)
		return nil
	}
	m := versionRe.FindStringSubmatch(o.Version(ctx, o.ClaudeBin))
	if m == nil {
		return nil // not Claude Code, or not runnable: nothing to verify, and the run reports its own failure
	}
	claudeVersion := m[1]
	path := filepath.Join(o.CacheDir, "claude-contract.json")
	cache := load(path)
	if cache.has(claudeVersion, o.ApeVersion) {
		return nil
	}

	fmt.Fprintf(o.Out, "ape: claude %s is new to this ape — checking it still starts the way ape expects (once, a few seconds)…\n",
		claudeVersion)
	res := o.Probe(ctx, o.ClaudeBin)
	switch res.Verdict {
	case repl.ProbeVerified:
		cache.add(claudeVersion, o.ApeVersion, o.Now())
		if err := save(path, cache); err != nil {
			fmt.Fprintf(o.Out, "ape: claude %s verified, but the result could not be cached (%v) — it will be checked again\n",
				claudeVersion, err)
			return nil
		}
		fmt.Fprintf(o.Out, "ape: claude %s verified\n", claudeVersion)
		return nil
	case repl.ProbeBroken:
		saved := ""
		if len(res.Output) > 0 {
			p := filepath.Join(o.CacheDir, "claude-probe-"+claudeVersion+".bin")
			if err := os.MkdirAll(o.CacheDir, 0o755); err == nil && os.WriteFile(p, res.Output, 0o644) == nil { //nolint:gosec // diagnostic artifact in the user's own cache
				saved = p
			}
		}
		return &BrokenError{ClaudeVersion: claudeVersion, Result: res, SavedTo: saved}
	default:
		fmt.Fprintf(o.Out, "ape: WARNING — could not verify claude %s (%s); continuing unverified, and it will be checked again next run\n",
			claudeVersion, res.Detail)
		return nil
	}
}

func withDefaults(o Options) Options {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.CacheDir == "" {
		if dir, err := os.UserCacheDir(); err == nil {
			o.CacheDir = filepath.Join(dir, "ape")
		} else {
			o.CacheDir = filepath.Join(os.TempDir(), "ape")
		}
	}
	if o.Version == nil {
		o.Version = claudeVersionOutput
	}
	if o.Probe == nil {
		o.Probe = repl.ProbeClaude
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

func claudeVersionOutput(ctx context.Context, claudeBin string) string {
	vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, claudeBin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// load reads the cache. A missing or unreadable file is an empty cache: the
// cost is one extra probe, never a skipped one.
func load(path string) *cacheFile {
	c := &cacheFile{}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	if json.Unmarshal(data, c) != nil {
		return &cacheFile{}
	}
	return c
}

func save(path string, c *cacheFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err //nolint:wrapcheck // reported verbatim to the user
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err //nolint:wrapcheck // cannot fail for this type
	}
	return atomicfile.Write(path, append(data, '\n'))
}

func (c *cacheFile) has(claudeVersion, apeVersion string) bool {
	return slices.ContainsFunc(c.Verified, func(p verifiedPair) bool {
		return p.ClaudeVersion == claudeVersion && p.ApeVersion == apeVersion
	})
}

func (c *cacheFile) add(claudeVersion, apeVersion string, at time.Time) {
	c.Verified = append(c.Verified, verifiedPair{
		ClaudeVersion: claudeVersion, ApeVersion: apeVersion, VerifiedAt: at.UTC().Format(time.RFC3339),
	})
	if len(c.Verified) > maxCached {
		c.Verified = c.Verified[len(c.Verified)-maxCached:]
	}
}

// IsBroken reports whether err is a claude the probe could not drive.
func IsBroken(err error) bool {
	_, ok := errors.AsType[*BrokenError](err)
	return ok
}
