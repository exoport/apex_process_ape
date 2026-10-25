package apexv1

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What a check gets besides its process group and scope (v1.4, D498 (a),
// D561 B1, T3, T5, T1, T8; D563): isolation and cleanup a project need not
// write, and the cost of what it starts.
//
//   - TMPDIR: a directory of its own, /tmp/ape-<uid>/<id> (mode 0700),
//     removed when it ends. Short on purpose: a unix socket a test binds
//     under it must fit in 108 bytes, which a directory under the project
//     did not (T5). Its size is recorded (tmp_bytes).
//   - Processes that hold that directory — in their command line, working
//     directory or environment — are the check's even outside its scope:
//     snap's Chromium moves itself into a scope of its own and keeps using
//     the check's TMPDIR as its profile (T1). They are ended and named with
//     the leftovers. The directory is unique to the check, so the match
//     cannot reach another check's.
//   - COMPOSE_PROJECT_NAME=apex-<project>-<id>: a `docker compose` the
//     check runs gets a project of its own rather than the person's dev
//     stack (atomix, D493). It overrides the project's `name:` and `.env`
//     (T3: compose ranks the variable above both); only `-p` opts out (Q7).
//   - With a container runtime (docker on PATH, a daemon answering):
//     containers the check created are listed against those that existed
//     before it. Those of its compose project are removed, with their
//     networks and volumes; testcontainers' are Ryuk's; any other new one is
//     reported, never removed (OQ-4). Each one's lifetime and memory.peak
//     (its own docker-<id>.scope, outside the check's: T8) are recorded.
//   - A red check whose output says its port is taken ("address already in
//     use", "port is already allocated", EADDRINUSE) is unrun, port-held,
//     when the port's holder is outside the check — the person's dev stack,
//     another check — never red for the product; held by the check itself
//     it stays red.

// ContainerUse is a container a check created.
type ContainerUse struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Image string `json:"image,omitempty"`
	// Secs is how long it existed, up to the check's end; MemoryPeak its
	// cgroup's memory.peak in bytes when it could be read.
	Secs       float64 `json:"secs"`
	MemoryPeak int64   `json:"memory_peak,omitempty"`
	// Owner is "compose" (the check's own project: removed),
	// "testcontainers" (Ryuk removes it) or "" (reported, left).
	Owner   string `json:"owner,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}

// shortContainerID is how much of a container id a result keeps (docker
// ps's own short form); holderWidth how much of a holder's command line.
const (
	shortContainerID = 12
	holderWidth      = 80
)

// checkInfra is one check's TMPDIR, compose project and container list.
type checkInfra struct {
	id      string
	tmp     string
	compose string
	docker  bool
	before  map[string]bool
	start   time.Time
}

// checkTmpRoot is where checks' TMPDIRs are made: under ape's own TMPDIR
// while a check's directory there stays within maxCheckTmp, else /tmp. A
// var so tests can choose.
var checkTmpRoot = tmpRoot(os.TempDir())

// maxCheckTmp bounds a check's TMPDIR: a unix socket a test binds under it
// (t.TempDir()/<Test><n>/001/app.sock adds about 32 bytes) must fit in 107.
const maxCheckTmp = 60

func tmpRoot(base string) string {
	root := filepath.Join(base, "ape-"+strconv.Itoa(os.Getuid()))
	if len(root)+len("/4194304-abcdef") > maxCheckTmp && runtime.GOOS != "windows" {
		return filepath.Join(string(filepath.Separator)+"tmp", "ape-"+strconv.Itoa(os.Getuid()))
	}
	return root
}

// composeNameRe is what a compose project name may hold.
var composeNameRe = regexp.MustCompile(`[^a-z0-9_-]+`)

// newCheckInfra prepares a check run from dir (the project root).
func newCheckInfra(ctx context.Context, dir string) *checkInfra {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	ci := &checkInfra{id: hex.EncodeToString(b), start: time.Now()}
	if err := os.MkdirAll(checkTmpRoot, 0o700); err == nil {
		staleTmpOnce.Do(removeStaleTmp)
		t := filepath.Join(checkTmpRoot, strconv.Itoa(os.Getpid())+"-"+ci.id)
		if os.Mkdir(t, 0o700) == nil {
			ci.tmp = t
		}
	}
	proj := strings.Trim(composeNameRe.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-_")
	if proj == "" {
		proj = "project"
	}
	ci.compose = "apex-" + proj + "-" + ci.id
	if dockerAvailable(ctx) {
		ci.docker = true
		ci.before = map[string]bool{}
		for _, c := range dockerContainers(ctx) {
			ci.before[c.id] = true
		}
	}
	return ci
}

// staleTmpOnce removes, once per ape command, the TMPDIRs whose ape is
// gone: one killed mid-check (SIGKILL) could not remove its own.
var staleTmpOnce sync.Once

// removeStaleTmp removes checkTmpRoot's <pid>-<id> directories whose pid
// no longer runs.
func removeStaleTmp() {
	entries, err := os.ReadDir(checkTmpRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		pidPart, _, ok := strings.Cut(e.Name(), "-")
		pid, err := strconv.Atoi(pidPart)
		if !ok || err != nil || pid == os.Getpid() || processAlive(pid) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(checkTmpRoot, e.Name()))
	}
}

// discard removes the TMPDIR of a check that never started.
func (ci *checkInfra) discard() {
	if ci.tmp != "" {
		_ = os.RemoveAll(ci.tmp)
	}
}

// env is what the check's environment gets: its TMPDIR and compose project.
func (ci *checkInfra) env() []string {
	var out []string
	if ci.tmp != "" {
		out = append(out, "TMPDIR="+ci.tmp)
	}
	return append(out, "COMPOSE_PROJECT_NAME="+ci.compose)
}

// finish is everything after the check and its leftovers ended: the
// TMPDIR's holders, the containers, the TMPDIR itself. It fills r.
func (ci *checkInfra) finish(ctx context.Context, r *CheckResult, inCheck func(pid int) bool) {
	ctx = context.WithoutCancel(ctx)
	r.Compose = ci.compose
	if ci.tmp != "" {
		r.Leftovers = append(r.Leftovers, endTmpHolders(ci.tmp, inCheck)...)
	}
	if ci.docker {
		r.Containers = ci.containers(ctx)
	}
	if ci.tmp != "" {
		r.TmpBytes = dirBytes(ci.tmp)
		_ = os.RemoveAll(ci.tmp)
	}
}

// dirBytes is the size of the regular files under dir.
func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// Docker, through its CLI (OQ-2).

var (
	dockerOnce sync.Once
	dockerOK   bool
)

// dockerAvailable reports a docker CLI whose daemon answers, probed once
// per ape command.
func dockerAvailable(ctx context.Context) bool {
	dockerOnce.Do(func() {
		if _, err := exec.LookPath("docker"); err != nil {
			return
		}
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		dockerOK = exec.CommandContext(c, "docker", "version", "--format", "{{.Server.Version}}").Run() == nil
	})
	return dockerOK
}

type dockerContainer struct {
	id, name, image, project, testcontainers, created string
}

// dockerContainers lists every container, running or not.
func dockerContainers(ctx context.Context) []dockerContainer {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "docker", "ps", "-a", "--no-trunc", "--format",
		`{{.ID}}	{{.Names}}	{{.Image}}	{{.Label "com.docker.compose.project"}}	{{.Label "org.testcontainers"}}	{{.CreatedAt}}`).Output()
	if err != nil {
		return nil
	}
	var cs []dockerContainer
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 6 || f[0] == "" {
			continue
		}
		cs = append(cs, dockerContainer{id: f[0], name: f[1], image: f[2], project: f[3], testcontainers: f[4], created: f[5]})
	}
	return cs
}

// containers are the containers the check created, its compose project's
// removed (with that project's networks and volumes).
func (ci *checkInfra) containers(ctx context.Context) []ContainerUse {
	var out []ContainerUse
	var remove []string
	for _, c := range dockerContainers(ctx) {
		if ci.before[c.id] {
			continue
		}
		u := ContainerUse{ID: c.id[:min(shortContainerID, len(c.id))], Name: c.name, Image: c.image}
		if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", c.created); err == nil {
			u.Secs = float64(time.Since(t).Round(100*time.Millisecond)) / float64(time.Second)
		}
		u.MemoryPeak = containerMemoryPeak(ctx, c.id)
		switch {
		case c.project == ci.compose:
			u.Owner, u.Removed = "compose", true
			remove = append(remove, c.id)
		case c.testcontainers == "true":
			u.Owner = "testcontainers"
		}
		out = append(out, u)
	}
	c, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if len(remove) > 0 {
		rm := exec.CommandContext(c, "docker", append([]string{"rm", "-f", "-v"}, remove...)...) //nolint:gosec // container ids docker itself listed
		if rm.Run() != nil {
			for i := range out {
				out[i].Removed = false
			}
		}
	}
	label := "label=com.docker.compose.project=" + ci.compose
	for _, kind := range []string{"network", "volume"} {
		ids, err := exec.CommandContext(c, "docker", kind, "ls", "-q", "--filter", label).Output() //nolint:gosec // kind is network or volume, label ape's own name
		if err == nil && len(strings.Fields(string(ids))) > 0 {
			_ = exec.CommandContext(c, "docker", append([]string{kind, "rm"}, strings.Fields(string(ids))...)...).Run() //nolint:gosec // ids docker itself listed
		}
	}
	return out
}

// containerMemoryPeak is a running container's cgroup memory.peak.
func containerMemoryPeak(ctx context.Context, id string) int64 {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "docker", "inspect", "--format", "{{.State.Pid}}", id).Output()
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return 0
	}
	return procCgroupMemoryPeak(pid)
}

// Ports.

var (
	portTakenRe = regexp.MustCompile(`(?i)address already in use|port is already allocated|EADDRINUSE`)
	portNumRe   = regexp.MustCompile(`:(\d{2,5})\b`)
)

// takenPort is the port a red check's output says was taken, 0 when it
// says none.
func takenPort(out string) int {
	for line := range strings.SplitSeq(out, "\n") {
		if !portTakenRe.MatchString(line) {
			continue
		}
		for _, m := range portNumRe.FindAllStringSubmatch(line, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n < 65536 {
				return n
			}
		}
	}
	return 0
}

// portHolder names who holds port outside the check: a process (when the
// check holds it, "" — it is the product's own), else a container
// publishing it; "" when nobody can be named.
func portHolder(ctx context.Context, port int, docker bool, inCheck func(pid int) bool) string {
	if pid := listenerPID(port); pid > 0 {
		if inCheck(pid) {
			return ""
		}
		return strconv.Itoa(pid) + " " + clip(strings.Join(pidArgv(pid), " "), holderWidth)
	}
	if docker {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(c, "docker", "ps", "--filter", "publish="+strconv.Itoa(port), "--format", "{{.Names}}").Output() //nolint:gosec // a port number
		if err == nil {
			if names := strings.Fields(string(out)); len(names) > 0 {
				return "container " + strings.Join(names, ", ")
			}
		}
	}
	return ""
}
