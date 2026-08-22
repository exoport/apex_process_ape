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

// sectionRe matches the legacy ledger's provenance headings:
//
//	## Deferred from: story review of 54-1 (2026-08-21)
//	## Deferred from: apex-correct-course reconciliation of epic 12 (2026-07-02)
var sectionRe = regexp.MustCompile(
	`(?i)^##\s+Deferred from:\s*(.*?)(?:\s+of\s+(\S+))?\s*(?:\(([\d-]+)\))?\s*$`)

// MigrateOptions controls one migration.
type MigrateOptions struct {
	// From is the legacy ledger. Required.
	From string
	// RecoverDeleted mines git history for records removed from the ledger
	// and writes them straight to closed/.
	RecoverDeleted bool
	// DryRun parses and verifies, writing nothing.
	DryRun bool
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
	// Paths is every file written, for the caller's `git add` line.
	Paths []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	// StubWritten records that the legacy file became a signpost.
	StubWritten bool     `json:"stub_written"       yaml:"stub_written"`
	DryRun      bool     `json:"dry_run"            yaml:"dry_run"`
	AlreadyDone bool     `json:"already_done"       yaml:"already_done"`
	Warnings    []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// ErrLosslessnessFailed reports that the verify-before-write assertion did
// not hold. Nothing is written when this is returned.
var ErrLosslessnessFailed = errors.New("losslessness assertion failed")

// ParseLegacy splits the legacy ledger into records.
//
// The mapping is fixed, because this is the only place losslessness can be
// lost. A record whose tail does not match keeps its full text as the body
// and takes its title from the first line — 26 of 109 real records are
// free-form, so that path always runs. Nothing is dropped and nothing is
// guessed at.
func ParseLegacy(data []byte) []Record {
	var (
		records []Record
		section sectionContext
		pending strings.Builder
	)
	flush := func() {
		text := pending.String()
		pending.Reset()
		if strings.TrimSpace(text) == "" {
			return
		}
		rec := ParseBullet(text)
		rec.Source = section.source
		rec.SourceStory = section.story
		rec.Created = section.date
		rec.Status = StatusOpen
		rec.ID = NewID(section.date, rec.Title, rec.Body)
		records = append(records, rec)
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
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			flush()
			startedList = false
			section = sectionFrom(m)
			continue
		}
		switch {
		case topLevelBulletRe.MatchString(line):
			flush()
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

	// De-duplicate ids that collide because two records in the same
	// section have identical text. Both are kept: the operator wrote two.
	seen := map[string]int{}
	for i := range records {
		id := records[i].ID
		if n := seen[id]; n > 0 {
			records[i].ID = fmt.Sprintf("%s-%d", id, n+1)
		}
		seen[id]++
	}
	return records
}

type sectionContext struct {
	source string
	story  string
	date   string
}

func sectionFrom(m []string) sectionContext {
	label := strings.ToLower(strings.TrimSpace(m[1]))
	ctx := sectionContext{story: strings.TrimSpace(m[2]), date: strings.TrimSpace(m[3])}
	switch {
	case strings.Contains(label, "correct-course"):
		ctx.source = SourceCorrectCourse
		// A correct-course heading names an epic, not a story.
		ctx.story = ""
	case strings.Contains(label, "story review"), strings.Contains(label, "review"):
		ctx.source = SourceStoryReview
	default:
		ctx.source = SourceUnknown
	}
	return ctx
}

// Migrate converts the legacy ledger into record files.
//
// Required properties, all four asserted rather than assumed:
//
//   - VERIFIED BEFORE WRITE. N records in must equal N files out and every
//     body must be byte-identical, or nothing is written at all. A lossy
//     conversion that passed silently is the one failure here that is not
//     recoverable from git.
//   - IDEMPOTENT, detected from disk state (does deferred/ exist, is the
//     legacy file already a stub). No stored version marker, so there is
//     nothing to drift.
//   - NEVER DELETES THE SOURCE. The legacy file becomes a short stub
//     pointing at the directory; its content stays in git.
//   - NO COMMIT. The files land in the working tree and the operator
//     commits, as one commit or two, however they like.
func (s *Store) Migrate(ctx context.Context, opts MigrateOptions) (*MigrateResult, error) {
	res := &MigrateResult{From: opts.From, To: s.Dir, DryRun: opts.DryRun}

	if done, err := s.migrationDone(opts.From); err != nil {
		return nil, err
	} else if done {
		res.AlreadyDone = true
		return res, nil
	}

	data, err := os.ReadFile(opts.From)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", opts.From, err)
	}

	records := ParseLegacy(data)
	res.RecordsIn = len(records)
	for i := range records {
		if records[i].FreeForm {
			res.FreeForm++
		}
	}

	// Verify BEFORE writing: render each record, parse it back, and
	// require the body to survive byte-for-byte.
	//
	// Record content cannot currently defeat this, and that is deliberate:
	// Render always emits the frontmatter's own closing `---` before the
	// body, so Split's first-closer-wins rule can never eat into it. The
	// check stays as a guard on future changes to either function — the one
	// failure in this migration that git could not undo is a lossy
	// conversion that passed silently.
	for i := range records {
		rendered, renderErr := Render(records[i])
		if renderErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrLosslessnessFailed, renderErr)
		}
		_, body, splitErr := frontmatter.Split(rendered)
		if splitErr != nil {
			return nil, fmt.Errorf("%w: record %s does not round-trip: %w",
				ErrLosslessnessFailed, records[i].ID, splitErr)
		}
		if string(body) != records[i].Body {
			return nil, fmt.Errorf("%w: record %s body changed on round trip",
				ErrLosslessnessFailed, records[i].ID)
		}
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
		path, writeErr := s.Write(records[i])
		if writeErr != nil {
			return nil, writeErr
		}
		res.Paths = append(res.Paths, path)
		res.RecordsOut++
	}

	// The post-write count assertion. Belt to the pre-write braces: this
	// is what catches a filename collision silently overwriting a record.
	loaded, err := s.Load(LoadOptions{})
	if err != nil {
		return nil, err
	}
	if len(loaded.Records) != res.RecordsIn {
		return nil, fmt.Errorf("%w: %d records parsed but %d on disk after writing",
			ErrLosslessnessFailed, res.RecordsIn, len(loaded.Records))
	}

	if err := s.writeStub(opts.From); err != nil {
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

// writeStub replaces the legacy ledger with a short signpost. The file is
// never deleted: anything still reading the old path finds a pointer
// rather than silence, and the content stays in git.
func (s *Store) writeStub(legacy string) error {
	rel := s.Dir
	if r, err := filepath.Rel(filepath.Dir(legacy), s.Dir); err == nil {
		rel = filepath.ToSlash(r)
	}
	stub := fmt.Sprintf(`# Deferred work

%s

This ledger has been migrated to one file per record:

    %s/

Why: as a single file it reached 456,144 bytes, which is past the size a
skill can read — and its only eviction mechanism was deletion, so closing
a record destroyed its own audit trail.

    ape deferred list                 open records
    ape deferred list --status all    including closed ones
    ape deferred verify               invariants, plus candidates for a human
    ape deferred close <id> --by "…"  discharge one

Closed records move to %s/closed/ and stay there. The full history of this
file, including every record ever removed from it, is in git.
`, stubMarker, rel, rel)
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
			rec.Status = StatusClosed
			rec.ResolvedBy = "recovered from git history (" + rev[:min(len(rev), revShortLen)] + ")"
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
	rel, err := filepath.Rel(strings.TrimSpace(top), abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
