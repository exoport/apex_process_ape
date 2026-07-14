package eventing

import (
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// DefaultPrefix is the default root for progress-event subjects. It is
// overridable per invocation (--events-subject-prefix); the rest of the
// taxonomy is a versioned, additive-only contract (docs/reference/events.md).
const DefaultPrefix = "ape.evt"

// Kind is the <kind> subject segment: which surface produced the run.
type Kind string

const (
	KindPipeline Kind = "pipeline"
	KindTask     Kind = "task"
	KindPrompt   Kind = "prompt"
	KindScript   Kind = "script"
	KindSession  Kind = "session" // standalone / agent-initiated reporting (PLAN-17)
	KindSvc      Kind = "svc"     // daemon lifecycle (PLAN-14)
)

// ProjectSlug derives the <project> subject token from a project root: the
// base directory name, slugged the same way as the <user> token so it is a
// valid single subject token.
func ProjectSlug(projectRoot string) string {
	base := filepath.Base(strings.TrimRight(projectRoot, `/\`))
	slug := natsconn.SubjectToken(base)
	if slug == "" || slug == "." || slug == "-" {
		return "project"
	}
	return slug
}

// token sanitizes an arbitrary string into a single subject token, so a
// stray id/event value can never inject extra subject levels or wildcards.
func token(s string) string {
	slug := natsconn.SubjectToken(s)
	if slug == "" {
		return "unknown"
	}
	return slug
}

// subject builds `<prefix>.<user>.<project>.<kind>.<id>.<event>`.
func (p *Publisher) subject(event string) string {
	return strings.Join([]string{
		p.prefix,
		p.user,
		p.project,
		string(p.kind),
		p.id,
		token(event),
	}, ".")
}
