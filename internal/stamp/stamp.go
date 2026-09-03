// Package stamp issues the framework's `timestamp` variable, and
// guarantees it never moves backwards.
//
// # Why this is a package and not a call to time.Now
//
// Every APEX skill resolves a `timestamp` and copies it into a field that
// records when something was last written — `updated_at`, `generated_at`,
// `frozen_at`, a review report's own `timestamp`. A field that answers
// "when was this last written?" moving backwards is a corrupt write, and
// it was observed in the field: a consuming project's `sprint-status.yaml`
// `updated_at` went back ~2h44m and an ADR index's `generated_at` ~57min,
// written by different agents. Clock skew between machines, a resumed
// session, a hand-edited predecessor — the cause varies and the damage
// does not.
//
// The framework's answer was a paragraph, carried verbatim in ~53 skills,
// instructing each writer to compare and clamp before writing. That works
// exactly as well as an instruction is followed. The contract does not
// change here; its implementation moves into the binary, which is what
// lets the framework delete the paragraph and the meta-rule that would
// re-author it (PLAN-64 5.8, VAR-09-TSBASIS).
//
// # The guarantee
//
//   - Basis is the system's LOCAL wall-clock — its own configured
//     timezone, never UTC unless the system itself is set to UTC. The
//     framework's `_output/` filenames have always been local.
//   - Format is apexcfg.TimestampLayout (`YYYYMMDDHHMMSS`), which is
//     lexicographically ordered, so string comparison is time comparison.
//   - Every issue returns max(now, last_issued). Never earlier.
//
// # Why a clamp and not an error
//
// Keeping the floor is a deterministic repair that needs no operator
// judgement, so it resolves silently and identically in guided and
// autonomous runs. Failing instead would trade a corrupt field for a dead
// run on a skewed clock — the framework's rule is explicit that this must
// never be "strengthened" to a halt.
//
// # The seed
//
// The floor persists under {output_folder}/ape/. When that file is absent
// — a fresh clone, a cleaned `_output` — the floor is seeded from the
// newest stamp already committed in `sprint-status.yaml`'s `updated_at`.
// Without the seed, a clean checkout silently resets the floor to the
// wall clock, and `ape sprint verify`'s exit-5 backwards-write check
// becomes the only thing left holding the guarantee. It is meant to be
// the detector of last resort, not the only detector.
package stamp

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"gopkg.in/yaml.v3"
)

// stampRe is the YYYYMMDDHHMMSS form. A value in any other shape is not a
// stamp and is never compared against one: a lexicographic comparison
// against arbitrary text is meaningless, and here it would set the floor
// to garbage for the life of the project.
var stampRe = regexp.MustCompile(`^\d{14}$`)

// IsStamp reports whether s is a well-formed stamp.
func IsStamp(s string) bool { return stampRe.MatchString(s) }

// Issuer hands out monotonic stamps for one project.
//
// Not safe for concurrent use across processes by construction — the
// read-modify-write is not locked. That is deliberate and sufficient: the
// floor only ever moves forward, so the worst a lost update can do is
// leave the floor one issue behind, and the next issue restores it. A
// lock here would serialise every project-data command for a guarantee
// the ordering already provides.
type Issuer struct {
	projectRoot string
	now         func() time.Time
	// statePath and trackerPath are resolved once. Both may be empty when
	// there is no project, which is a normal outcome: `ape chat` in a bare
	// directory still needs a timestamp.
	statePath   string
	trackerPath string
}

// New builds an Issuer for projectRoot. now may be nil for time.Now.
func New(projectRoot string, now func() time.Time) *Issuer {
	if now == nil {
		now = time.Now
	}
	iss := &Issuer{projectRoot: projectRoot, now: now}
	if projectRoot == "" {
		return iss
	}
	iss.statePath = runlog.TimestampStatePath(projectRoot)
	// Resolved with a nil clock — this call must not re-enter the issuer,
	// and it has no reason to: it is asked only for paths.
	if res, err := apexcfg.ResolveAt(projectRoot, nil); err == nil {
		iss.trackerPath = res.Paths.SprintStatus
	}
	return iss
}

// Now returns the issued instant: the later of the wall clock and the
// persisted floor. Callers that want the formatted value use Issue; this
// exists so it can be handed to apexcfg.Resolve as its Clock, which makes
// the resolved `date` and `timestamp` derive from the SAME instant.
//
// That matters more than it looks: deriving `date` from the raw wall clock
// while `timestamp` is clamped forward would let a project's records carry
// a date of one day and a timestamp of the next.
func (i *Issuer) Now() time.Time {
	now := i.now()
	floor, ok := i.floor()
	if !ok {
		i.persist(now)
		return now
	}
	if now.Before(floor) {
		// The clamp. The floor is already persisted, so nothing to write.
		return floor
	}
	i.persist(now)
	return now
}

// Issue returns the formatted stamp for the issued instant.
func (i *Issuer) Issue() string {
	return i.Now().Format(apexcfg.TimestampLayout)
}

// Clock adapts the Issuer to apexcfg.Clock.
func (i *Issuer) Clock() apexcfg.Clock { return i.Now }

// floor reads the persisted floor, seeding it from the tracker when the
// state file is absent. A floor that cannot be established is reported as
// absent rather than as zero, so the caller uses the wall clock rather
// than clamping against the epoch.
func (i *Issuer) floor() (time.Time, bool) {
	if i.statePath == "" {
		return time.Time{}, false
	}
	if t, ok := parseStamp(readStampFile(i.statePath)); ok {
		return t, true
	}
	// Absent or unreadable state: seed from what the project itself
	// already claims. An unparseable state file is treated as absent for
	// the same reason a malformed stamp is never compared — better to
	// re-seed from a real value than to trust a corrupt floor.
	return parseStamp(trackerUpdatedAt(i.trackerPath))
}

// persist records the new floor. Best-effort by design: a project whose
// output folder is unwritable (a read-only checkout, a sandbox mount)
// must still be able to resolve a timestamp. Losing the floor degrades
// the guarantee to what it was before this package existed; failing the
// command would degrade it to nothing at all.
func (i *Issuer) persist(t time.Time) {
	if i.statePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(i.statePath), 0o755); err != nil {
		return
	}
	_ = atomicfile.Write(i.statePath, []byte(t.Format(apexcfg.TimestampLayout)+"\n"))
}

func readStampFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// trackerUpdatedAt reads the tracker's top-level updated_at. Absence,
// unreadability and a malformed file are all the same normal outcome:
// there is no seed, so there is no floor.
func trackerUpdatedAt(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return ""
	}
	v, ok := raw["updated_at"]
	if !ok || v == nil {
		return ""
	}
	// Rendered through %v rather than a string assertion: a YAML scalar
	// of 14 digits decodes as an int, and rejecting it would drop exactly
	// the seed this function exists to find.
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

// parseStamp turns a well-formed stamp into a LOCAL time. The layout has
// no zone, and time.ParseInLocation with time.Local is what keeps the
// round trip exact: time.Parse would read it as UTC and shift every
// comparison by the host's offset.
func parseStamp(s string) (time.Time, bool) {
	if !IsStamp(s) {
		return time.Time{}, false
	}
	//nolint:gosmopolitan // local wall-clock IS the contract here — see
	// the package doc. A UTC parse would shift every comparison by the
	// host's offset and silently break the guarantee.
	t, err := time.ParseInLocation(apexcfg.TimestampLayout, s, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
