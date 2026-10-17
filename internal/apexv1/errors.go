package apexv1

import (
	"strings"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
)

// The three failure kinds the contract distinguishes by exit code:
// a check that failed (1), a usage error (2) and a conflict (3). Each
// carries the one-line remediation the command prints.

// FailError is "the check ran and failed" — exit 1. Action, when set,
// overrides the code's published action for this one error (D49): a code
// whose next step depends on the case documents both in its meaning.
type FailError struct {
	Code     string
	Msg      string
	Findings []Finding
	Action   string
}

func (e *FailError) Error() string { return e.Msg }

// UsageError is a bad invocation — exit 2. Action as for FailError.
type UsageError struct{ Code, Msg, Action string }

func (e *UsageError) Error() string { return e.Msg }

// ConflictError is a conflict with another change or with the specs —
// exit 3, and nothing was written.
type ConflictError struct {
	Code      string
	Msg       string
	Conflicts []string
	Action    string
}

func (e *ConflictError) Error() string {
	if len(e.Conflicts) == 0 {
		return e.Msg
	}
	return e.Msg + ": " + strings.Join(e.Conflicts, "; ")
}

func writeFileAtomic(path, content string) error {
	return atomicfile.Write(path, []byte(content))
}
