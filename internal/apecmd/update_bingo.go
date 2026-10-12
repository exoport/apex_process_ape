package apecmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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
