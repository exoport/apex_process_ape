package deferred

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/exoport/apex_process_ape/internal/frontmatter"
)

// headingRe matches ANY level-2 heading.
//
// The ledger's structure is carried entirely by its `##` lines, so every
// one of them is a record boundary. The parser used to know only the one
// provenance form below and let every other `##` line fall through into
// generic text accumulation, which is a single gap that produced three
// separate field defects at once: the previous section's story and date
// stayed live under a heading that had nothing to do with them, the
// unrecognised heading itself became body text, and the ledger's own
// preamble accumulated into a record because `## Still open` reset
// nothing either.
//
// Being heading-aware rather than bullet-aware closes all three.
var headingRe = regexp.MustCompile(`^##\s+\S`)

// sectionRe matches the legacy ledger's provenance headings and captures
// everything after the label:
//
//	## Deferred from: story review of 54-1 (2026-08-21)
//	## Deferred from: apex-correct-course reconciliation of epic 12 (2026-07-02)
//
// It deliberately captures the WHOLE remainder rather than trying to
// structure it in one pattern. The previous shape made slug and date
// alternatives inside a single lazy expression, so a heading carrying a
// prose parenthetical —
//
//	## Deferred from: story review of 86-1_… (retroactively backfilled by Story 89.2, 2026-07-26)
//
// — failed the date group, backtracked, and collapsed the entire remainder
// into the label: the slug was lost too, with both values sitting in
// plain sight on the line. sectionFrom extracts them independently.
var sectionRe = regexp.MustCompile(`(?i)^##\s+Deferred from:\s*(.*)$`)

// sectionSlugRe and isoDateRe are the two INDEPENDENT extractions.
//
// The slug's anchor is `of <token>`, whatever follows it, and the date is
// the last ISO date anywhere in the heading — so neither can be defeated
// by the other failing to match.
var (
	sectionSlugRe = regexp.MustCompile(`(?i)\bof\s+(\S+)`)
	isoDateRe     = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
)

// MigrateOptions controls one migration.
type MigrateOptions struct {
	// From is the legacy ledger. Required.
	From string
	// RecoverDeleted mines git history for records removed from the ledger
	// and writes them straight to closed/.
	//
	// The CLI defaults this ON; the zero value here stays off so no
	// existing library caller changes behaviour by being recompiled. The
	// two disagree deliberately — see the CLI's flag for why the default
	// moved.
	RecoverDeleted bool
	// DryRun parses and verifies, writing nothing.
	DryRun bool
}

// RecoverOptions controls a standalone recovery into an existing store.
type RecoverOptions struct {
	// From is the legacy ledger path. After a migration this is a stub,
	// which is fine and is the whole point: history is read THROUGH the
	// path, not out of the file sitting at it.
	From string
	// DryRun reports what would be recovered, writing nothing.
	DryRun bool
}

// RecoverResult is what a standalone recovery did, or would do.
type RecoverResult struct {
	From string `json:"from" yaml:"from"`
	To   string `json:"to"   yaml:"to"`
	// Recovered is how many tombstones were written.
	Recovered int `json:"recovered" yaml:"recovered"`
	// Existing is how many records were already on disk, and so were the
	// set the recovery de-duplicated against.
	Existing int      `json:"existing"           yaml:"existing"`
	Records  []Record `json:"records,omitempty"  yaml:"records,omitempty"`
	Paths    []string `json:"paths,omitempty"    yaml:"paths,omitempty"`
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	DryRun   bool     `json:"dry_run"            yaml:"dry_run"`
}

// MigrateResult is what the migration did, or would do.
type MigrateResult struct {
	From string `json:"from" yaml:"from"`
	To   string `json:"to"   yaml:"to"`
	// RecordsIn is what the parser found; RecordsOut is what was written.
	// The losslessness assertion is that they are equal.
	RecordsIn  int `json:"records_in"  yaml:"records_in"`
	RecordsOut int `json:"records_out" yaml:"records_out"`
	FreeForm   int `json:"free_form"   yaml:"free_form"`
	Recovered  int `json:"recovered"   yaml:"recovered"`
	// Closed is how many parsed records arrived already discharged, by any
	// of the three shapes a ledger uses to say so: a resolution banner, an
	// appended closure marker, or a bracketed status annotation. Discarded
	// records are counted here too — they are equally not open items.
	Closed int `json:"closed" yaml:"closed"`
	// RecoverRequested records that recovery was asked for, so the
	// AlreadyDone path can say the request could not be honoured. A flag
	// that is silently ignored is indistinguishable from one that ran and
	// found nothing, and those are very different facts.
	RecoverRequested bool `json:"recover_requested" yaml:"recover_requested"`
	// Bytes is how much ledger text moved out of the legacy file. The
	// framework's anchor and citation gates are scoped to a folder, so a
	// migration that relocates this much cited text moves those counts
	// with it; reporting it is what stops that looking like a regression.
	Bytes int `json:"bytes" yaml:"bytes"`
	// Paths is every file written, for the caller's `git add` line.
	Paths []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	// StubWritten records that the legacy file became a signpost.
	StubWritten bool `json:"stub_written" yaml:"stub_written"`
	// PreambleWritten records that the ledger's own header — document
	// history, not a deferred item — was preserved beside the records.
	PreambleWritten bool `json:"preamble_written" yaml:"preamble_written"`
	// PreambleBytes and OrphanHeadings are WHY that file exists, and they
	// are two independent reasons: prose above the first boundary, and
	// headings that titled no record. Either alone writes it, so
	// `preamble_written` on its own cannot tell an operator what is in it —
	// and calling an orphan-headings-only file "the ledger preamble" names
	// something the ledger never had.
	PreambleBytes  int      `json:"preamble_bytes"     yaml:"preamble_bytes"`
	OrphanHeadings int      `json:"orphan_headings"    yaml:"orphan_headings"`
	DryRun         bool     `json:"dry_run"            yaml:"dry_run"`
	AlreadyDone    bool     `json:"already_done"       yaml:"already_done"`
	Warnings       []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// ErrLosslessnessFailed reports that the verify-before-write assertion did
// not hold. Nothing is written when this is returned.
var ErrLosslessnessFailed = errors.New("losslessness assertion failed")

// ErrStorePopulated reports a store that already holds records when a
// migration was asked to run into it.
//
// Without this the migration writes its records alongside the existing
// ones and only then fails the post-write count assertion — files on
// disk, an error at the end, and an operator left to work out which half
// is which. Refusing up front is the same verdict delivered before
// anything is touched.
var ErrStorePopulated = errors.New("store already holds records")

// LegacyDocument is everything a parse of the ledger produced. Every line
// of the source is accounted for by exactly one of its fields, which is
// what lets verifyLossless check the whole document rather than the part
// that happens to be convenient.
type LegacyDocument struct {
	// Records are the deferred items.
	Records []Record
	// Preamble is ledger prose that is not a record: the header above the
	// first boundary. Verbatim ledger text and nothing else.
	Preamble string
	// OrphanHeadings are headings that titled no record, because another
	// heading followed with nothing in between.
	//
	// They are kept rather than dropped. `## Still open` is noise and
	// `## Deferred-at-decision: <a real title>` is not, and nothing here
	// can tell them apart — so neither is deleted. They are stored beside
	// the preamble, not inside it, so Preamble stays verbatim.
	OrphanHeadings []string
	// Headings is every `##` line consumed as a boundary that went on to
	// produce a record — lifted out of the body and written verbatim to
	// each of those records' `source_heading`.
	//
	// It exists to be COUNTED. Heading lines are the one class that leaves
	// the body text, and an exemption that is merely ignored is a hole the
	// losslessness check cannot see through: heading-shaped content could
	// be deleted silently and the check would still pass.
	//
	// Counting it is only honest because `source_heading` puts it on disk.
	// While it was a parse-time artifact that evaporated at write time,
	// this bucket made the check prove something about the PARSE while
	// reading as a claim about the MIGRATION — and the text a recognised
	// provenance heading carries beyond `source`/`source_story`/`created`
	// really was being dropped.
	Headings []string
}

// ParseLegacy splits the legacy ledger into records, discarding everything
// that is not one. Use ParseLegacyDocument where the rest matters — the
// migration keeps it, because discarding it is what the store exists to
// stop.
func ParseLegacy(data []byte) []Record {
	return ParseLegacyDocument(data).Records
}

// ParseLegacyDocument splits the legacy ledger into records plus the
// preamble the records sit under.
//
// The mapping is fixed, because this is the only place losslessness can be
// lost. A record whose tail does not match keeps its full text as the body
// and takes its title from the first line; nothing is dropped and nothing
// is guessed at.
//
// THREE STRUCTURAL RULES, each of which was a field defect:
//
//   - A `##` heading is a boundary, always. A recognised one sets
//     provenance context; an unrecognised one CLEARS it. Letting the
//     previous `## Deferred from:` context stay live under an unrelated
//     heading made records assert a story they never sat under, and the
//     inherited date is hashed into the id — so the damage survived any
//     later frontmatter edit and could only be undone by re-running.
//
//   - A heading is never body text. An unrecognised one titles the FIRST
//     record beneath it and is lifted out of every body, exactly as the
//     provenance form already was. Leaving it in the body made the store
//     hold titles with no body and bodies with no title, and which of the
//     two you got depended on whether the next line was a bullet.
//
//   - Content above the first boundary is preamble, not a record. In the
//     field this produced one 434-line, 37,822-byte "open item" that was
//     pure document history. A ledger that opens with bullets and no
//     heading still parses: the first top-level bullet is a boundary too,
//     so a flat ledger does not collapse into preamble.
//
// A FENCED CODE BLOCK SUSPENDS ALL OF IT. Inside a ``` or ~~~ fence
// opened at column 0, no line is a boundary — not a heading and not a
// bullet. Without that, a Makefile pasted into the ledger has its
// `## build everything` comment consumed as a section heading: the line is
// deleted, and the fence is split across two records with the opener in
// one and the closer in the other. Losslessness cannot catch it, because
// the deleted line is heading-shaped and heading-shaped lines are the
// exempt class — which is exactly why the exemption is now COUNTED
// (LegacyDocument.Headings) instead of ignored. An unclosed fence runs to
// the end of the document, as Markdown says it does.
func ParseLegacyDocument(data []byte) *LegacyDocument {
	doc := &LegacyDocument{}
	var (
		section     sectionContext
		pending     strings.Builder
		preambleBuf strings.Builder
		orphans     []string
		heading     string
		headingLine string
		headingUsed bool
		sawBoundary bool
		inFence     bool
	)
	flush := func() {
		text := pending.String()
		pending.Reset()
		if strings.TrimSpace(text) == "" {
			// A heading with nothing under it is not a record. `heading` is
			// deliberately NOT cleared here: it belongs to whatever content
			// comes next, which has not been buffered yet.
			return
		}
		if !sawBoundary {
			preambleBuf.WriteString(text)
			return
		}
		rec := ParseBullet(text)
		rec.Source = section.source
		rec.SourceStory = section.story
		rec.Created = section.date
		// The heading goes down verbatim on every record it covers. That
		// is what makes Headings an OUTPUT rather than a parse-time
		// artifact, and it is the only place the parts of a heading that
		// no structured field extracts survive at all.
		rec.SourceHeading = headingLine
		if heading != "" {
			// An unrecognised heading titles the first record under it, and
			// only that one — everything after takes its own first line.
			rec.Title = heading
			heading = ""
		}
		// Three independent ways a ledger says "already discharged", applied
		// in order. Each no-ops on a record another has already closed, so
		// the first to fire wins and none can reopen what another closed.
		applyResolutionBanner(&rec)
		applyClosureMarker(&rec)
		applyStatusAnnotation(&rec)
		rec.ID = NewID(section.date, rec.Title, rec.Body)
		doc.Records = append(doc.Records, rec)
		headingUsed = true
	}
	// closeHeading files the heading that is going out of scope. One that
	// produced a record is accounted for by that record's frontmatter; one
	// that produced nothing has nowhere to be, so its text is kept as
	// preamble rather than dropped. `## Still open` is noise and `##
	// Deferred-at-decision: <a real title>` is not, and nothing here can
	// tell them apart — so neither is deleted.
	closeHeading := func() {
		if headingLine == "" {
			return
		}
		if headingUsed {
			doc.Headings = append(doc.Headings, headingLine)
		} else {
			orphans = append(orphans, headingLine)
		}
		headingLine = ""
		headingUsed = false
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	const (
		scanStartSize = 64 << 10
		scanMaxLine   = 4 << 20
	)
	scanner.Buffer(make([]byte, 0, scanStartSize), scanMaxLine)
	startedList := false
	for scanner.Scan() {
		line := scanner.Text()
		if fenceRe.MatchString(line) {
			inFence = !inFence
			pending.WriteString(line)
			pending.WriteString("\n")
			continue
		}
		if inFence {
			// Verbatim, blank lines included: inside a fence a blank line
			// is content, not separation.
			pending.WriteString(line)
			pending.WriteString("\n")
			continue
		}
		if headingRe.MatchString(line) {
			flush()
			closeHeading()
			sawBoundary = true
			startedList = false
			headingLine = line
			if m := sectionRe.FindStringSubmatch(line); m != nil {
				section = sectionFrom(m[1])
				heading = ""
			} else {
				section = sectionContext{}
				heading = headingTitle(line)
			}
			continue
		}
		switch {
		case topLevelBulletRe.MatchString(line):
			if absorbAnnotation(line, sawBoundary, pending.String()) {
				// Not a boundary: this bullet annotates the record above it.
				break
			}
			flush()
			sawBoundary = true
			startedList = true
		case strings.TrimSpace(line) == "":
			if pending.Len() > 0 {
				pending.WriteString("\n")
			}
			continue
		case startedList && !isIndented(line):
			// Same rule as SplitBullets: an unindented paragraph after a
			// bullet is a separate record, not a continuation of it.
			flush()
			startedList = false
		}
		pending.WriteString(line)
		pending.WriteString("\n")
	}
	flush()
	closeHeading()

	// De-duplicate ids that collide because two records in the same
	// section have identical text. Both are kept: the operator wrote two.
	seen := map[string]int{}
	for i := range doc.Records {
		id := doc.Records[i].ID
		if n := seen[id]; n > 0 {
			doc.Records[i].ID = fmt.Sprintf("%s-%d", id, n+1)
		}
		seen[id]++
	}

	doc.Preamble = preambleBuf.String()
	doc.OrphanHeadings = orphans
	return doc
}

// absorbAnnotation reports whether a top-level bullet is a discharge
// marker on the record above it rather than a record boundary.
//
// THE FOURTH STRUCTURAL RULE, and the most expensive one to have missed.
// A register whose closure convention is POSITIONAL appends the marker as a
// sibling bullet at column 0, and it does it in BOTH of the vocabularies a
// ledger uses to say "discharged":
//
//   - [Defer] <the entry>
//
//   - [Closed: 193c4ec] Story 17.8 built the real adapter and swapped …
//
//   - [Defer] <the entry>
//
//   - **RESOLVED (2026-08-20) — closed by Story 3.17, re-verified …**
//
// Recognising only the bracketed family — on the theory that indentation
// told the two apart — left the second one splitting exactly as before, and
// worse than before: the marker now closes itself and moves to `closed/`,
// so the entry is left open with the evidence no longer even beside it, and
// invisible to `verify`, whose own body no longer carries a marker. Both
// families are read here for that reason.
//
// Every top-level bullet was a boundary, so the marker became a RECORD.
// On the ledger that produced this rule — 238 real entries, every one
// carrying a companion marker — that turned one discharged item into two
// open records: the entry, with nothing in its body saying it was closed,
// and the closure evidence, filed as an open free-form record of its own.
// 238 entries became 637 records, 252 of them pure annotations, and every
// one of the 637 landed `status: open` — while 52 of the entries carried a
// companion marker saying they were already done.
//
// No text was lost, and that is exactly why nothing caught it:
// verifyLossless compares a MULTISET OF LINES, so document order — the only
// thing linking a marker to the entry it discharges — is invisible to it.
// The guarantee was true of the text and false of the relation the text
// depended on.
//
// Absorbing rather than interpreting is what keeps this honest. The line
// stays verbatim in the body it belonged to all along, so the losslessness
// check still sees it exactly once; all that changes is which record it
// lands in. Both vocabularies are closed and structural — a bracketed
// `[Open]`/`[Closed]`/`[Superseded]` opening the bullet, or a `RESOLVED`
// clause opening it — and both are disjoint from the tags that legitimately
// open a record (`[Defer]`, `[Patch]`, `[Addendum, <date>]`), so this cannot
// swallow an entry.
//
// `Disposition recorded` was in the second vocabulary and is not any more;
// see inBodyClosureRe for why. A column-0 disposition bullet is therefore a
// record of its own again rather than being absorbed — which is right, since
// it no longer discharges anything, and the entry above keeps the open
// status the note says it still has. The one such line in the field is an
// indented sub-bullet, so it is ordinary continuation text and no boundary
// question arises at all.
//
// It absorbs only into a record that is actually open above it: a marker
// that opens a section has nothing to annotate and stays a record of its
// own, where applyStatusAnnotation and applyClosureMarker can still read
// it.
func absorbAnnotation(line string, sawBoundary bool, pending string) bool {
	return sawBoundary &&
		strings.TrimSpace(pending) != "" &&
		(statusAnnotationRe.MatchString(line) || inBodyClosureRe.MatchString(line))
}

// fenceRe matches a code-fence delimiter at column 0. An indented fence
// is already inside a bullet's continuation, where nothing is a boundary
// anyway.
var fenceRe = regexp.MustCompile("^(?:```|~~~)")

// orphanHeadingsNote tells a reader why headings are sitting at the end of
// the preamble file.
const orphanHeadingsNote = "<!-- Headings from the ledger that titled no record. A heading is lifted " +
	"into frontmatter, and one with nothing under it has no frontmatter to be lifted into — kept " +
	"here rather than dropped. -->"

// headingTitle renders a heading line as a record title.
func headingTitle(line string) string {
	return strings.TrimSpace(strings.TrimLeft(line, "# \t"))
}

type sectionContext struct {
	source string
	story  string
	date   string
}

// sectionFrom reads provenance out of a `## Deferred from:` heading's
// remainder, extracting each field independently so one unparseable part
// cannot take the others down with it.
func sectionFrom(label string) sectionContext {
	var ctx sectionContext
	if m := sectionSlugRe.FindAllStringSubmatch(label, -1); len(m) > 0 {
		// The last `of <token>` that looks like a story key — one starting
		// with a digit — and the FIRST `of` otherwise.
		//
		// Neither half alone is enough. Taking the last unconditionally
		// reads a slug out of ordinary prose ("of 91-2, deemed out of
		// scope" -> "scope"), which is FW-1a's failure mode arriving
		// through a different door: a confidently wrong value that reads as
		// authoritative. Taking the first breaks a label that qualifies
		// itself before naming the story. Story keys start with a digit and
		// the prose words that follow `of` do not, so the digit test picks
		// the real one whenever there is one, and the first-`of` fallback
		// keeps a non-numeric key like `epic-12` working.
		ctx.story = strings.Trim(strings.TrimSpace(m[0][1]), "(),;:.")
		for _, match := range m {
			token := strings.Trim(strings.TrimSpace(match[1]), "(),;:.")
			if token != "" && token[0] >= '0' && token[0] <= '9' {
				ctx.story = token
			}
		}
	}
	if dates := isoDateRe.FindAllString(label, -1); len(dates) > 0 {
		// The last ISO date anywhere in the heading. A prose parenthetical
		// can carry an earlier one; the filing date is the one at the end.
		ctx.date = dates[len(dates)-1]
	}
	ctx.source = classifySource(label)
	if ctx.source == SourceCorrectCourse {
		// A correct-course heading names an epic, not a story.
		ctx.story = ""
	}
	return ctx
}

// Migrate converts the legacy ledger into record files.
//
// Required properties, all four asserted rather than assumed:
//
//   - VERIFIED BEFORE WRITE, AGAINST THE LEDGER. Every significant line of
//     the source must come back out in something this writes — a record
//     body, a record's `source_heading`, or the preamble file — N records
//     in must equal N files out, and every body must survive a
//     render/parse round trip, or nothing is written at all. A lossy
//     conversion that passed silently is the one failure here that is not
//     recoverable from git.
//
//     LINE ENDINGS ARE THE ONE NORMALISATION. Bodies are byte-identical
//     modulo CRLF: the line scanner drops a trailing carriage return, so a
//     CRLF ledger yields LF bodies. Both sides of the check strip it, so
//     the assertion holds; "byte-for-byte" means "byte-for-byte after line
//     endings are normalised", and on this repo's history that distinction
//     has been worth stating rather than assuming.
//
//   - IDEMPOTENT, detected from disk state (does deferred/ exist, is the
//     legacy file already a stub). No stored version marker, so there is
//     nothing to drift.
//
//   - NEVER DELETES THE SOURCE. The legacy file becomes a short stub
//     pointing at the directory; its content stays in git.
//
//   - NO COMMIT. The files land in the working tree and the operator
//     commits, as one commit or two, however they like.
func (s *Store) Migrate(ctx context.Context, opts MigrateOptions) (*MigrateResult, error) {
	res := &MigrateResult{
		From: opts.From, To: s.Dir, DryRun: opts.DryRun,
		RecoverRequested: opts.RecoverDeleted,
	}

	if done, err := s.migrationDone(opts.From); err != nil {
		return nil, err
	} else if done {
		res.AlreadyDone = true
		return res, nil
	}

	if !opts.DryRun {
		if err := s.refuseIfPopulated(); err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(opts.From)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", opts.From, err)
	}

	doc := ParseLegacyDocument(data)
	records := doc.Records
	res.RecordsIn = len(records)
	res.Bytes = len(data)
	res.PreambleBytes = len(doc.Preamble)
	res.OrphanHeadings = len(doc.OrphanHeadings)
	for i := range records {
		if records[i].FreeForm {
			res.FreeForm++
		}
		if !records[i].IsOpen() {
			res.Closed++
		}
	}

	if err := verifyBeforeWrite(data, doc); err != nil {
		return nil, err
	}

	if opts.RecoverDeleted {
		recovered, warnings := s.recoverDeleted(ctx, opts.From, records)
		res.Recovered = len(recovered)
		res.Warnings = append(res.Warnings, warnings...)
		if !opts.DryRun {
			for i := range recovered {
				path, writeErr := s.writeClosed(recovered[i])
				if writeErr != nil {
					return nil, writeErr
				}
				res.Paths = append(res.Paths, path)
			}
		}
	}

	if opts.DryRun {
		res.RecordsOut = res.RecordsIn
		return res, nil
	}

	for i := range records {
		write := s.Write
		if !records[i].IsOpen() {
			// A record that arrived already discharged goes straight to
			// closed/ — it is history, and putting it in the open working
			// set is how the ledger accumulated resolved items in the
			// first place.
			write = s.writeClosed
		}
		path, writeErr := write(records[i])
		if writeErr != nil {
			return nil, writeErr
		}
		res.Paths = append(res.Paths, path)
		res.RecordsOut++
	}

	if doc.Preamble != "" || len(doc.OrphanHeadings) > 0 {
		path, writeErr := s.writePreamble(doc)
		if writeErr != nil {
			return nil, writeErr
		}
		res.Paths = append(res.Paths, path)
		res.PreambleWritten = true
	}

	// The post-write count assertion. Belt to the pre-write braces: this
	// is what catches a filename collision silently overwriting a record.
	loaded, err := s.Load(LoadOptions{IncludeClosed: true})
	if err != nil {
		return nil, err
	}
	if want := res.RecordsIn + res.Recovered; len(loaded.Records) != want {
		return nil, fmt.Errorf("%w: %d records parsed and %d recovered but %d on disk after writing",
			ErrLosslessnessFailed, res.RecordsIn, res.Recovered, len(loaded.Records))
	}

	if err := s.writeStub(opts.From, doc); err != nil {
		return nil, err
	}
	res.StubWritten = true
	res.Paths = append(res.Paths, opts.From)

	if err := s.RebuildIndex(); err != nil {
		return nil, err
	}
	if err := s.writeGitignore(); err != nil {
		return nil, err
	}
	return res, nil
}

// refuseIfPopulated rejects a migration into a store that already holds
// records, before anything is written.
func (s *Store) refuseIfPopulated() error {
	existing, err := s.Load(LoadOptions{IncludeClosed: true})
	if err != nil {
		return err
	}
	if len(existing.Records) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: %s holds %d record(s) from an earlier migration. To re-migrate, "+
			"restore the ledger from git and remove the store directory first — "+
			"record ids are derived from the ledger, so a corrected parse produces "+
			"different ids and the two sets cannot be merged",
		ErrStorePopulated, s.Dir, len(existing.Records))
}

// verifyBeforeWrite is the whole pre-write assertion, in two halves.
//
// FIRST, against the ledger: every significant source line has to come
// back out. This is the half that was missing, and its absence is not
// theoretical — a parser that inherited stale section context, turned
// headings into records and swallowed the preamble produced a completely
// wrong store, and the migration reported clean success on it, because
// nothing here ever compared the output to the input. The field project
// found the corruption by building this exact check by hand, afterwards.
// It belongs in front of the write.
//
// SECOND, per record: render it, parse it back, require byte identity.
// Record content cannot currently defeat this, and that is deliberate:
// Render always emits the frontmatter's own closing `---` before the body,
// so Split's first-closer-wins rule can never eat into it. It stays as a
// guard on future changes to either function.
func verifyBeforeWrite(data []byte, doc *LegacyDocument) error {
	if err := verifyLossless(data, doc); err != nil {
		return err
	}
	for i := range doc.Records {
		rendered, err := Render(doc.Records[i])
		if err != nil {
			return fmt.Errorf("%w: %w", ErrLosslessnessFailed, err)
		}
		_, body, err := frontmatter.Split(rendered)
		if err != nil {
			return fmt.Errorf("%w: record %s does not round-trip: %w",
				ErrLosslessnessFailed, doc.Records[i].ID, err)
		}
		if string(body) != doc.Records[i].Body {
			return fmt.Errorf("%w: record %s body changed on round trip",
				ErrLosslessnessFailed, doc.Records[i].ID)
		}
	}
	return nil
}

// verifyLossless compares the ledger against everything the migration is
// about to emit, as a multiset of lines.
//
// A multiset rather than a diff, and in BOTH directions: it catches a line
// dropped, a line duplicated into two records, and a line the parser
// invented, without caring which record any line landed in. Ordering and
// record boundaries are the parser's job to get right; not losing text is
// the property that has to hold even when it gets them wrong.
//
// THERE IS NO EXEMPT LINE CLASS. Heading lines leave the body — they
// become provenance or a title — but they are counted here all the same,
// from doc.Headings for the ones that produced a record and from
// doc.OrphanHeadings for the ones that did not. An exemption that is
// merely ignored is a hole the check cannot see through: it was possible
// for a heading-shaped line to be deleted silently and still pass, which
// is precisely what a `##` comment inside a pasted code fence did.
//
// EVERY BUCKET IS AN OUTPUT, which is what makes this a statement about
// the migration rather than about the parse. Records become files,
// Preamble and OrphanHeadings become PREAMBLE.md, and Headings is written
// verbatim to `source_heading` on each record it provenances. A bucket
// that were merely counted here and then discarded at write time would
// make this check read as a guarantee it does not give.
func verifyLossless(data []byte, doc *LegacyDocument) error {
	source := significantLines(string(data))
	emitted := map[string]int{}
	add := func(text string) {
		for line, n := range significantLines(text) {
			emitted[line] += n
		}
	}
	for i := range doc.Records {
		add(doc.Records[i].Body)
	}
	add(doc.Preamble)
	for _, heading := range doc.Headings {
		add(heading)
	}
	for _, heading := range doc.OrphanHeadings {
		add(heading)
	}

	for line, want := range source {
		if got := emitted[line]; got != want {
			return fmt.Errorf(
				"%w: the ledger has %d copy/copies of a line and the migration emits %d: %q",
				ErrLosslessnessFailed, want, got, line)
		}
	}
	for line, got := range emitted {
		if want := source[line]; got != want {
			return fmt.Errorf(
				"%w: the migration emits %d copy/copies of a line the ledger has %d of: %q",
				ErrLosslessnessFailed, got, want, line)
		}
	}
	return nil
}

// significantLines counts the lines that carry content.
//
// Blank lines are separation rather than content, and are reflowed around
// record boundaries by design. A trailing carriage return is stripped so a
// CRLF ledger compares against bodies the line scanner has already
// normalised, rather than reporting every single line as lost — the one
// respect in which a body is not literally byte-identical to its source.
func significantLines(text string) map[string]int {
	out := map[string]int{}
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		out[line]++
	}
	return out
}

// PreambleFileName holds the legacy ledger's own header — the banners and
// reconciliation notes that sat above the first record.
//
// It is preserved rather than dropped because the preamble is document
// history, and losing history on a migration is precisely the failure this
// store was built to end. It is not a record: it has no id, no status and
// nothing to discharge, and the loader skips it by name.
const PreambleFileName = "PREAMBLE.md"

// writePreamble stores the ledger prose that is not a record.
func (s *Store) writePreamble(doc *LegacyDocument) (string, error) {
	var b strings.Builder
	b.WriteString("<!-- Preserved verbatim from the legacy deferred-work.md. " +
		"Document history, not a deferred record: it has no id and nothing to discharge. -->\n\n")
	b.WriteString(doc.Preamble)
	if len(doc.OrphanHeadings) > 0 {
		if doc.Preamble != "" && !strings.HasSuffix(doc.Preamble, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n" + orphanHeadingsNote + "\n\n")
		b.WriteString(strings.Join(doc.OrphanHeadings, "\n") + "\n")
	}
	path := filepath.Join(s.Dir, PreambleFileName)
	if err := writeFileAtomic(path, []byte(b.String())); err != nil {
		return "", err
	}
	return path, nil
}

// migrationDone detects the post-migration state from disk alone: the
// store exists with records in it, and the legacy file is already a stub.
// No version marker is stored, so nothing can drift out of sync.
func (s *Store) migrationDone(legacy string) (bool, error) {
	loaded, err := s.Load(LoadOptions{})
	if err != nil {
		return false, err
	}
	if len(loaded.Records) == 0 {
		return false, nil
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		if os.IsNotExist(err) {
			// Records exist and the legacy file is gone: migrated (and the
			// stub removed by hand, which is the operator's business).
			return true, nil
		}
		return false, err
	}
	return bytes.Contains(data, []byte(stubMarker)), nil
}

// stubMarker identifies a legacy file that has already been replaced by a
// signpost. Its presence is the idempotency check.
const stubMarker = "<!-- ape:deferred-migrated -->"

// preambleSentence describes what PREAMBLE.md holds, or says nothing at
// all when the migration did not write one.
//
// The sentence used to be unconditional, so a ledger that opens on its
// first heading — no preamble to preserve, nothing written — still got a
// stub pointing at a `PREAMBLE.md` that does not exist. Two of the three
// field ledgers are that shape. A signpost's whole job is to be true about
// where things went.
func preambleSentence(rel string, doc *LegacyDocument) string {
	hasPreamble := doc.Preamble != ""
	hasOrphans := len(doc.OrphanHeadings) > 0
	target := rel + "/" + PreambleFileName
	switch {
	case hasPreamble && hasOrphans:
		return "\n\nThis file's own preamble — the banners and reconciliation notes that were\n" +
			"not deferred items — is preserved verbatim at " + target + ", together with\n" +
			"the headings that titled no record."
	case hasPreamble:
		return "\n\nThis file's own preamble — the banners and reconciliation notes that were\n" +
			"not deferred items — is preserved verbatim at " + target + "."
	case hasOrphans:
		return "\n\nThe headings in this file that titled no record are preserved at\n" + target + "."
	default:
		// This ledger had no preamble and no orphan headings, so there is no
		// PREAMBLE.md and nothing to point at.
		return ""
	}
}

// writeStub replaces the legacy ledger with a short signpost. The file is
// never deleted: anything still reading the old path finds a pointer
// rather than silence, and the content stays in git.
func (s *Store) writeStub(legacy string, doc *LegacyDocument) error {
	rel := s.Dir
	if r, err := filepath.Rel(filepath.Dir(legacy), s.Dir); err == nil {
		rel = filepath.ToSlash(r)
	}
	stub := fmt.Sprintf(`# Deferred work

%s

This ledger has been migrated to one file per record:

    %s/

Why: as a single file it grew past the size a skill can read — and its only
eviction mechanism was deletion, so closing a record destroyed its own
audit trail.

    ape deferred list                 open records
    ape deferred list --status all    including closed ones
    ape deferred verify               invariants, plus candidates for a human
    ape deferred close <id> --by "…"  discharge one

Closed records move to %s/closed/ and stay there.%s

The full history of this file, including every record ever removed from it,
is in git.
`, stubMarker, rel, rel, preambleSentence(rel, doc))
	return writeFileAtomic(legacy, []byte(stub))
}

// writeGitignore marks the derived index as untracked. It is rebuilt on
// read, so a committed copy could only ever be stale.
func (s *Store) writeGitignore() error {
	path := filepath.Join(s.Dir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	body := "# Derived from the record files and rebuilt on read.\n" + IndexFileName + "\n"
	return writeFileAtomic(path, []byte(body))
}

// writeClosed writes a record straight into closed/.
func (s *Store) writeClosed(rec Record) (string, error) {
	if err := os.MkdirAll(s.ClosedDir(), 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", s.ClosedDir(), err)
	}
	data, err := Render(rec)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.ClosedDir(), rec.FileName())
	if err := writeFileAtomic(path, data); err != nil {
		return "", err
	}
	return path, nil
}

// recoverDeleted mines the ledger's git history for records that are no
// longer in the live file, and returns them as tombstones.
//
// The ledger's own preamble documents `git log -p` as the recovery route
// for evicted records; this automates exactly that. Failure is a warning,
// never fatal: recovery is a bonus, and a repo without history still
// migrates.
func (s *Store) recoverDeleted(ctx context.Context, legacy string, live []Record) (recovered []Record, warnings []string) {
	dir := filepath.Dir(legacy)
	revs, err := gitOutput(ctx, dir, "log", "--format=%H", "--", legacy)
	if err != nil {
		return nil, []string{fmt.Sprintf("git history unavailable, nothing recovered: %v", err)}
	}
	rel, err := gitRelative(ctx, dir, legacy)
	if err != nil {
		return nil, []string{fmt.Sprintf("cannot resolve %s inside the repo: %v", legacy, err)}
	}

	// Matching is by normalised TITLE, not by id: a record's id is derived
	// from its section date, and a historical copy carries the same title
	// under a heading that may have been edited since.
	liveTitles := map[string]bool{}
	for i := range live {
		liveTitles[normaliseTitle(live[i].Title)] = true
	}

	// revShortLen keeps the provenance note readable without pretending a
	// 40-character sha adds information here.
	const revShortLen = 12
	seen := map[string]bool{}
	for rev := range strings.FieldsSeq(revs) {
		blob, err := gitOutput(ctx, dir, "show", rev+":"+rel)
		if err != nil {
			continue // the file did not exist at that revision
		}
		historical := ParseLegacy([]byte(blob))
		for i := range historical {
			rec := &historical[i]
			key := normaliseTitle(rec.Title)
			if key == "" || liveTitles[key] || seen[key] {
				continue
			}
			seen[key] = true
			note := "recovered from git history (" + rev[:min(len(rev), revShortLen)] + ")"
			// A historical copy can arrive ALREADY discharged, because the
			// parse reads the same closure markers the live one does. Keep
			// that verdict and append the recovery to its own reason rather
			// than overwriting it: replacing it would trade WHY the record
			// left the working set for merely WHERE it was found, and on a
			// discarded record it would assert a delivery that never
			// happened — the precise confusion `discarded` exists to prevent.
			switch {
			case rec.IsOpen():
				rec.Status = StatusClosed
				rec.ResolvedBy = note
			case rec.ResolvedBy != "":
				rec.ResolvedBy += "; " + note
			case rec.DiscardReason != "":
				rec.DiscardReason += "; " + note
			}
			recovered = append(recovered, *rec)
		}
	}
	return recovered, warnings
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (%s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func gitRelative(ctx context.Context, dir, path string) (string, error) {
	top, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolveForGit(strings.TrimSpace(top)), resolveForGit(abs))
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// resolveForGit normalises a path so filepath.Rel can be compared against
// git's own idea of the repo root.
//
// `git rev-parse --show-toplevel` reports a fully resolved path; the path
// ape holds may not be. On Windows a temp dir is handed out in 8.3 short
// form (C:\Users\RUNNER~1\...) while git reports the long name, and on
// macOS /var is a symlink to /private/var. Either mismatch makes
// filepath.Rel return a "../../.." walk, the `git show HEAD:<rel>` that
// follows fails, and the caller reads that as "nothing committed to
// compare" — a silent skip rather than an error, which is how this went
// unnoticed until the first Windows CI run.
//
// EvalSymlinks resolves both forms. A path that cannot be resolved is
// returned unchanged: the comparison may still work, and failing here
// would turn a best-effort check into a hard error.
func resolveForGit(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// Recover mines the ledger's git history for records the store does not
// hold, and writes them to closed/ as tombstones.
//
// This is Migrate's --recover-deleted, reachable AFTER a migration. It has
// to exist separately because Migrate short-circuits on AlreadyDone before
// it ever reaches its recovery branch, so on a migrated project that flag
// is silently inert — and the route the AlreadyDone message names (restore
// the ledger, delete the store, re-migrate) throws away every close,
// discard and repair edit made since. For a project that has committed its
// store, this is the only path that does not cost work.
//
// TWO DIFFERENCES FROM THE MIGRATION'S RECOVERY, both consequences of
// running later:
//
//   - It de-duplicates against what is ON DISK (open records and existing
//     tombstones), not against a fresh parse of the ledger. Those are the
//     same set immediately after a migration and diverge afterwards, and
//     the store is the one that stays true.
//   - The path it reads is normally a STUB by now. That is fine and is the
//     point: git history is read through the path, not out of the file
//     sitting at it, so every revision that held the real ledger is still
//     reachable.
//
// Recovery only ever writes to closed/. It cannot put anything into the
// open working set, which is what makes it safe to run more than once —
// a second run recovers nothing, because the first run's tombstones are
// now part of the set it de-duplicates against.
func (s *Store) Recover(ctx context.Context, opts RecoverOptions) (*RecoverResult, error) {
	res := &RecoverResult{From: opts.From, To: s.ClosedDir(), DryRun: opts.DryRun}
	if opts.From == "" {
		return nil, errors.New("no ledger path to read history through")
	}
	if _, err := os.Stat(opts.From); err != nil {
		return nil, fmt.Errorf("read %s: %w", opts.From, err)
	}

	loaded, err := s.Load(LoadOptions{IncludeClosed: true})
	if err != nil {
		return nil, err
	}
	res.Existing = len(loaded.Records)

	recovered, warnings := s.recoverDeleted(ctx, opts.From, loaded.Records)
	res.Warnings = append(res.Warnings, warnings...)
	res.Recovered = len(recovered)
	res.Records = recovered
	if opts.DryRun {
		return res, nil
	}
	for i := range recovered {
		path, writeErr := s.writeClosed(recovered[i])
		if writeErr != nil {
			return nil, writeErr
		}
		res.Paths = append(res.Paths, path)
	}
	if len(recovered) > 0 {
		if err := s.RebuildIndex(); err != nil {
			return nil, err
		}
	}
	return res, nil
}
