package apecmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

// Reading a body of text that will be TYPED INTO A PTY, and refusing the
// shapes that would not survive the typing.
//
// `ape change`'s request, and `ape task`/`ape pipeline`'s --prompt-file,
// all end up as literal keystrokes in a claude REPL. That is a narrower
// channel than a string:
//
//   - a newline mid-text submits the line early, so the rest of the
//     request is typed into a session that is already working on half of
//     it;
//   - a trailing backslash never submits at all — claude reads it as a
//     continuation and waits for more, and the run idles out;
//   - a control character is a key, not a letter. A tab opens completion,
//     an escape leaves the input line.
//
// None of these are hypothetical failures of a *bad* request: they are
// ordinary text a person might paste. So they are refused at the door,
// with the reason, rather than typed and diagnosed from a stuck pane an
// hour later.
//
// The reader exists for the same reason: argv cannot carry this text. A
// shell argument runs command substitution on backticks and reads a
// leading dash as a flag, which is why ape already bans argv bodies for
// `ape deferred add`. A file or stdin carries bytes.

// errStdinSpelling names the one stdin spelling ape accepts, so the
// message can say it rather than leaving the caller to guess.
var errStdinSpelling = errors.New(`stdin is spelled "-"`)

// readTextInput reads a body from path, or from stdin when path is "-".
//
// "-" is the spelling, and it is deliberately NOT the same convention as
// `ape deferred add --body-file`, which reads stdin when the flag is
// ABSENT and would open a file literally named "-". The commands that
// take a request cannot borrow that rule: they also accept the body as a
// positional argument for a human at a shell, so an absent flag already
// means "the text is in argv" and cannot also mean stdin.
func readTextInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read from stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (%w)", path, err, errStdinSpelling)
	}
	return data, nil
}

// validateTypedLine strips exactly one trailing newline and refuses text
// that cannot be typed into the REPL as a single line. `what` names the
// text in every message: a request on `ape change`, a prompt on
// `ape task` and `ape pipeline`.
//
// One trailing newline, because a file written by an editor has one and
// nobody means it as part of the request. A CRLF pair counts as that one
// newline: a request file written on Windows is not a malformed request,
// and leaving the \r behind would fail it as a control character, which
// is a true statement with a useless explanation.
//
// Everything after that is refused, including a second trailing newline:
// ape cannot know whether the blank line was meant, and silently eating
// it would edit the operator's words. The request is recorded
// byte-verbatim and lands in a commit trailer, so "nearly what they
// wrote" is not good enough.
func validateTypedLine(raw []byte, what string) (string, error) {
	text := string(raw)
	text = strings.TrimSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\r")

	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s is empty", what)
	}
	if i := strings.IndexAny(text, "\n\r"); i >= 0 {
		return "", fmt.Errorf("%s holds a line break at byte %d: it is typed into the REPL as "+
			"keystrokes, and a newline submits the line early, leaving the rest to be typed into "+
			"a session already working on half of it", what, i)
	}
	for i, r := range text {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%s holds a control character (%#U) at byte %d: "+
				"it is typed as a key, not a letter", what, r, i)
		}
	}
	if strings.HasSuffix(text, `\`) {
		return "", fmt.Errorf("%s ends in a backslash: claude reads it as a continuation and "+
			"never submits, so the run would idle out", what)
	}
	return text, nil
}
