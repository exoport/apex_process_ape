package pipeline

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// CommitMode discriminates the three shapes a step's `commit:` field
// can take in the pipeline YAML.
type CommitMode int

const (
	// CommitModeDefault — commit with the derived
	// `ape:<pipeline>/<stage>/<skill>` message. The default when the
	// field is omitted, set to `null`, or set to `true`.
	CommitModeDefault CommitMode = iota
	// CommitModeSkip — `commit: false` in the YAML. Step's output is
	// not committed even when the pipeline-level kill-switch is unset.
	CommitModeSkip
	// CommitModeExplicit — `commit: "some message"` in the YAML. Step
	// commits with the literal message provided by the spec author.
	CommitModeExplicit
)

// CommitDirective is the per-step commit configuration decoded from
// the pipeline spec's optional `commit:` field. The zero value
// (`CommitModeDefault`, empty Message) is the omit-this-field case and
// is what every step gets when its YAML doesn't mention `commit:`.
type CommitDirective struct {
	Mode    CommitMode
	Message string
}

// UnmarshalYAML accepts the bool-or-string shorthand documented in
// PLAN-4 § C1:
//
//	commit: true    → CommitModeDefault
//	commit: false   → CommitModeSkip
//	commit: "msg"   → CommitModeExplicit{Message: "msg"}
//	commit: ~       → CommitModeDefault
//	(field omitted) → CommitModeDefault   (handled by zero value)
//
// Any other shape (mapping, sequence, multi-line string, integer …)
// is a hard spec-load error.
func (c *CommitDirective) UnmarshalYAML(node *yaml.Node) error {
	if node == nil || node.Kind == 0 {
		c.Mode = CommitModeDefault
		return nil
	}
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("commit must be a bool or string scalar, got kind %d at line %d", node.Kind, node.Line)
	}
	switch node.Tag {
	case "!!null":
		c.Mode = CommitModeDefault
		return nil
	case "!!bool":
		var b bool
		if err := node.Decode(&b); err != nil {
			return fmt.Errorf("commit: decode bool at line %d: %w", node.Line, err)
		}
		if b {
			c.Mode = CommitModeDefault
		} else {
			c.Mode = CommitModeSkip
		}
		return nil
	case "!!str":
		if strings.ContainsAny(node.Value, "\n\r") {
			return fmt.Errorf("commit message must be single-line, got multi-line at line %d", node.Line)
		}
		if node.Value == "" {
			return fmt.Errorf("commit message cannot be empty string at line %d", node.Line)
		}
		c.Mode = CommitModeExplicit
		c.Message = node.Value
		return nil
	}
	return fmt.Errorf("commit must be a bool or string, got yaml tag %q at line %d", node.Tag, node.Line)
}

// DerivedCommitMessage returns the commit message to use for a given
// step when CommitModeDefault is in effect at the step boundary.
// Stage and skill names are sanitized identically to the manifest's
// on-disk directory layout (see sanitizeFsName in manifest_writer.go)
// so the message is filesystem-safe to grep / pipe through git tooling.
func DerivedCommitMessage(pipelineName, stageName, skill string) string {
	return fmt.Sprintf(
		"ape:%s/%s/%s",
		sanitizeFsName(pipelineName),
		sanitizeFsName(stageName),
		sanitizeFsName(skill),
	)
}

// DerivedStageCommitMessage returns the commit message for a
// stage-boundary CommitModeDefault commit (PLAN-6 / C2). There is no
// single skill to attribute the commit to — the chain folds into
// one commit — so the derived form drops the skill component:
//
//	ape:<pipeline>/<stage>
func DerivedStageCommitMessage(pipelineName, stageName string) string {
	return fmt.Sprintf(
		"ape:%s/%s",
		sanitizeFsName(pipelineName),
		sanitizeFsName(stageName),
	)
}

// DerivedTaskCommitMessage returns the derived message for a bare
// `--task-commit` on `ape task <skill>` (PLAN-11):
//
//	ape:task/<skill>
//
// Mirrors the pipeline's derived `ape:<pipeline>/<stage>/<skill>` shape.
func DerivedTaskCommitMessage(skill string) string {
	return "ape:task/" + sanitizeFsName(skill)
}

// Resolve returns the concrete commit message + skip flag for a step,
// applying CommitDirective semantics. The `skip` return is true when
// the spec said `commit: false`; the caller still has to check the
// global `--no-commit` flag and the step's run-status separately.
//
// Pointer receiver matches UnmarshalYAML's pointer receiver — recvcheck
// requires both to use the same form even though Resolve does not
// mutate.
func (c *CommitDirective) Resolve(pipelineName, stageName, skill string) (msg string, skip bool) {
	switch c.Mode {
	case CommitModeSkip:
		return "", true
	case CommitModeExplicit:
		return c.Message, false
	default:
		return DerivedCommitMessage(pipelineName, stageName, skill), false
	}
}
