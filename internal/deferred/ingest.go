package deferred

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// IngestOptions carries what the caller knows that the bullets do not.
type IngestOptions struct {
	Story string
	Skill string
	Cycle int
	Date  string
}

// IngestResult reports what was stored.
type IngestResult struct {
	Records []Record `json:"records" yaml:"records"`
	Stored  []string `json:"stored"  yaml:"stored"`
	// Warnings are non-fatal, by design: see Ingest's contract.
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Count    int      `json:"count"              yaml:"count"`
}

// SplitBullets divides an input blob into one chunk per record, keeping
// indented continuation lines with their bullet.
//
// The rule that matters: an INDENTED line continues the current bullet,
// while an UNINDENTED non-bullet line starts a new chunk. Without that
// second half, a paragraph following a bullet is silently absorbed into
// it — which in a real ledger means two records become one, and the
// losslessness count is quietly wrong.
//
// Anything before the first bullet becomes its own chunk rather than being
// dropped: the store never discards input it did not understand.
func SplitBullets(data []byte) []string {
	var (
		chunks      []string
		current     strings.Builder
		startedList bool
	)
	flush := func() {
		if strings.TrimSpace(current.String()) != "" {
			chunks = append(chunks, current.String())
		}
		current.Reset()
		startedList = false
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Real bodies run long; the default 64 KiB token limit is not enough
	// for a pasted diff inside a record.
	const (
		scanStartSize = 64 << 10
		scanMaxLine   = 4 << 20
	)
	scanner.Buffer(make([]byte, 0, scanStartSize), scanMaxLine)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case topLevelBulletRe.MatchString(line):
			flush()
			startedList = true
		case strings.TrimSpace(line) == "":
			// A blank line is kept either way, so the body stays
			// byte-for-byte: inside a bullet's continuation it is content,
			// between records it is separation.
			if current.Len() > 0 {
				current.WriteString("\n")
			}
			continue
		case startedList && !isIndented(line):
			// An unindented paragraph after a bullet is its own record.
			flush()
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	flush()
	return chunks
}

// isIndented reports whether a line is a continuation of the bullet above
// it. One space is enough — the framework indents continuations by two.
func isIndented(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

// topLevelBulletRe matches an unindented (or single-space-indented) list
// item, which starts a new record. A more deeply indented bullet is a
// sub-item and stays with its parent.
var topLevelBulletRe = regexp.MustCompile(`^ ?[-*] `)

// Ingest parses bullets from data and writes one record per bullet.
//
// EXIT-CODE CONTRACT, and the reason this function returns no error for
// any content: `ape deferred ingest` is reachable from
// apex-review-story's emit path, where a non-zero exit converts a defer
// into a patch, raises unfixed_patches, and DEMOTES THE STORY to
// in-progress. So:
//
//   - a bullet whose shape is unrecognised is stored verbatim as
//     free-form, with a warning;
//   - empty input is a no-op, not a failure;
//   - only a genuine setup failure — an unwritable directory — returns
//     an error, which is C2's distinction between a content verdict and a
//     broken environment.
func (s *Store) Ingest(data []byte, opts IngestOptions) (*IngestResult, error) {
	res := &IngestResult{}
	chunks := SplitBullets(data)
	if len(chunks) == 0 {
		return res, nil
	}
	seen := map[string]int{}
	for _, chunk := range chunks {
		rec := ParseBullet(chunk)
		rec.SourceStory = opts.Story
		rec.Skill = opts.Skill
		rec.Cycle = opts.Cycle
		rec.Created = opts.Date
		rec.Source = sourceForSkill(opts.Skill)
		// The same three discharge readings the migration applies, for the
		// same reason and in the same order: a body that says it is closed
		// must not land in the open working set, whichever door it came in
		// through. Without this the invariant `verify` now checks was true of
		// migrated records and merely documented for ingested ones — and a
		// `certain` finding on a record ape itself had just written is a
		// contradiction the operator has to unpick.
		//
		// Nothing here can fail, so the exit-code contract above is untouched.
		//
		// ONE RESIDUAL, named rather than fixed: SplitBullets has no
		// absorbAnnotation, so a marker written at column 0 in an ingest
		// payload still becomes its own record. An ingest payload is a skill's
		// freshly-emitted defer bullets, not a register carrying an
		// append-only closure convention, and no field payload has ever
		// carried one.
		applyResolutionBanner(&rec)
		applyClosureMarker(&rec)
		applyStatusAnnotation(&rec)
		rec.ID = NewID(opts.Date, rec.Title, rec.Body)

		// Two identical bullets in one payload would collide on a
		// content-addressed id. Suffix the later ones rather than silently
		// overwriting: the operator wrote two, so two are stored.
		if n := seen[rec.ID]; n > 0 {
			rec.ID = fmt.Sprintf("%s-%d", rec.ID, n+1)
		}
		seen[rec.ID]++

		if rec.FreeForm {
			res.Warnings = append(res.Warnings, rec.ID+": stored verbatim as free-form (no [Defer] bullet shape) — flag for `ape deferred verify`")
		}
		write := s.Write
		if !rec.IsOpen() {
			// Straight to closed/, because the OPEN SET IS THE DIRECTORY:
			// Load reads open records by listing s.Dir, not by filtering on
			// the status field, so a `status: closed` file written here would
			// sit in `ape deferred list` forever. It is also worth saying out
			// loud — a skill filing a defer that already announces its own
			// discharge is a thing the operator should see, not a silent
			// re-route.
			write = s.writeClosed
			res.Warnings = append(res.Warnings,
				rec.ID+": the bullet's own text says it is already discharged — stored in closed/, not the working set")
		}
		path, err := write(rec)
		if err != nil {
			// An unwritable store IS a setup failure, and the only thing
			// here allowed to fail the command.
			return nil, err
		}
		rec.Path = path
		res.Records = append(res.Records, rec)
		res.Stored = append(res.Stored, path)
	}
	res.Count = len(res.Records)
	return res, nil
}

// sourceForSkill maps the filing skill onto the source vocabulary, so
// migrated and freshly-ingested records really are comparable.
//
// It defers to classifySource, which is the whole fix: this function used
// to fall back to the RAW SKILL NAME while the migration's half of the same
// mapping fell back to `unknown`, so the two produced different values for
// the same conceptual source and the claim in this comment was false. The
// skill is not lost by the change — `skill` carries it verbatim on every
// ingested record.
//
// It passes an EMPTY skill through rather than short-circuiting it, which
// costs nothing (`classifySource("")` matches nothing and is `unknown`) and
// buys the exactness `Source`'s own doc claims: `unknown` means a
// classification ran and nothing matched, on every door, with no case where
// the value was reached without one.
func sourceForSkill(skill string) string {
	return classifySource(skill)
}
