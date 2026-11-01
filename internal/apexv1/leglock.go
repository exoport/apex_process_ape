package apexv1

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The host's compose-leg lock (v1.5, D705, FINDING-V15-1A-4): a check
// that brings up containers or services holds it while it runs, so two
// gates, closes or verifies on one host — two milestones' members — take
// their live legs one at a time. Their own compose projects
// (COMPOSE_PROJECT_NAME, checkinfra.go) do not separate a fixed host port
// (`5615:5432`) or a fixed container name: eval's pair on mmr7 turned 9
// and 7 live legs red that the solo run had green.
//
// The lock is host-wide, not per project: a host port is the host's, and
// two clones of one project need not share a name. It is a file lock in
// the system temporary folder, made writable by every user, since another
// user's docker publishes on the same ports.
//
// A check is a compose leg (composeLeg) when it is a full check — the
// framework's mark for a service, a network or a browser — or its command
// names a container runtime, or sets a `*_LIVE` variable on (atomix's
// convention, and a common one). Anything else runs unlocked: holding
// every check would serialize two gates whole.

// legLockPath is the lock's file. A var so tests can choose.
var legLockPath = filepath.Join(os.TempDir(), "ape-compose-legs.lock")

// legLockPoll is how often a waiting check tries the lock again. A var so
// tests can wait fast.
var legLockPoll = time.Second

var (
	legRuntimeRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_.-])(?:docker|docker-compose|podman|nerdctl|testcontainers)(?:[^a-z0-9_-]|$)`)
	legLiveRe    = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])[A-Z][A-Z0-9_]*_LIVE=(?:1|true|yes|on)(?:[^A-Za-z0-9_]|$)`)
)

// composeLeg reports a check that holds the compose-leg lock.
func composeLeg(pc *PlannedCheck) bool {
	return pc.Full || legRuntimeRe.MatchString(pc.Cmd) || legLiveRe.MatchString(pc.Cmd)
}

// LegLockUse sums a run's compose-leg lock: how many checks held it, how
// many of them waited for another run's leg first, and for how long in
// all.
type LegLockUse struct {
	Checks   int     `json:"checks"`
	Waited   int     `json:"waited"`
	WaitSecs float64 `json:"wait_secs"`
}

// legLockUse sums results' use of the lock, nil when no check held it.
func legLockUse(results []CheckResult) *LegLockUse {
	var u LegLockUse
	for i := range results {
		if !results[i].LegLock {
			continue
		}
		u.Checks++
		if results[i].LegLockWaitSecs > 0 {
			u.Waited++
			u.WaitSecs += results[i].LegLockWaitSecs
		}
	}
	if u.Checks == 0 {
		return nil
	}
	u.WaitSecs = roundTenth(u.WaitSecs)
	return &u
}

func roundTenth(f float64) float64 { return math.Round(f*10) / 10 }

// withLegLock runs run, holding the compose-leg lock first when pc is a
// leg; the result records that it held it and how long it waited, and
// whose run it waited for. Interrupted while waiting, the check is not run.
// A lock that cannot be taken at all (the folder unwritable) runs the
// check unlocked and says so in progress.
func withLegLock(ctx context.Context, root string, pc *PlannedCheck, progress io.Writer, run func() CheckResult) CheckResult {
	if !composeLeg(pc) {
		return run()
	}
	start := time.Now()
	holder := ""
	for {
		unlock, ok, err := tryLockFile(legLockPath)
		if err != nil {
			progressf(progress, "%s %s: the host's compose-leg lock cannot be taken (%v); running it unlocked", Stamp(), pc.Ref, err)
			return run()
		}
		if ok {
			waited := time.Since(start)
			if holder != "" {
				progressf(progress, "%s %s took the host's compose-leg lock after %.1fs", Stamp(), pc.Ref, waited.Seconds())
			}
			writeLegHolder(legLockPath, root, pc)
			r := run()
			unlock()
			r.LegLock = true
			if holder != "" {
				r.LegLockWaitSecs = roundTenth(waited.Seconds())
				r.LegLockHolder = holder
			}
			return r
		}
		if holder == "" {
			holder = readLegHolder(legLockPath)
			progressf(progress, "%s %s waits for the host's compose-leg lock, held by %s", Stamp(), pc.Ref, holder)
		}
		select {
		case <-ctx.Done():
			return CheckResult{
				Cmd: pc.Cmd, Exit: exitInterrupted, Tail: []string{"[ape] not run: interrupted waiting for the host's compose-leg lock"},
				LegLock: true, LegLockWaitSecs: roundTenth(time.Since(start).Seconds()), LegLockHolder: holder,
			}
		case <-time.After(legLockPoll):
		}
	}
}

// writeLegHolder names the lock's holder in its file — the pid, the
// check and the project — for a waiting run to report. Best effort.
func writeLegHolder(path, root string, pc *PlannedCheck) {
	_ = os.WriteFile(path+".holder", []byte(strconv.Itoa(os.Getpid())+" "+pc.Ref+" in "+root+"\n"), 0o666) //nolint:gosec // a note for other users' waiting runs
}

// readLegHolder is who holds the lock, as its holder wrote it.
func readLegHolder(path string) string {
	b, err := os.ReadFile(path + ".holder")
	if s := strings.TrimSpace(string(b)); err == nil && s != "" {
		return "pid " + s
	}
	return "another ape run"
}

// String is the gate report's line on the lock, "" when no check held
// it.
func (u *LegLockUse) String() string {
	if u == nil {
		return ""
	}
	if u.Waited == 0 {
		return fmt.Sprintf("%d compose leg(s) held the host's compose-leg lock; none waited", u.Checks)
	}
	return fmt.Sprintf("%d compose leg(s) held the host's compose-leg lock; %d waited %.1fs in all for another run's legs", u.Checks, u.Waited, u.WaitSecs)
}
