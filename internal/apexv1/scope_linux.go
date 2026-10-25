package apexv1

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Check scopes (v1.4, D498 (a), D561 B1, D562): on Linux each check runs in
// a systemd transient scope of its own, beside ape's in ape's slice
// (`systemd-run --user --scope --slice-inherit`), so its cgroup holds
// everything it starts — a setsid daemon and a double fork included, which
// its process group (F15) cannot see. What is left in the scope when the
// check's shell exits is ended and named; a check killed at a timeout is
// killed whole (cgroup.kill); the watchdog, inside the scope, kills the
// scope when ape ends. The scope's memory.peak is recorded. Where there is
// no systemd user manager (a container, CI) the process group stays the
// reaping, and the result says which (reaping: scope|group). Containers
// are not in the scope (a container runtime runs them in its own units,
// T8): they are the check's by its compose project (checkinfra.go).

// systemdRun is the systemd-run a scope is started with; "" when there is
// none, or scopes do not work here (scopeSupport).
var (
	scopeOnce   sync.Once
	systemdRun  string
	scopeParent string
	scopeSeq    atomic.Int64
)

// checkScopes is how checks are reaped: "auto" (a scope where one works,
// else the process group) or "group" (the process group alone). A var so
// tests can choose.
var checkScopes = "auto"

// scopeSupport is the systemd-run binary and the cgroup a check's scope is
// created in (ape's own cgroup's parent: the slice --slice-inherit names),
// or "" when scopes cannot be used: no systemd-run, no cgroup v2, no user
// manager answering. Probed once per ape command (about 15 ms).
func scopeSupport() (string, string) {
	if checkScopes != "auto" {
		return "", ""
	}
	scopeOnce.Do(func() {
		path, err := exec.LookPath("systemd-run")
		if err != nil {
			return
		}
		own, ok := ownCgroup()
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// The probe runs with the flags every check scope uses: a systemd
		// without --expand-environment (before 254) would rewrite each
		// check's $VAR and $$, so it gets no scopes.
		probe := exec.CommandContext(ctx, path, append(scopeFlags, "--", "sh", "-c", `[ "$1" = '$x' ]`, "probe", "$x")...)
		if probe.Run() != nil {
			return
		}
		systemdRun, scopeParent = path, filepath.Join("/sys/fs/cgroup", filepath.Dir(own))
	})
	return systemdRun, scopeParent
}

// ownCgroup is this process's cgroup v2 path ("/user.slice/…/x.scope").
func ownCgroup() (string, bool) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok && rest != "" && rest != "/" {
			return rest, true
		}
	}
	return "", false
}

// newScope is a fresh unit name for a check's scope and its cgroup
// directory; "" when scopes are not used.
func newScope() (unit, dir, runner string) {
	run, parent := scopeSupport()
	if run == "" {
		return "", "", ""
	}
	unit = fmt.Sprintf("ape-%d-%d", os.Getpid(), scopeSeq.Add(1))
	return unit, filepath.Join(parent, unit+".scope"), run
}

// scopeFlags are systemd-run's flags for a check's scope: the user
// manager, a scope in ape's slice, collected when done, quiet, and the
// command passed as it is — systemd-run expands $VAR and $$ otherwise.
var scopeFlags = []string{"--user", "--scope", "--slice-inherit", "--collect", "-q", "--expand-environment=no"}

// scopeArgv is the command line that runs argv in unit's scope.
func scopeArgv(runner, unit string, argv []string) []string {
	return append(append(append([]string{runner}, scopeFlags...), "--unit="+unit, "--"), argv...)
}

// scopePIDs are the processes in the scope's cgroup.
func scopePIDs(dir string) []int {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil
	}
	var pids []int
	for f := range strings.FieldsSeq(string(data)) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// scopeMemoryPeak is the scope's memory.peak in bytes, 0 when unread.
func scopeMemoryPeak(dir string) int64 {
	data, err := os.ReadFile(filepath.Join(dir, "memory.peak"))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n
}

// scopeCPU is the CPU time the scope's processes used (cpu.stat
// usage_usec, which cgroup v2 keeps without the cpu controller).
func scopeCPU(dir string) (time.Duration, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return 0, false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return time.Duration(n) * time.Microsecond, err == nil
		}
	}
	return 0, false
}

// scopeKill kills every process of the scope at once.
func scopeKill(dir string) bool {
	return os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0o600) == nil
}

// pidArgv is a live process's command line, nil when it is gone.
func pidArgv(pid int) []string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
}
