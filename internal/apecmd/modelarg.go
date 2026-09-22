package apecmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/cost"
)

// modelFamilyWords is the bare family words `--model` accepts, read from
// the alias table rather than written down.
//
// The accepted set is DATA — `internal/cost/prices.yaml`'s `aliases:` — so
// a word added there is accepted immediately while any sentence naming the
// families is not updated by that edit. Four such sentences drifted: three
// `--model` flag descriptions and the unrecognized-model warning below all
// said "(sonnet, opus, haiku)" long after `fable` and `mythos` resolved,
// which told a caller that a word ape would honour was not a family.
// Derived here so the next alias cannot repeat it.
func modelFamilyWords() string {
	aliases := cost.FamilyAliases()
	words := make([]string, 0, len(aliases))
	for w := range aliases {
		words = append(words, w)
	}
	sort.Strings(words)
	return strings.Join(words, ", ")
}

// modelFlagUsage is the `--model` description every command sharing this
// flag prints, so the three cannot disagree with each other either.
func modelFlagUsage(prefix string) string {
	return prefix + " A bare family (" + modelFamilyWords() +
		") resolves to its current generation; sonnet-5 / claude-sonnet-5 / opus[1m] pin explicitly"
}

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
				"  Accepted: a bare family (%s) for its current generation,\n"+
				"  or an explicit id (sonnet-5, claude-sonnet-5, opus[1m]).\n",
			raw, modelFamilyWords())
	}
	return canonical
}
