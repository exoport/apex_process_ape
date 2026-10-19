package apecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"
)

// The project-pinned toolchain: a bare `ape` honours the project's bingo pin,
// the way `go` honours a module's `toolchain` line.
//
// The framework's skills call a bare `ape`, so the shell picks whatever is
// first on PATH. Sessions ape spawns are covered by internal/selfpath, but a
// Claude session the user opens directly is not: observed live, a project
// whose .bingo/ape.mod pinned 0.0.66 ran 0.7.0 — the `-l` link of another
// project's pin — and nothing said so. Every invocation now looks for
// `.bingo/ape.mod` from the working directory up to the repo root, and when
// it pins a different ape, hands the command to that binary unchanged. When
// it cannot, it refuses: running anyway is the silent mismatch this exists
// to end.

const (
	// envToolchain=local skips the pin, to try a different ape on purpose.
	envToolchain = "APE_TOOLCHAIN"
	// envPinned is set on a swap to the pin it honoured. A binary that finds
	// its own project's pin there runs, whatever version it reports, so no
	// build can swap into itself forever; a session that moves to a project
	// with a different pin is still checked.
	envPinned = "APE_PINNED"
	// envPinnedFrom is set on a swap to the version that handed over, so
	// `ape version` can say how it was reached.
	envPinnedFrom = "APE_PINNED_FROM"

	apeModuleRoot = "github.com/exoport/apex_process_ape"

	// exitCodeApePinUnusable: the project pins an ape that cannot run here.
	exitCodeApePinUnusable = 13
)

// apePin is a project's `.bingo/ape.mod`.
type apePin struct {
	root    string // the directory holding .bingo/
	version string // vX.Y.Z, as bingo pinned it
}

// findApePin finds the pin governing dir: the first `.bingo/ape.mod` from
// dir up to the repo root (the nearest directory holding .git — a
// directory, or a worktree's file). Outside any repo there is no pin: a
// `.bingo/ape.mod` above it — `~/_dev/.bingo`, which installs a launcher —
// pins a tool for that directory, not for every directory below it.
// An absent pin is (nil, nil); a pin that cannot be read is an error,
// because guessing past it is the silent fallback this check exists to
// remove.
func findApePin(dir string) (*apePin, error) {
	root, ok := repoRoot(dir)
	if !ok {
		return nil, nil //nolint:nilnil // not in a repo: nothing is pinned
	}
	for {
		mod := filepath.Join(dir, ".bingo", "ape.mod")
		data, err := os.ReadFile(mod)
		switch {
		case err == nil:
			v, perr := parseApePin(mod, data)
			if perr != nil {
				return nil, perr
			}
			return &apePin{root: dir, version: v}, nil
		case !errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("read %s: %w", mod, err)
		}
		if dir == root {
			return nil, nil //nolint:nilnil // no pin is the common case, not an error
		}
		dir = filepath.Dir(dir)
	}
}

// repoRoot is the nearest directory at or above dir holding .git.
func repoRoot(dir string) (string, bool) {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func parseApePin(path string, data []byte) (string, error) {
	f, err := modfile.ParseLax(path, data, nil)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	for _, r := range f.Require {
		if r.Mod.Path == apeModuleRoot {
			return r.Mod.Version, nil
		}
	}
	return "", fmt.Errorf("%s requires no %s", path, apeModuleRoot)
}

// sameApeVersion compares a running version with a pin, ignoring the v.
func sameApeVersion(running, pin string) bool {
	return trimV(strings.TrimSpace(running)) == trimV(strings.TrimSpace(pin))
}

// pinDecision is what the startup check concluded.
type pinDecision struct {
	run    bool   // run in this process
	exec   string // or hand the command to this binary
	refuse error  // or stop with this
	note   string // printed to stderr when running here despite a pin
}

// pinCheckInput is everything the decision reads, so it is testable
// without a real PATH, GOBIN or process.
type pinCheckInput struct {
	cwd       string
	running   string // this binary's Version
	self      string // this binary's resolved path
	env       func(string) string
	verb      string // the cobra command path, "ape framework update"
	help      bool   // -h/--help was asked for
	updateApe bool   // `framework update --update-ape`
	locate    func(version string) (string, bool)
	floorFrom func(root string) (pinFloor, error)
}

// pinFloor is the least ape a project's pin may name, and what sets it,
// worded for the refusal.
type pinFloor struct {
	version string
	by      string
}

// recoveryVerbs still run when the pin cannot: they are how a project
// finds out what is wrong and moves its pin. They run in this process,
// with a note, rather than refusing.
var recoveryVerbs = map[string]bool{
	"ape":               true, // bare `ape`, `ape --version`
	"ape version":       true,
	"ape doctor":        true,
	"ape help":          true,
	"ape update":        true,
	verbFrameworkUpdate: true,
}

// verbFrameworkUpdate is the command path of `ape framework update`.
const verbFrameworkUpdate = "ape framework update"

// verbHookPreTool is the command path of `ape hook pre-tool`: a pin that
// refuses it exits 2, so the guard fails closed. stop and session-start
// keep 13, which Claude Code does not read as a block — a refused stop
// hook that blocked would loop.
const verbHookPreTool = "ape hook pre-tool"

// verbProjectLift is the command path of `ape project lift`. It is exempt
// from the pin (L1): it runs on a v0 project, whose pin names a v0 ape
// that has no `project` command — every rehearsal stopped there. The lift
// runs on the binary invoked, says which in its output, and moves the pin
// to it (or prints the command that does).
const verbProjectLift = "ape project lift"

func decidePin(in pinCheckInput) pinDecision {
	if strings.EqualFold(in.env(envToolchain), "local") || in.verb == verbProjectLift {
		return pinDecision{run: true}
	}
	pin, err := findApePin(in.cwd)
	if err != nil {
		return in.cannot(err, "fix or remove it, or run with "+envToolchain+"=local")
	}
	if pin == nil || in.env(envPinned) == pin.version {
		return pinDecision{run: true}
	}
	remedy := func(v string) string {
		return "bingo get -l " + apeModulePath + "@" + v + "   (in " + pin.root + ", then commit .bingo/)"
	}

	// A pin below the installed framework's floor would run a binary the
	// framework's skills cannot use. Honouring it is not an option.
	if floor, ferr := in.floorFrom(pin.root); ferr == nil &&
		framework.CompareApeVersion(pin.version, floor.version) == framework.ApeVersionBelow {
		return in.cannot(fmt.Errorf("this project pins ape %s (.bingo/ape.mod), below %s", pin.version, floor.by),
			"move the pin forward: "+remedy("v"+trimV(floor.version)))
	}

	// `ape update` acts on the binary that runs it. Handing it to a pin
	// older than 0.8.5 would overwrite the pin's own versioned file.
	// It is the machine's launcher that `ape update` updates; it says what
	// to do about a pin that is behind once it knows the newest version.
	if in.verb == "ape update" {
		return pinDecision{run: true}
	}
	// `framework update --update-ape` likewise runs on the launcher: it
	// moves the pin, then re-runs on the pinned ape without the flag — a
	// pin older than 0.9.0 does not know it.
	if in.verb == verbFrameworkUpdate && in.updateApe {
		return pinDecision{run: true}
	}
	if sameApeVersion(in.running, pin.version) {
		return pinDecision{run: true}
	}
	bin, ok := in.locate(pin.version)
	if !ok {
		return in.cannot(fmt.Errorf("this project pins ape %s (.bingo/ape.mod), and ape-%s is not installed",
			pin.version, pin.version), "install it: bingo get ape   (in "+pin.root+"), or move the pin: ape update --pin")
	}
	if bin == in.self {
		return pinDecision{run: true}
	}
	return pinDecision{exec: bin}
}

// cannot refuses, except for the recovery verbs and help, which run here
// and say why.
func (in pinCheckInput) cannot(err error, remedy string) pinDecision {
	if recoveryVerbs[in.verb] || in.help {
		return pinDecision{run: true, note: fmt.Sprintf("ape: %v — running ape %s instead, for this command only. %s",
			err, in.running, remedy)}
	}
	return pinDecision{refuse: fmt.Errorf(
		"%w\n  %s\n  (%s=local runs this ape regardless)", err, remedy, envToolchain)}
}

// locatePinnedApe finds `ape-<version>` where bingo puts it: GOBIN, else
// the first GOPATH entry's bin, else ~/go/bin. `go env` is asked only when
// none of those has it, so the common case spawns nothing.
func locatePinnedApe(ctx context.Context, version string) (string, bool) {
	name := "ape-" + version
	if runtime.GOOS == goosWindows {
		name += ".exe"
	}
	var dirs []string
	if d := os.Getenv("GOBIN"); d != "" {
		dirs = append(dirs, d)
	}
	if p := os.Getenv("GOPATH"); p != "" {
		dirs = append(dirs, filepath.Join(filepath.SplitList(p)[0], "bin"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "go", "bin"))
	}
	if bin, ok := firstFile(dirs, name); ok {
		return bin, true
	}
	if _, err := exec.LookPath("go"); err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "env", "GOBIN", "GOPATH").Output()
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	dirs = dirs[:0]
	if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
		dirs = append(dirs, strings.TrimSpace(lines[0]))
	}
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
		dirs = append(dirs, filepath.Join(filepath.SplitList(strings.TrimSpace(lines[1]))[0], "bin"))
	}
	return firstFile(dirs, name)
}

func firstFile(dirs []string, name string) (string, bool) {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			if r, err := filepath.EvalSymlinks(p); err == nil {
				p = r
			}
			return p, true
		}
	}
	return "", false
}

// v1LayoutApeFloor is the least ape an APEX v1 project runs on.
const v1LayoutApeFloor = "1.0.0"

// installedFloor is the installed framework's min_ape_version
// (_apex/ape-commands.yaml), else, in an APEX v1 project, 1.0.0 (R33): a
// lifted project has no ape-commands.yaml until the payload lands, and
// its v0 pin then ran a v0 ape — every commit refused with `unknown
// command "hook"`, and the pre-tool guard off — instead of refusing.
func installedFloor(root string) (pinFloor, error) {
	m, err := framework.LoadApeCommands(root)
	switch {
	case err != nil:
		return pinFloor{}, errors.New("no floor")
	case m != nil:
		return pinFloor{version: m.MinApeVersion, by: "the installed framework's min_ape_version " + m.MinApeVersion}, nil
	case isV1Layout(root):
		return pinFloor{version: v1LayoutApeFloor, by: v1LayoutApeFloor + ", the least an APEX v1 project (_apex/state/ or " +
			apexv1.DefaultDevFolder + "/project.md) runs on"}, nil
	}
	return pinFloor{}, errors.New("no floor")
}

// isV1Layout is apexv1's v1 detection at root; a config it cannot read
// leaves the default development folder.
func isV1Layout(root string) bool {
	p, err := apexv1.Open(root)
	if err != nil {
		p = &apexv1.Project{Root: root, DevRel: apexv1.DefaultDevFolder}
	}
	return p.IsV1()
}

// honourApePin runs the check for this process: nil to carry on here, or
// an error to stop with. A swap does not return on Unix; on Windows it
// returns the pinned binary's exit status as an already-reported error.
func honourApePin(root *cobra.Command, args []string, stderr io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil //nolint:nilerr // no working directory, no project to pin: run as before
	}
	verb := "ape"
	if c, _, ferr := root.Find(args); ferr == nil && c != nil {
		verb = c.CommandPath()
	}
	help := false
	for _, a := range args {
		if a == "-h" || a == "--help" {
			help = true
		}
		if a == "--" {
			break
		}
	}
	self, _ := os.Executable()
	if r, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = r
	}
	d := decidePin(pinCheckInput{
		cwd: cwd, running: Version, self: self, env: os.Getenv, verb: verb, help: help,
		updateApe: verb == verbFrameworkUpdate && hasUpdateApe(args),
		locate:    func(v string) (string, bool) { return locatePinnedApe(context.Background(), v) },
		floorFrom: installedFloor,
	})
	switch {
	case d.refuse != nil && verb == verbHookPreTool:
		// Claude Code reads only exit 2 from a PreToolUse hook as a
		// block: 13 would let every tool call through (R33).
		return usageErrExit(ExitUsage, d.refuse)
	case d.refuse != nil:
		return usageErrExit(exitCodeApePinUnusable, d.refuse)
	case d.exec != "":
		pin, _ := findApePin(cwd)
		env := append(os.Environ(), envPinned+"="+pin.version, envPinnedFrom+"="+Version)
		return execPinned(d.exec, args, env)
	}
	if d.note != "" {
		fmt.Fprintln(stderr, d.note)
	}
	return nil
}
