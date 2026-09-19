package apecmd

import (
	"errors"
	"fmt"
	"io"
)

// resolvePromptFile reads a run prompt from a file or stdin, for the two
// commands that dispatch one.
//
// It exists because the escalation routes `ape change` prints hand the
// operator's own words to the next command, and argv cannot carry them:
// a shell argument runs command substitution on backticks and reads a
// leading dash as a flag. Every printed command therefore takes its
// prompt from a file, and these two commands are where those printed
// lines land.
//
// The validation is the same one `ape change` applies to a request,
// because the destination is the same: a claude REPL, typed into as
// keystrokes.
func resolvePromptFile(promptFile string, promptChanged, handoffSet bool, stdin io.Reader) (string, error) {
	if promptFile == "" {
		return "", nil
	}
	if promptChanged {
		return "", errors.New("--prompt-file and --prompt are mutually exclusive: pass the text once")
	}
	if handoffSet {
		return "", errors.New("--prompt-file and --handoff are mutually exclusive")
	}
	data, err := readTextInput(promptFile, stdin)
	if err != nil {
		return "", err
	}
	text, err := validateTypedLine(data, "the prompt")
	if err != nil {
		return "", fmt.Errorf("--prompt-file: %w", err)
	}
	return text, nil
}
