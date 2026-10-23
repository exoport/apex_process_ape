package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// bingoPin is an ape binary that bingo installed: `<GOBIN>/ape-vX.Y.Z`,
// built from a project's `.bingo/ape.mod`.
//
// `ape update` must not touch one. On Linux os.Executable resolves
// symlinks, so from `~/go/bin/ape → ape-v0.8.3` a self-update wrote the
// new release INTO `ape-v0.8.3`: the file's name then lies about its
// version, the project's pin still names the old one, bingo never rebuilds
// it (it rebuilds only when ape.mod changes), and every other project
// pinned to that version through the same GOBIN silently runs the new
// binary. Detected by the binary's own name, because that is what the
// overwrite would falsify — whatever directory ape happens to run in.
type bingoPin struct {
	// path is the resolved binary, `<GOBIN>/ape-vX.Y.Z`.
	path string
	// linked is true when `<GOBIN>/ape` is a symlink to path — the pin was
	// made with `bingo get -l`, which is what keeps the bare `ape` name on
	// it. The remedy must carry -l then, or the bare name stays behind.
	linked bool
}

var bingoApeName = regexp.MustCompile(`^ape-v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\.exe)?$`)

// runningBingoPin reports whether THIS binary is a bingo pin.
func runningBingoPin() (bingoPin, bool) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return bingoPin{}, false
	}
	return detectBingoPin(exe)
}

func detectBingoPin(exe string) (bingoPin, bool) {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if !bingoApeName.MatchString(filepath.Base(exe)) {
		return bingoPin{}, false
	}
	p := bingoPin{path: exe}
	link := filepath.Join(filepath.Dir(exe), binaryName(runtime.GOOS))
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if target, err := filepath.EvalSymlinks(link); err == nil && target == exe {
			p.linked = true
		}
	}
	return p, true
}

// command is the bingo line that moves the pin to version ("" for latest).
func (p bingoPin) command(version string) string {
	at := "latest"
	if version != "" {
		at = "v" + trimV(version)
	}
	flag := ""
	if p.linked {
		flag = "-l "
	}
	return "bingo get " + flag + apeModulePath + "@" + at
}

// apeUpdateCommand is the command that gets this ape to version ("" for
// latest): `ape update`, or the bingo line when this binary is a pin.
func apeUpdateCommand(version string) string {
	if p, ok := runningBingoPin(); ok {
		return p.command(version)
	}
	return "ape update"
}

// apeUpdateRemediation says how to install a newer ape, in words.
func apeUpdateRemediation(version string) string {
	if p, ok := runningBingoPin(); ok {
		return fmt.Sprintf("This ape is pinned by bingo (%s). Move the pin in the project that owns it "+
			"and commit .bingo/: `%s`. Not `ape update`, which would overwrite %s with another version.",
			p.path, p.command(version), filepath.Base(p.path))
	}
	return "Run `ape update` to install the latest release."
}

// refuseBingoSelfUpdate stops `ape update` on a bingo pin, naming the
// command that does the same job without falsifying the pin.
func refuseBingoSelfUpdate(latest string) error {
	p, ok := runningBingoPin()
	if !ok {
		return nil
	}
	return fmt.Errorf("this ape is pinned by bingo (%s): `ape update` would write %s into that file, "+
		"so its name would lie about its version, the project's .bingo/ape.mod would still name the old "+
		"one, and every project pinned to it through the same GOBIN would silently run %s. Move the pin "+
		"instead, in the project that pins ape, and commit .bingo/:\n  %s",
		p.path, latest, latest, p.command(latest))
}

// goBinDir is where `go install` and bingo put binaries: $GOBIN, else the
// first $GOPATH's bin, as `go env` reports them ("" when go cannot say).
func goBinDir(ctx context.Context) string {
	for _, v := range []string{"GOBIN", "GOPATH"} {
		out, err := exec.CommandContext(ctx, "go", "env", v).Output()
		if err != nil {
			continue
		}
		if p := strings.TrimSpace(string(out)); p != "" {
			if v == "GOPATH" {
				p = filepath.Join(strings.Split(p, string(os.PathListSeparator))[0], "bin")
			}
			return p
		}
	}
	return ""
}

var bingoModVersion = regexp.MustCompile(`github\.com/bwplotka/bingo\s+(v\d+\.\d+\.\d+\S*)`)

// findBingo is the bingo that moves a project's pin (F16d): bingo on PATH;
// else the project's own pinned bingo, `$GOBIN/bingo-<version>` from its
// .bingo/bingo.mod — what its Variables.mk builds and runs; else
// `$GOBIN/bingo`. A project that pins bingo needs none on PATH (atomix's
// `ape update --pin` was refused for that). The error names every place
// looked.
func findBingo(ctx context.Context, root string) (string, error) {
	if p, err := exec.LookPath("bingo"); err == nil {
		return p, nil
	}
	gobin := goBinDir(ctx)
	var tried []string
	if gobin != "" {
		var names []string
		if data, err := os.ReadFile(filepath.Join(root, ".bingo", "bingo.mod")); err == nil {
			if m := bingoModVersion.FindSubmatch(data); m != nil {
				names = append(names, "bingo-"+string(m[1]))
			}
		}
		names = append(names, "bingo")
		for _, n := range names {
			if runtime.GOOS == goosWindows {
				n += ".exe"
			}
			p := filepath.Join(gobin, n)
			if isExecutable(p) {
				return p, nil
			}
			tried = append(tried, p)
		}
	}
	where := "bingo is not on PATH"
	if len(tried) > 0 {
		where += ", nor at " + strings.Join(tried, " or ")
	}
	return "", errors.New(where + " (install it: go install github.com/bwplotka/bingo@latest)")
}

// isExecutable reports a regular file with an execute bit (any file on
// Windows, where the name carries it).
func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == goosWindows || fi.Mode()&0o111 != 0
}
