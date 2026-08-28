package runlog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HarnessFile names which Claude Code produced a run.
//
// The stamp used to live only in the pipeline manifest, so it existed for
// exactly the run kinds that go through pipeline.Run — `ape pipeline`,
// `ape task`, `ape script`. `ape prompt` and `ape chat` drive claude
// directly and write no manifest, so their runs carried no version at all.
//
// That mattered because hookdrift scopes its verdict to one Claude Code
// version, and picks that version from the newest run in the window. A
// single recent prompt run therefore pulled the whole verdict into the
// unstamped bucket and excluded every properly-stamped pipeline run
// alongside it — and inside that bucket, pre- and post-upgrade runs fused
// together, which is precisely the masking the version scoping exists to
// prevent.
//
// The stamp lives here, and not on each command's own record struct,
// because every run kind opens a Writer — that is what creates
// hook-events.jsonl in the first place. A stamp written by New cannot be
// forgotten by a producer that does not exist yet, the same reasoning that
// makes RunRoots the single list of run trees rather than something each
// consumer re-derives.
const HarnessFile = "harness.yaml"

// DefaultClaudeBin is the executable New probes when no caller overrides it.
const DefaultClaudeBin = "claude"

// claudeVersionTimeout bounds the probe. `claude --version` is a local
// exec, so this is a hang guard, not a budget.
const claudeVersionTimeout = 5 * time.Second

// Option configures New.
type Option func(*writerConfig)

type writerConfig struct{ claudeBin string }

// WithClaudeBin names the executable whose --version is stamped, for the
// callers that already let the spawned claude be overridden. An empty
// path keeps DefaultClaudeBin, so passing an unset override is a no-op
// rather than a probe of "".
func WithClaudeBin(path string) Option {
	return func(c *writerConfig) {
		if path != "" {
			c.claudeBin = path
		}
	}
}

var (
	claudeVersionMu sync.Mutex
	// Keyed by binary path. Resolved once per process: a long-lived `ape
	// service` opens many runlogs, and the answer cannot change under a
	// running process in a way this stamp could act on. Holding the lock
	// across the probe also collapses a burst of concurrent opens into a
	// single exec.
	claudeVersionCache = map[string]string{}
	// claudeVersionFn is the probe, swapped in tests so opening a runlog
	// does not depend on a claude binary being installed.
	claudeVersionFn = execClaudeVersion
)

func claudeVersionFor(bin string) string {
	claudeVersionMu.Lock()
	defer claudeVersionMu.Unlock()
	if v, ok := claudeVersionCache[bin]; ok {
		return v
	}
	v := claudeVersionFn(bin)
	claudeVersionCache[bin] = v
	return v
}

func execClaudeVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), claudeVersionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// writeHarnessStamp records the harness version beside the run's streams.
// Best-effort throughout: a run that cannot name its harness is still a
// run, and failing New over it would take down the pipeline for a
// diagnostic file.
func writeHarnessStamp(dir, claudeBin string) {
	path := filepath.Join(dir, HarnessFile)
	// Stamped once, at open. New is also the reopen path (a resumed or
	// re-entered run dir appends to the same streams), and re-stamping
	// there would relabel events an earlier harness actually wrote. The
	// manifest records the version at run start for the same reason.
	if _, err := os.Stat(path); err == nil {
		return
	}
	v := claudeVersionFor(claudeBin)
	if v == "" {
		// No file rather than an empty value. Absent is the honest signal
		// — hookdrift falls back to the manifest and, failing that, judges
		// the run unstamped. A `claude_version: ""` would instead assert
		// that the run happened under no version at all.
		return
	}
	//nolint:gosec // user-visible runlog metadata; world-readable is intentional
	_ = os.WriteFile(path, []byte("claude_version: "+v+"\n"), 0o644)
}
