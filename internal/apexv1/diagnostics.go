package apexv1

import (
	"errors"
	"regexp"
	"strings"
)

// Item 13: every --json failure carries diagnostics, each
// {severity, code, message, fix}, so an orchestrator or a code loop
// branches on a stable code rather than on prose. The codes are
// published (DiagnosticCodes, `ape project codes`); a test keeps the list
// and the code in step.

// Diagnostic is one machine-readable failure.
type Diagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Fix      string `json:"fix"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
}

// SeverityError is the only severity: v1 has no warning tier (backlog
// item 15) — what is worth reporting is worth failing on.
const SeverityError = "error"

// splitFix separates "<what> — <fix>", the shape every ape message takes,
// into its message and its remediation.
func splitFix(msg string) (message, fix string) {
	if before, after, ok := strings.Cut(msg, " — "); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	return msg, ""
}

// Diagnostics converts an apexv1 error (and its findings or conflicts)
// into diagnostics, the error itself first.
func Diagnostics(err error) []Diagnostic {
	var out []Diagnostic
	var fe *FailError
	var ue *UsageError
	var ce *ConflictError
	switch {
	case errors.As(err, &fe):
		out = append(out, newDiagnostic(codeOr(fe.Code, "error.unclassified"), fe.Msg))
		for _, f := range ClassifyFindings(fe.Findings) {
			out = append(out, Diagnostic{Severity: SeverityError, Code: f.Code, Message: f.Message, Fix: f.Fix, File: f.File, Line: f.Line})
		}
	case errors.As(err, &ue):
		out = append(out, newDiagnostic(codeOr(ue.Code, "usage.unclassified"), ue.Msg))
	case errors.As(err, &ce):
		out = append(out, newDiagnostic(codeOr(ce.Code, "conflict.unclassified"), ce.Msg))
		for _, c := range ce.Conflicts {
			out = append(out, Diagnostic{Severity: SeverityError, Code: codeOr(ce.Code, "conflict.unclassified"), Message: c})
		}
	case err != nil:
		out = append(out, newDiagnostic("error.unclassified", err.Error()))
	}
	return out
}

func newDiagnostic(code, msg string) Diagnostic {
	m, fix := splitFix(msg)
	return Diagnostic{Severity: SeverityError, Code: code, Message: m, Fix: fix}
}

func codeOr(code, fallback string) string {
	if code != "" {
		return code
	}
	return fallback
}

// findingRule classifies a finding: the first rule whose file kind (""
// for any) and message pattern match gives its code.
type findingRule struct {
	kind string
	re   *regexp.Regexp
	code string
}

func rule(kind, pattern, code string) findingRule {
	return findingRule{kind: kind, re: regexp.MustCompile(pattern), code: code}
}

// findingRules: one per validation rule. Ordered: specific before general.
var findingRules = []findingRule{
	rule("", `^missing — `, "file.missing"),
	rule("", `^project.md is .* lines, over the 150-line cap`, "project.too-long"),
	rule("", `^project.md is .* over the 8 KB cap`, "project.too-big"),
	rule("", "^missing `# <Project name>`", "project.no-title"),
	rule("project", "^missing `## ", "project.missing-section"),
	rule("project", "` is empty$", "project.empty-section"),
	rule("", `lines, over the 120-line cap`, "agents.too-long"),
	rule("", `must import the map`, "claude-md.no-import"),
	rule("", "first heading must be `# Roadmap`", "roadmap.title"),
	rule("", `^no milestones`, "roadmap.no-milestones"),
	rule("", `roadmap section must be`, "roadmap.bad-heading"),
	rule("", `^duplicate milestone`, "roadmap.duplicate-milestone"),
	rule("", "has no `Status:` line", "roadmap.no-status"),
	rule("", `only ape writes it`, "roadmap.bad-status"),
	rule("", "has no `Gate:`", "roadmap.no-gate"),
	rule("", "has no `Goal:`", "roadmap.no-goal"),
	rule("", `is not a number \(USD\)`, "roadmap.bad-budget"),
	rule("", `decision files are named`, "decision.bad-name"),
	rule("", "first heading must be `# ADR-", "decision.bad-title"),
	rule("", "^missing `Status:", "decision.no-status"),
	rule("", "^missing `Date:", "decision.no-date"),
	rule("", "^missing `Enforced by:", "decision.no-enforced-by"),
	rule("", `was accepted and its text has changed since`, "decision.accepted-edited"),
	rule("", `^Supersedes: .* names no decision`, "decision.bad-supersedes"),
	rule("decision", "^missing `## ", "decision.missing-section"),
	rule("", `capability folder must be a kebab-case slug`, "spec.bad-capability-name"),
	rule("", `capability folder without spec.md`, "spec.missing"),
	rule("", `over the 20 KB cap`, "spec.too-big"),
	rule("", "^missing `# <Capability title>`", "spec.no-title"),
	rule("", "^missing `Purpose:`", "spec.no-purpose"),
	rule("", `^duplicate requirement`, "spec.duplicate-requirement"),
	rule("", `^level-3 heading that is not`, "spec.bad-heading"),
	rule("", `^requirement has no name`, "spec.unnamed-requirement"),
	rule("", "has no `#### Scenario:`", "spec.no-scenario"),
	rule("", "has no `- check:", "spec.no-check"),
	rule("", "more than one `check:` line", "spec.multiple-checks"),
	rule("", "has an empty `check:`", "spec.empty-check"),
	rule("", "`check:` line outside any scenario", "spec.stray-check"),
	rule("", `^change.md is .* over the 8 KB cap`, "change.too-big"),
	rule("", `^design.md is`, "change.design-too-big"),
	rule("", `^no state.json beside it`, "change.no-state"),
	rule("", `^(no YAML frontmatter|frontmatter)`, "change.frontmatter"),
	rule("", `^change id must match`, "change.bad-id"),
	rule("", `^milestone .* is not in roadmap.md`, "change.unknown-milestone"),
	rule("", `^touches entry`, "change.bad-touches"),
	rule("", `^decisions entry`, "change.bad-decision-ref"),
	rule("", `has no file in decisions/`, "change.unknown-decision"),
	rule("", `^paths entry`, "change.bad-path-glob"),
	rule("", "^missing `# <title>`", "change.no-title"),
	rule("change", "^missing `## ", "change.missing-section"),
	rule("change", "` is empty$", "change.empty-section"),
	rule("", `^## Delta is empty`, "change.empty-delta"),
	rule("", `^text before the first delta block is not carried`, "change.dropped-text"),
	rule("", "^a REMOVED block carries only `Reason:`", "change.dropped-text"),
	rule("", `nearly matches the existing`, "change.added-near-duplicate"),
	rule("", `^the delta removes every requirement of`, "change.capability-emptied"),
	rule("", `^retire: lists`, "change.bad-retire"),
	rule("", "^`Purpose:` in an ADDED block names a new capability", "change.purpose-existing"),
	rule("", "^`Purpose:` is empty", "change.purpose-empty"),
	rule("", `^## Delta holds`, "change.bad-delta"),
	rule("", `^delta heading must be`, "change.bad-delta-heading"),
	rule("", `^delta capability .* is not a kebab-case slug`, "change.bad-delta-capability"),
	rule("", `is not in frontmatter touches`, "change.delta-not-touched"),
	rule("", `^delta has no requirement name`, "change.unnamed-delta"),
	rule("", `appears twice in the delta`, "change.duplicate-delta"),
	rule("", `already exists in specs/`, "change.added-exists"),
	rule("", `a MODIFIED block keeps every scenario`, "change.scenario-loss"),
	rule("", `^MODIFIED names`, "change.modified-missing"),
	rule("", `^REMOVED names`, "change.removed-missing"),
	rule("", "^REMOVED needs a `Reason:`", "change.removed-no-reason"),
	rule("", `^## Tasks has no tasks`, "change.no-tasks"),
	rule("", `^duplicate task`, "change.duplicate-task"),
	rule("", `^tasks have no checkboxes`, "change.task-checkbox"),
	rule("", `^task line must read`, "change.bad-task-line"),
	rule("", "^task .* has no `check:", "change.task-no-check"),
}

// UnclassifiedFinding is the code of a finding no rule matches — a
// test asserts no validation rule produces one.
const UnclassifiedFinding = "finding.unclassified"

// fileKind names the document a finding is about.
func fileKind(file string) string {
	base := file[strings.LastIndex(file, "/")+1:]
	switch {
	case base == "AGENTS.md":
		return "agents"
	case base == "CLAUDE.md":
		return "claude"
	case base == "project.md":
		return "project"
	case base == "roadmap.md":
		return "roadmap"
	case base == "change.md":
		return "change"
	case base == "design.md":
		return "design"
	case strings.Contains(file, "/decisions/"):
		return "decision"
	case base == "spec.md":
		return "spec"
	}
	return ""
}

// ClassifyFindings returns fs with Code and Fix filled in.
func ClassifyFindings(fs []Finding) []Finding {
	out := make([]Finding, len(fs))
	for i, f := range fs {
		f.Code = UnclassifiedFinding
		kind := fileKind(f.File)
		for _, r := range findingRules {
			if (r.kind == "" || r.kind == kind) && r.re.MatchString(f.Message) {
				f.Code = r.code
				break
			}
		}
		_, f.Fix = splitFix(f.Message)
		out[i] = f
	}
	return out
}
