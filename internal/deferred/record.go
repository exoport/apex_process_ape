// Package deferred is the deferred-work record store: one file per
// record, YAML frontmatter plus a verbatim Markdown body.
//
// It replaces a single 456,144-byte `deferred-work.md` whose only
// eviction mechanism was deletion — git shows 524 records removed in one
// commit — and whose size made it unreadable by the skills that append to
// it. One file per record is not a style choice: the single-store
// alternative returns ZERO records when handed one malformed entry, while
// this shape loses exactly the one bad file.
//
// The store lives under `{development_folder}/deferred/`, deliberately
// OUTSIDE `{implementation_folder}`: ten skills glob
// `{implementation_folder}/**/*.md` across 17 sites, and 227 record files
// under that folder would feed every one of them. Siting it one level up
// costs zero skill edits and cannot collide.
package deferred

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Status values. A closed record never leaves the disk — it moves to
// `closed/`. Deletion is what destroyed the audit trail the first time,
// and an LLM that cannot see a closed defer re-files it.
const (
	StatusOpen      = "open"
	StatusClosed    = "closed"
	StatusDiscarded = "discarded"
)

// Source values recorded from the legacy ledger's section headings.
const (
	SourceStoryReview   = "story-review"
	SourceCorrectCourse = "correct-course"
	SourceUnknown       = "unknown"
)

// Record is one deferred item.
//
// Field names are the wire contract the framework's skills read. The
// three `— defer:` tail fields are carried VERBATIM and unparsed: they
// are free text in the corpus ("yes", "no", "n/a", prose), and
// normalising them would be the kind of guess this store exists to avoid.
type Record struct {
	ID     string `json:"id"               yaml:"id"`
	Title  string `json:"title"            yaml:"title"`
	Status string `json:"status"           yaml:"status"`
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
	// SourceStory is the story whose review filed this.
	SourceStory string `json:"source_story,omitempty" yaml:"source_story,omitempty"`
	// Skill is the skill that filed it.
	Skill string `json:"skill,omitempty" yaml:"skill,omitempty"`
	// Cycle is the review cycle number, when the caller passed one.
	Cycle   int      `json:"cycle,omitempty"   yaml:"cycle,omitempty"`
	Created string   `json:"created,omitempty" yaml:"created,omitempty"`
	Anchors []string `json:"anchors,omitempty" yaml:"anchors,omitempty"`

	OutsideStory string `json:"outside_story,omitempty" yaml:"outside_story,omitempty"`
	CrossCycle   string `json:"cross_cycle,omitempty"   yaml:"cross_cycle,omitempty"`
	NonBlocking  string `json:"non_blocking,omitempty"  yaml:"non_blocking,omitempty"`

	// Owner and Trigger are the two fields upstream A7 adds at emit time.
	// Without a closing condition captured when the defer is filed,
	// nothing can discharge it later.
	Owner      string `json:"owner,omitempty"       yaml:"owner,omitempty"`
	Trigger    string `json:"trigger,omitempty"     yaml:"trigger,omitempty"`
	NextAction string `json:"next_action,omitempty" yaml:"next_action,omitempty"`

	// Group, Related and Supersedes are relations, NOT a DAG: 0 of 109
	// real records assert an ordering constraint on another, the largest
	// related family is 4, and there are no chains of length 3.
	Group      string   `json:"group,omitempty"      yaml:"group,omitempty"`
	Related    []string `json:"related,omitempty"    yaml:"related,omitempty"`
	Supersedes []string `json:"supersedes,omitempty" yaml:"supersedes,omitempty"`

	// FreeForm marks a record the deterministic parser could not complete:
	// its body is the original text verbatim and its title is the first
	// line. 26 of 109 real records are free-form, so this path always runs.
	FreeForm bool `json:"free_form,omitempty" yaml:"free_form,omitempty"`

	// ResolvedBy and ResolvedAt are written by close.
	ResolvedBy string `json:"resolved_by,omitempty" yaml:"resolved_by,omitempty"`
	ResolvedAt string `json:"resolved_at,omitempty" yaml:"resolved_at,omitempty"`
	// DiscardReason and DiscardEvidence are written by the judgment phase.
	DiscardReason   string `json:"discard_reason,omitempty"   yaml:"discard_reason,omitempty"`
	DiscardEvidence string `json:"discard_evidence,omitempty" yaml:"discard_evidence,omitempty"`

	// Body is the record's Markdown, byte-identical to what was ingested.
	// Never serialised into frontmatter; it is the file's content below the
	// closing delimiter.
	Body string `json:"body" yaml:"-"`

	// Path is where the record lives on disk. Derived, not stored.
	Path string `json:"path,omitempty" yaml:"-"`
}

// IsOpen reports whether the record is still in the working set.
func (r Record) IsOpen() bool { return r.Status == StatusOpen || r.Status == "" }

// idPrefix is the record-id namespace: DW for "deferred work".
const idPrefix = "DW"

// slugMaxLen bounds a filename's slug component. Long enough to stay
// recognisable, short enough that path limits are never the problem.
const slugMaxLen = 48

// hashLen is how much of the content digest goes into an id. Six hex
// characters over a per-day namespace: collisions are handled explicitly
// rather than assumed away.
const hashLen = 6

// NewID builds a deterministic record id from the filing date and the
// record's own content, so re-ingesting the same bullet on the same day
// yields the same id rather than a duplicate.
func NewID(date, title, body string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + body))
	return fmt.Sprintf("%s-%s-%s", idPrefix, compactDate(date), hex.EncodeToString(sum[:])[:hashLen])
}

func compactDate(date string) string {
	var b strings.Builder
	for _, r := range date {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "00000000"
	}
	return b.String()
}

// Slug renders a title as a filename component.
//
// Windows-safety is the point: a record title routinely contains `:`,
// `?`, quotes and slashes, all of which are illegal in a Windows filename
// and would make the store unusable there.
func Slug(title string) string {
	var b strings.Builder
	lastDash := true // suppress a leading dash
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
		if b.Len() >= slugMaxLen {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "record"
	}
	return out
}

// FileName is the record's on-disk name.
func (r Record) FileName() string {
	return r.ID + "_" + Slug(r.Title) + ".md"
}

// tailRe matches the `— defer: …` tail the framework appends to a defer
// bullet. The three fields inside are captured as one blob and split
// separately, because their separators vary in the real corpus.
var tailRe = regexp.MustCompile(`(?s)\s*[—–-]\s*defer:\s*(.*)$`)

// deferBulletRe matches the bullet forms: `- [Defer] title` and
// `- [ ] [Defer] title`. The checkbox is discarded — `status` carries it —
// but its PRESENCE is preserved in the body, which the current
// append-to-a-single-file path strips in 100% of the 109 live records.
var deferBulletRe = regexp.MustCompile(`^\s*[-*]\s*(?:\[([ xX])\]\s*)?\[Defer\]\s*(.*)$`)

// plainBulletRe matches any other list item, which becomes a free-form
// record rather than being dropped.
var plainBulletRe = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)

// anchorRe matches a `[path:line]` citation bracket.
var anchorRe = regexp.MustCompile(`\[([^\]\s]+:\d+(?:-\d+)?)\]`)

// fieldRes are the `key=value` and `key: value` fields that appear in a
// tail. Each is applied to the tail blob, not the whole bullet, so a
// title containing "owner=" is not misread.
var (
	outsideStoryRe = regexp.MustCompile(`outside-story\s*=\s*([^;\n]+)`)
	crossCycleRe   = regexp.MustCompile(`cross-cycle\s*=\s*([^;\n]+)`)
	nonBlockingRe  = regexp.MustCompile(`non-blocking\s*=\s*([^;\n]+)`)
	ownerRe        = regexp.MustCompile(`owner\s*=\s*([^;\n]+)`)
	triggerRe      = regexp.MustCompile(`trigger\s*[:=]\s*([^;\n]+)`)
	nextActionRe   = regexp.MustCompile(`next-batch brief\s*[:=]\s*([^;\n]+)`)
)

// ParseBullet turns one bullet (with any continuation lines) into a
// Record. It never fails: a bullet whose shape is unrecognised becomes a
// free-form record with the text as its body and its first line as the
// title. Nothing is dropped and nothing is guessed at.
func ParseBullet(text string) Record {
	rec := Record{Status: StatusOpen, Body: text}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 0 {
		rec.Title = "(empty)"
		rec.FreeForm = true
		return rec
	}
	first := lines[0]

	m := deferBulletRe.FindStringSubmatch(first)
	if m == nil {
		// Not a [Defer] bullet. Keep everything, flag it, move on — the
		// judgment phase completes or retires these.
		rec.FreeForm = true
		if pm := plainBulletRe.FindStringSubmatch(first); pm != nil {
			rec.Title = strings.TrimSpace(pm[1])
		} else {
			rec.Title = strings.TrimSpace(first)
		}
		rec.Title = trimTail(rec.Title)
		if rec.Title == "" {
			rec.Title = "(untitled)"
		}
		applyTail(&rec, strings.Join(lines, "\n"))
		rec.Anchors = findAnchors(text)
		return rec
	}

	titleAndTail := m[2]
	tail := ""
	if tm := tailRe.FindStringSubmatch(titleAndTail); tm != nil {
		tail = tm[1]
	}
	rec.Title = strings.TrimSpace(stripAnchors(trimTail(titleAndTail)))
	if rec.Title == "" {
		rec.Title = "(untitled)"
	}
	// Fields can appear in the tail or on continuation lines.
	applyTail(&rec, tail+"\n"+strings.Join(lines[1:], "\n"))
	rec.Anchors = findAnchors(text)
	return rec
}

// trimTail removes a `— defer: …` tail from a title fragment.
func trimTail(s string) string {
	if loc := tailRe.FindStringIndex(s); loc != nil {
		return s[:loc[0]]
	}
	return s
}

func applyTail(rec *Record, blob string) {
	set := func(re *regexp.Regexp, dst *string) {
		if *dst != "" {
			return
		}
		if m := re.FindStringSubmatch(blob); m != nil {
			*dst = strings.TrimSpace(m[1])
		}
	}
	set(outsideStoryRe, &rec.OutsideStory)
	set(crossCycleRe, &rec.CrossCycle)
	set(nonBlockingRe, &rec.NonBlocking)
	set(ownerRe, &rec.Owner)
	set(triggerRe, &rec.Trigger)
	set(nextActionRe, &rec.NextAction)
}

func findAnchors(text string) []string {
	matches := anchorRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

func stripAnchors(s string) string {
	return strings.TrimSpace(anchorRe.ReplaceAllString(s, ""))
}
