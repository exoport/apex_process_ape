package apexv1

import "encoding/json"

// Renamed codes (v1.3, D503/D510/D512). "Codes are stable" holds for a
// code's meaning; a code whose name came to read wrong is renamed, and its
// old name stays an alias for a while, because readers count the string: a
// diagnostic carries the new code in `code` and the old one in `alias`, the
// human line names both, and `ape project codes` lists the old name as a
// row of its own pointing at the new one. CodeAliases is the one source of
// truth: emission (Diagnostic.Alias, FormerlyNote) and the published list
// (PublishedCodes) both read it.

// CodeAlias is one renamed code: Code is the published code (its row in
// DiagnosticCodes carries its exit, action and meaning), Alias the name it
// had, kept in every emission until Until.
type CodeAlias struct {
	Code  string `json:"code"`
	Alias string `json:"alias"`
	Until string `json:"until"`
}

// CodeAliases lists every renamed code. A code may have more than one
// former name; a diagnostic's Alias names the first.
var CodeAliases = []CodeAlias{
	// "orchestrator" is the operator's /apex-orchestrator; the second run is
	// the loop's (apex-loop, the orchestrator's name before rc.7).
	{Code: CodeLoopRunning, Alias: "session.orchestrator-running", Until: "v1.5"},
	// "operator" is the person; the row is models.apex-orchestrator.
	{Code: CodeModelsOrchestratorRow, Alias: "models.operator-row", Until: "v1.5"},
	// The check kind (live) is named (full) (D508).
	{Code: CodeCheckFullSuggested, Alias: "check.live-suggested", Until: "v1.5"},
}

// AliasOf is code's former name, "" when code was never renamed.
func AliasOf(code string) string {
	for _, a := range CodeAliases {
		if a.Code == code {
			return a.Alias
		}
	}
	return ""
}

// AliasesOf is every former name of code.
func AliasesOf(code string) []string {
	var out []string
	for _, a := range CodeAliases {
		if a.Code == code {
			out = append(out, a.Alias)
		}
	}
	return out
}

// CanonicalCode is the published code for code: the new name for a
// former one (as an older ape, a daemon say, still sends it), else code.
func CanonicalCode(code string) string {
	for _, a := range CodeAliases {
		if a.Alias == code {
			return a.Code
		}
	}
	return code
}

// FormerlyNote is what a human line appends after code: "; formerly
// <alias> (deprecated)" for a renamed code, so the old string still
// appears and says it is going (v1.4, D526: renamed in v1.3, warned in
// v1.4, removed no earlier than v1.5), else "".
func FormerlyNote(code string) string {
	if a := AliasOf(code); a != "" {
		return "; formerly " + a + " (deprecated)"
	}
	return ""
}

// WithAlias is d with Alias filled from its code (CodeAliases), unless set.
func (d Diagnostic) WithAlias() Diagnostic {
	if d.Alias == "" {
		d.Alias = AliasOf(d.Code)
	}
	return d
}

// MarshalJSON fills alias from the code, so every --json emission of a
// renamed code carries its former name, whichever path built it.
func (d Diagnostic) MarshalJSON() ([]byte, error) {
	type plain Diagnostic
	return json.Marshal(plain(d.WithAlias()))
}

// PublishedCode is a row of `ape project codes`: a CodeDoc, with Aliases
// (the code's former names) on a renamed code's row, and RenamedTo on a
// former name's own row, whose exit and action are the new code's.
type PublishedCode struct {
	CodeDoc
	Aliases   []string `json:"aliases,omitempty"`
	RenamedTo string   `json:"renamed_to,omitempty"`
	// Deprecated marks a former name's row (v1.4, D526): readers that
	// count the old string move to the new one before it goes.
	Deprecated bool `json:"deprecated,omitempty"`
}

// PublishedCodes is DiagnosticCodes as `ape project codes` lists it: each
// renamed code's row followed by one row per former name.
func PublishedCodes() []PublishedCode {
	out := make([]PublishedCode, 0, len(DiagnosticCodes)+len(CodeAliases))
	for _, c := range DiagnosticCodes {
		out = append(out, PublishedCode{CodeDoc: c, Aliases: AliasesOf(c.Code)})
		for _, a := range CodeAliases {
			if a.Code != c.Code {
				continue
			}
			out = append(out, PublishedCode{
				Code: a.Alias, Exit: c.Exit, Action: c.Action,
				Meaning:    "renamed to `" + a.Code + "`; deprecated since v1.4 and kept as an alias until " + a.Until + ": a diagnostic carries `" + a.Code + "` in code and this name in alias",
				RenamedTo:  a.Code,
				Deprecated: true,
			})
		}
	}
	return out
}
