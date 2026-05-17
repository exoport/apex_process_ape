package runlog

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EnsureGitignore looks for `_output/` in <projectRoot>/.gitignore. If
// absent and stdin is a TTY (askPrompt provided), it asks the user;
// otherwise it warns to stderr but does not modify the file. PLAN-5
// / C6 — first-run policy.
//
// Returns true if the line was appended (or already present), false
// if the user declined or non-TTY warn path was taken.
func EnsureGitignore(projectRoot string, askPrompt func(question string) bool, warnTo io.Writer) (bool, error) {
	path := filepath.Join(projectRoot, ".gitignore")
	contained, err := gitignoreContains(path, "_output/")
	if err != nil {
		return false, err
	}
	if contained {
		return true, nil
	}
	if askPrompt == nil {
		if warnTo != nil {
			fmt.Fprintln(warnTo, "warning: _output/ is not in .gitignore; run artefacts will be tracked by git")
		}
		return false, nil
	}
	if !askPrompt("Append `_output/` to .gitignore? [y/N]: ") {
		return false, nil
	}
	return true, appendGitignoreLine(path, "_output/")
}

func gitignoreContains(path, line string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == line {
			return true, nil
		}
	}
	return false, sc.Err()
}

func appendGitignoreLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	// Ensure trailing newline on the previous line; reading the
	// existing tail just to add a leading \n is overkill, write
	// `\n_output/\n` for safety on every append.
	if _, err := f.WriteString("\n" + line + "\n"); err != nil {
		return err
	}
	return nil
}
