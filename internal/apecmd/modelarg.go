package apecmd

import (
	"fmt"
	"os"

	"github.com/exoport/apex_process_ape/internal/cost"
)

// resolveModelArg canonicalizes a user-supplied `--model` value before it
// reaches claude, and warns when ape cannot attribute it to a known family.
//
// Callers write model names by hand, and the same model has several
// reasonable spellings: `sonnet`, `Sonnet`, `claude-sonnet`,
// `claude-sonnet-5`, `sonnet-5`, `claude-sonnet-4.6`. All of them resolve
// here; only a genuine typo falls through with a warning.
//
// A bare family word resolves to that family's current generation, so
// `--model sonnet` spawns a concrete id rather than deferring to claude.
//
// The warning is not a rejection. Claude Code may know models this ape
// binary does not — a model released after this binary was built must not
// be blocked by ape's own table — so the canonical form is passed through
// either way and claude gives the authoritative verdict.
func resolveModelArg(raw string) string {
	canonical, recognized := cost.CanonicalModelArg(raw)
	if canonical == "" {
		return ""
	}
	if !recognized {
		fmt.Fprintf(os.Stderr,
			"⚠ model %q is not one ape recognizes — passing it to claude unchanged.\n"+
				"  Accepted: a bare family (sonnet, opus, haiku) for its current generation,\n"+
				"  or an explicit id (sonnet-5, claude-sonnet-5, opus[1m]).\n",
			raw)
	}
	return canonical
}
