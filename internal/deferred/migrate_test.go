package deferred

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// legacyLedger exercises every row of the migration mapping table:
// both section-heading forms, the three bullet shapes, a citation bracket,
// the `— defer:` tail, owner=, trigger:, next-batch brief:, and a
// free-form record with no recognisable shape at all.
const legacyLedger = "# Deferred Work\n" +
	"\n" +
	"Struck entries are DELETED rather than tombstoned; use `git log -p` to recover one.\n" +
	"\n" +
	"## Deferred from: story review of 54-1 (2026-08-21)\n" +
	"\n" +
	"- [ ] [Defer] Tighten `resolveModelArg` when `--model` is empty " +
	"[internal/apecmd/modelarg.go:42] — defer: outside-story=yes ; cross-cycle=no ; " +
	"non-blocking=yes ; owner=platform ; trigger: story 54-2 lands\n" +
	"- [Defer] Second thing [pkg/b.go:7] — defer: outside-story=no ; owner=data ; " +
	"next-batch brief: fold into the 55 batch\n" +
	"\n" +
	"## Deferred from: apex-correct-course reconciliation of epic-12 (2026-07-02)\n" +
	"\n" +
	"- [x] [Defer] Already-checked entry [pkg/c.go:9]\n" +
	"\n" +
	"Someone pasted this paragraph in without any bullet or marker at all,\n" +
	"and it has to survive the migration verbatim.\n"

func writeLegacy(t *testing.T, body string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "deferred-work.md")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return dir, path
}

func TestParseLegacy_MappingTable(t *testing.T) {
	records := ParseLegacy([]byte(legacyLedger))
	require.Len(t, records, 4, "three bullets and the free-form paragraph; the preamble is not a record")

	byTitle := map[string]Record{}
	for _, rec := range records {
		byTitle[rec.Title] = rec
	}

	first := byTitle["Tighten `resolveModelArg` when `--model` is empty"]
	require.Equal(t, SourceStoryReview, first.Source)
	require.Equal(t, "54-1", first.SourceStory, "the heading's story key")
	require.Equal(t, "2026-08-21", first.Created, "the heading's date")
	require.Equal(t, []string{"internal/apecmd/modelarg.go:42"}, first.Anchors)
	require.Equal(t, "yes", first.OutsideStory)
	require.Equal(t, "no", first.CrossCycle)
	require.Equal(t, "yes", first.NonBlocking)
	require.Equal(t, "platform", first.Owner)
	require.Equal(t, "story 54-2 lands", first.Trigger)
	require.False(t, first.FreeForm)

	second := byTitle["Second thing"]
	require.Equal(t, "data", second.Owner)
	require.Equal(t, "fold into the 55 batch", second.NextAction)

	third := byTitle["Already-checked entry"]
	require.Equal(t, SourceCorrectCourse, third.Source)
	require.Empty(t, third.SourceStory, "a correct-course heading names an epic, not a story")
	require.Equal(t, "2026-07-02", third.Created)

	// The free-form paragraph survives with its text as the body.
	var freeForm int
	for _, rec := range records {
		if rec.FreeForm {
			freeForm++
		}
	}
	require.Equal(t, 1, freeForm, "the pasted paragraph — the preamble is no longer a record at all")
}

func TestParseLegacy_BodiesAreVerbatim(t *testing.T) {
	records := ParseLegacy([]byte(legacyLedger))
	for _, rec := range records {
		require.Contains(t, legacyLedger, rec.Body,
			"every record's body must be a verbatim slice of the source")
	}
}

// TestMigrate_LosslessnessAndStub is the deliverable's core assertion.
func TestMigrate_LosslessnessAndStub(t *testing.T) {
	dir, legacy := writeLegacy(t, legacyLedger)
	s := New(filepath.Join(dir, "deferred"))

	res, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)
	require.Equal(t, res.RecordsIn, res.RecordsOut, "N in must equal N out")
	require.Equal(t, 4, res.RecordsIn)
	require.Equal(t, 1, res.FreeForm)
	require.True(t, res.StubWritten)

	loaded, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, loaded.Records, 4)
	require.Empty(t, loaded.Warnings, "every written record must read back cleanly")

	// Every body is byte-identical to a slice of the original ledger.
	for i := range loaded.Records {
		require.Contains(t, legacyLedger, loaded.Records[i].Body)
	}

	// The source is never deleted — it becomes a signpost.
	stub, err := os.ReadFile(legacy)
	require.NoError(t, err)
	require.Contains(t, string(stub), stubMarker)
	require.Contains(t, string(stub), "ape deferred list")
	require.Less(t, len(strings.Split(string(stub), "\n")), 40, "a stub, not a document")

	// And the derived index is gitignored.
	ignore, err := os.ReadFile(filepath.Join(s.Dir, ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(ignore), IndexFileName)
}

// TestMigrate_IsIdempotentFromDiskState: no version marker is stored, so
// the second run detects the post-migration shape and does nothing.
func TestMigrate_IsIdempotentFromDiskState(t *testing.T) {
	dir, legacy := writeLegacy(t, legacyLedger)
	s := New(filepath.Join(dir, "deferred"))

	first, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)
	require.False(t, first.AlreadyDone)

	second, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)
	require.True(t, second.AlreadyDone)
	require.Zero(t, second.RecordsOut)

	loaded, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, loaded.Records, 4, "no duplicates from the second run")
}

func TestMigrate_DryRunWritesNothing(t *testing.T) {
	dir, legacy := writeLegacy(t, legacyLedger)
	s := New(filepath.Join(dir, "deferred"))
	before, err := os.ReadFile(legacy)
	require.NoError(t, err)

	res, err := s.Migrate(context.Background(), MigrateOptions{From: legacy, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, 4, res.RecordsIn)
	require.True(t, res.DryRun)
	require.False(t, res.StubWritten)

	_, err = os.Stat(s.Dir)
	require.Error(t, err, "the store was not created")
	after, err := os.ReadFile(legacy)
	require.NoError(t, err)
	require.Equal(t, before, after, "the ledger is untouched")
}

// TestMigrate_AdversarialBodiesRoundTrip is the losslessness property
// stated positively.
//
// The pre-write assertion in Migrate cannot be triggered by record CONTENT,
// and that is the point: Render always emits the frontmatter's own closing
// `---` before the body, so Split's first-closer-wins rule can never eat
// into it. These are the bodies most likely to break a naive
// implementation — a body that opens with a delimiter, one containing
// `---` lines, YAML-looking prose, backticks, the em-dash tail — and every
// one survives byte-for-byte. The assertion stays as a guard on future
// changes to Render or Split, not because content can defeat it today.
func TestMigrate_AdversarialBodiesRoundTrip(t *testing.T) {
	bodies := map[string]string{
		"opens with a delimiter": "---\nnot really the end\n",
		"contains a delimiter":   "before\n---\nafter\n",
		"yaml-looking prose":     "id: not-the-real-id\nstatus: closed\n",
		"backticks and em-dash":  "`code` — defer: outside-story=yes\n",
		"checkbox marker":        "- [ ] [Defer] nested marker\n",
		"trailing whitespace":    "text   \n\t\n",
		"no trailing newline":    "abrupt end",
		"crlf":                   "line one\r\nline two\r\n",
		//nolint:gosmopolitan // a non-Latin body is exactly what this asserts survives
		"unicode": "— – … ✓ 日本語\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			rec := Record{ID: "DW-1", Title: "T", Status: StatusOpen, Body: body}
			rendered, err := Render(rec)
			require.NoError(t, err)

			path := filepath.Join(t.TempDir(), "rec.md")
			require.NoError(t, os.WriteFile(path, rendered, 0o644))
			back, err := ReadRecord(path)
			require.NoError(t, err)
			require.Equal(t, body, back.Body, "the body must survive byte-for-byte")
			require.Equal(t, "DW-1", back.ID, "and the frontmatter must not be eaten by the body")
			require.Equal(t, StatusOpen, back.Status)
		})
	}
}

// TestMigrate_PostWriteCountAssertion covers the other half of the
// verify-before-write pair: after writing, the number of records readable
// on disk must equal the number parsed. A filename collision is what this
// would catch.
func TestMigrate_PostWriteCountAssertion(t *testing.T) {
	dir, legacy := writeLegacy(t, legacyLedger)
	s := New(filepath.Join(dir, "deferred"))
	res, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)

	loaded, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, loaded.Records, res.RecordsIn,
		"every parsed record has its own file — no id or filename collision swallowed one")

	names := map[string]bool{}
	for i := range loaded.Records {
		name := filepath.Base(loaded.Records[i].Path)
		require.False(t, names[name], "filename %s used twice", name)
		names[name] = true
	}
}

func TestMigrate_MissingLedger(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "deferred"))
	_, err := s.Migrate(context.Background(), MigrateOptions{From: filepath.Join(dir, "nope.md")})
	require.Error(t, err)
}

// TestMigrate_RecoverDeleted mines git history for records the live ledger
// no longer holds. The ledger's own preamble documents `git log -p` as the
// recovery route; this is that, automated.
func TestMigrate_RecoverDeleted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	git("init", "-q")

	legacy := filepath.Join(dir, "deferred-work.md")
	withEvicted := "## Deferred from: story review of 1-1 (2026-01-01)\n\n" +
		"- [Defer] Survivor [a.go:1]\n" +
		"- [Defer] Evicted long ago [b.go:2]\n"
	require.NoError(t, os.WriteFile(legacy, []byte(withEvicted), 0o644))
	git("add", ".")
	git("commit", "-qm", "both records")

	// Now delete one, exactly as the real convention does.
	withoutEvicted := "## Deferred from: story review of 1-1 (2026-01-01)\n\n" +
		"- [Defer] Survivor [a.go:1]\n"
	require.NoError(t, os.WriteFile(legacy, []byte(withoutEvicted), 0o644))
	git("add", ".")
	git("commit", "-qm", "evict one")

	s := New(filepath.Join(dir, "deferred"))
	res, err := s.Migrate(ctx, MigrateOptions{From: legacy, RecoverDeleted: true})
	require.NoError(t, err)
	require.Equal(t, 1, res.RecordsIn, "one live record")
	require.Equal(t, 1, res.Recovered, "and one recovered from history")

	all, err := s.Load(LoadOptions{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, all.Records, 2)

	var recovered *Record
	for i := range all.Records {
		if all.Records[i].Title == "Evicted long ago" {
			recovered = &all.Records[i]
		}
	}
	require.NotNil(t, recovered, "the evicted record came back")
	require.Equal(t, StatusClosed, recovered.Status, "as a tombstone, not an open item")
	require.Contains(t, recovered.ResolvedBy, "recovered from git history")
	require.Contains(t, recovered.Path, ClosedDirName)

	// The open set is unchanged by recovery.
	open, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, open.Records, 1)
}

func TestMigrate_RecoverDeletedOutsideARepoIsAWarning(t *testing.T) {
	dir, legacy := writeLegacy(t, legacyLedger)
	s := New(filepath.Join(dir, "deferred"))

	res, err := s.Migrate(context.Background(), MigrateOptions{From: legacy, RecoverDeleted: true})
	require.NoError(t, err, "no history is a warning, never fatal")
	require.Zero(t, res.Recovered)
	require.NotEmpty(t, res.Warnings)
	require.Contains(t, res.Warnings[0], "git history unavailable")
	require.Equal(t, 4, res.RecordsOut, "and the migration still completes")
}

func TestSectionFrom(t *testing.T) {
	cases := []struct {
		line   string
		source string
		story  string
		date   string
	}{
		{"## Deferred from: story review of 54-1 (2026-08-21)", SourceStoryReview, "54-1", "2026-08-21"},
		{
			"## Deferred from: apex-correct-course reconciliation of epic-12 (2026-07-02)",
			SourceCorrectCourse, "", "2026-07-02",
		},
		{"## Deferred from: something else entirely", SourceUnknown, "", ""},

		// Slug and date are extracted INDEPENDENTLY, so neither can be
		// taken down by the other. The single-pattern version lost both to
		// a prose parenthetical: the date group failed, the lazy label
		// group backtracked, and it swallowed the slug on its way past.
		{
			"## Deferred from: story review of 86-1_create-branch-cas (retroactively backfilled by Story 89.2, 2026-07-26)",
			SourceStoryReview, "86-1_create-branch-cas", "2026-07-26",
		},
		{
			"## Deferred from: story review of 90-1 — no date on this one",
			SourceStoryReview, "90-1", "",
		},
		{
			"## Deferred from: story review (2026-07-28)",
			SourceStoryReview, "", "2026-07-28",
		},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			m := sectionRe.FindStringSubmatch(tc.line)
			require.NotNil(t, m, "the heading must match")
			ctx := sectionFrom(m[1])
			require.Equal(t, tc.source, ctx.source)
			require.Equal(t, tc.story, ctx.story)
			require.Equal(t, tc.date, ctx.date)
		})
	}
}

// TestMigrate_ReferenceScale is the reference project's shape: 109 live
// records across several sections.
func TestMigrate_ReferenceScale(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Deferred Work\n\n")
	for section := 1; section <= 10; section++ {
		fmt.Fprintf(&b, "## Deferred from: story review of %d-1 (2026-0%d-01)\n\n", section, (section%9)+1)
		for i := 1; i <= 11; i++ {
			fmt.Fprintf(&b, "- [ ] [Defer] Item %d-%d [pkg/f%d.go:%d] — defer: owner=team%d\n",
				section, i, section, i, i%4)
		}
		b.WriteString("\n")
	}
	dir, legacy := writeLegacy(t, b.String())
	s := New(filepath.Join(dir, "deferred"))

	res, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)
	require.Equal(t, res.RecordsIn, res.RecordsOut)
	require.Equal(t, 110, res.RecordsIn, "110 bullets; the `# Deferred Work` preamble is not one")

	loaded, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, loaded.Records, res.RecordsIn, "no id collision swallowed a record")

	report, err := s.Verify(VerifyOptions{})
	require.NoError(t, err)
	// Every bullet parses, and the preamble is no longer a record, so
	// nothing here is free-form.
	require.Zero(t, report.Summary.ByCheck[CheckFreeForm])
}

// TestMigrate_RefusesToWriteALossyParse promotes the field project's own
// harness into the migration.
//
// This is the assertion that was missing when the first real migration ran.
// `Migrate` advertised "every body must survive byte-for-byte, or NOTHING
// is written", but the only check it made was rendering each record and
// parsing it back — a serialiser round trip that never once compared the
// output to the ledger. So a parser that dropped the preamble into a
// record, inherited stale section context and turned headings into records
// reported a clean success on 124 records, and the corruption was found by
// hand afterwards.
//
// A parser defect is simulated here rather than waited for: the check has
// to fail on content that does not add up, whatever produced it.
func TestMigrate_RefusesToWriteALossyParse(t *testing.T) {
	doc := ParseLegacyDocument([]byte(legacyLedger))
	require.NoError(t, verifyLossless([]byte(legacyLedger), doc),
		"the real parse must pass, or the rest of this test proves nothing")

	// mutate copies doc, so each subtest starts from the good parse.
	mutate := func(f func(*LegacyDocument)) *LegacyDocument {
		next := &LegacyDocument{
			Records:  append([]Record{}, doc.Records...),
			Preamble: doc.Preamble,
			Headings: append([]string{}, doc.Headings...),
		}
		f(next)
		return next
	}

	t.Run("a dropped line", func(t *testing.T) {
		lossy := mutate(func(d *LegacyDocument) {
			d.Records[0].Body = "" // as if a record had been silently swallowed
		})
		require.ErrorIs(t, verifyLossless([]byte(legacyLedger), lossy), ErrLosslessnessFailed)
	})

	t.Run("a duplicated line", func(t *testing.T) {
		dup := mutate(func(d *LegacyDocument) {
			d.Records = append(d.Records, d.Records[0])
		})
		require.ErrorIs(t, verifyLossless([]byte(legacyLedger), dup), ErrLosslessnessFailed)
	})

	t.Run("an invented line", func(t *testing.T) {
		invented := mutate(func(d *LegacyDocument) {
			d.Records[0].Body += "a line the ledger never contained\n"
		})
		require.ErrorIs(t, verifyLossless([]byte(legacyLedger), invented), ErrLosslessnessFailed)
	})

	t.Run("a dropped preamble", func(t *testing.T) {
		dropped := mutate(func(d *LegacyDocument) { d.Preamble = "" })
		require.ErrorIs(t, verifyLossless([]byte(legacyLedger), dropped), ErrLosslessnessFailed,
			"dropping the preamble is losing content, not tidying up")
	})

	// The class the old check was blind to: a heading line leaves the body,
	// so if it is neither consumed into frontmatter nor kept as preamble it
	// has simply been deleted — and while headings were an ignored
	// exemption, that passed.
	t.Run("a deleted heading", func(t *testing.T) {
		deleted := mutate(func(d *LegacyDocument) { d.Headings = d.Headings[1:] })
		require.ErrorIs(t, verifyLossless([]byte(legacyLedger), deleted), ErrLosslessnessFailed)
	})
}

// TestMigrate_PreambleIsPreservedNotDropped: the ledger's own header stops
// being a record without becoming a deletion. Losing history on a
// migration is the exact failure this store exists to end, so "not a
// record" had to mean "kept somewhere that is not the record set".
func TestMigrate_PreambleIsPreservedNotDropped(t *testing.T) {
	dir, legacy := writeLegacy(t, legacyLedger)
	s := New(filepath.Join(dir, "deferred"))

	res, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)
	require.True(t, res.PreambleWritten)

	preamble, err := os.ReadFile(filepath.Join(s.Dir, PreambleFileName))
	require.NoError(t, err)
	require.Contains(t, string(preamble), "Struck entries are DELETED",
		"the preamble text survives verbatim")

	// And it is not a record: it must not appear in any load, or it would
	// be back in the working set under a different name.
	loaded, err := s.Load(LoadOptions{IncludeClosed: true})
	require.NoError(t, err)
	require.Empty(t, loaded.Warnings, "the preamble file must not be read as a malformed record")
	for i := range loaded.Records {
		require.NotContains(t, loaded.Records[i].Body, "Struck entries are DELETED")
	}
}

// TestMigrate_RefusesAPopulatedStore covers the re-migration path.
//
// A wrong section date is hashed into the record id, so that class of
// defect cannot be repaired in place — the ledger has to be re-migrated by
// a fixed parser. Re-running into a store that still holds the old records
// used to write the new ones alongside them and only then fail the
// post-write count assertion, leaving two migrations interleaved on disk.
func TestMigrate_RefusesAPopulatedStore(t *testing.T) {
	dir, legacy := writeLegacy(t, legacyLedger)
	s := New(filepath.Join(dir, "deferred"))

	_, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)

	// Restore the ledger, exactly as an operator re-migrating would: that
	// clears the stub marker, so the idempotency check no longer fires.
	require.NoError(t, os.WriteFile(legacy, []byte(legacyLedger), 0o644))

	_, err = s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.ErrorIs(t, err, ErrStorePopulated)
	require.Contains(t, err.Error(), "remove the store directory",
		"the refusal has to say how to proceed, or it is just a wall")

	// Nothing was written on top of the first migration.
	loaded, err := s.Load(LoadOptions{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, loaded.Records, 4, "the earlier migration is intact")

	// And removing the store is genuinely all it takes.
	require.NoError(t, os.RemoveAll(s.Dir))
	res, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)
	require.Equal(t, 4, res.RecordsOut)
}

// TestMigrate_ResolvedBannerLandsInClosed: a record that arrived already
// discharged is history, and the open working set is not where history
// goes. Accumulating resolved items in the live list is how the ledger got
// unreadable in the first place.
func TestMigrate_ResolvedBannerLandsInClosed(t *testing.T) {
	ledger := "# Deferred Work\n\n" +
		"## Deferred from: story review of 90-1 (2026-07-28)\n\n" +
		"> ✅ **RESOLVED — 2026-07-20, by Story 70.5's dev cycle 2, commit 71e2635.**\n\n" +
		"- [Defer] A genuinely open item [a.go:1]\n"
	dir, legacy := writeLegacy(t, ledger)
	s := New(filepath.Join(dir, "deferred"))

	res, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)
	require.Equal(t, 2, res.RecordsIn)
	require.Equal(t, 1, res.Closed)

	open, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, open.Records, 1, "only the live item is in the working set")
	require.Equal(t, "A genuinely open item", open.Records[0].Title)

	all, err := s.Load(LoadOptions{IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, all.Records, 2, "and the banner is on disk, not discarded")

	// verify must stay clean: a closed record with no resolved_by is a
	// schema violation, so the migration owes one.
	report, err := s.Verify(VerifyOptions{})
	require.NoError(t, err)
	require.True(t, report.OK(), "findings: %+v", report.Findings)
}

// TestParseLegacy_FencedCodeIsNotStructure: a `##` line inside a code
// fence is a code comment, not a section heading.
//
// This was a regression introduced by making the parser heading-aware,
// and the worst possible shape of one: the line was DELETED, the fence was
// split across two records with the opener in one and the closer in the
// other, and the pre-write check passed — because heading-shaped lines
// were the check's exempt class, so deleting one was invisible to it.
// Both halves are fixed here: fences suspend boundary detection, and the
// exemption is now counted rather than ignored.
func TestParseLegacy_FencedCodeIsNotStructure(t *testing.T) {
	ledger := "# Deferred Work\n\n" +
		"## Deferred from: story review of 1-1 (2026-01-01)\n\n" +
		"- [Defer] A real defer [a.go:1]\n\n" +
		"```make\n" +
		"## build everything\n" +
		"all: test lint\n" +
		"```\n"
	doc := ParseLegacyDocument([]byte(ledger))

	require.NoError(t, verifyLossless([]byte(ledger), doc))

	var withFence *Record
	for i := range doc.Records {
		if strings.Contains(doc.Records[i].Body, "## build everything") {
			withFence = &doc.Records[i]
		}
	}
	require.NotNil(t, withFence, "the `##` comment inside the fence must survive as body text")
	require.Contains(t, withFence.Body, "```make", "and the fence must not be split across records")
	require.Contains(t, withFence.Body, "```\n")
	require.NotContains(t, doc.Headings, "## build everything",
		"a fenced line must never be consumed as a section heading")

	// A tilde fence behaves identically, and a bullet inside a fence is
	// not a record boundary either.
	tilde := "## Deferred from: story review of 1-1 (2026-01-01)\n\n" +
		"- [Defer] Has a sample [a.go:1]\n\n" +
		"~~~\n- not a bullet, a sample\n## not a heading\n~~~\n"
	doc2 := ParseLegacyDocument([]byte(tilde))
	require.NoError(t, verifyLossless([]byte(tilde), doc2))
	require.Len(t, doc2.Records, 1, "nothing inside the fence starts a record")
}

// TestParseLegacy_OrphanHeadingIsKept: a heading with nothing under it
// titles no record, so it has no frontmatter to be lifted into. `## Still
// open` is noise and `## Deferred-at-decision: <a real title>` is not, and
// the parser cannot tell them apart — so it deletes neither.
func TestParseLegacy_OrphanHeadingIsKept(t *testing.T) {
	ledger := "# Deferred Work\n\n" +
		"## Deferred-at-decision: a substantive title worth keeping\n\n" +
		"## Deferred from: story review of 1-1 (2026-01-01)\n\n" +
		"- [Defer] The only record [a.go:1]\n"
	doc := ParseLegacyDocument([]byte(ledger))

	require.NoError(t, verifyLossless([]byte(ledger), doc))
	require.Len(t, doc.Records, 1)
	require.Equal(t, []string{"## Deferred-at-decision: a substantive title worth keeping"},
		doc.OrphanHeadings)
	require.Equal(t, []string{"## Deferred from: story review of 1-1 (2026-01-01)"},
		doc.Headings, "the heading that DID produce a record is accounted for separately")

	// It reaches disk, in the file for ledger prose that is not a record.
	dir, legacy := writeLegacy(t, ledger)
	s := New(filepath.Join(dir, "deferred"))
	_, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)
	preamble, err := os.ReadFile(filepath.Join(s.Dir, PreambleFileName))
	require.NoError(t, err)
	require.Contains(t, string(preamble), "a substantive title worth keeping")
	require.Contains(t, string(preamble), "titled no record", "with a note saying why it is there")
}

// TestParseBullet_ProseContinuationNeverJoinsTheFieldList: the wrap fix
// must not become the corruption it replaced.
//
// A semicolon looks like the field list's separator and is also ordinary
// punctuation, so accepting a bare `;` as evidence of field syntax put the
// truncation straight back as an over-read: `owner=alice` followed by "it
// broke; then we reverted it" became owner="alice it broke".
func TestParseBullet_ProseContinuationNeverJoinsTheFieldList(t *testing.T) {
	prose := []string{
		"  continuation line for the first",
		"  it broke; then we reverted it",
		"  we tried A; then B; then gave up",
	}
	for _, line := range prose {
		t.Run(strings.TrimSpace(line), func(t *testing.T) {
			rec := ParseBullet("- [Defer] X — defer: owner=alice\n" + line + "\n")
			require.Equal(t, "alice", rec.Owner,
				"a prose continuation must never extend a field value")
		})
	}

	// And every wrapped value still joins, including one wrapped across
	// three lines where only the last opens the next field.
	wrapped := map[string]string{
		"two lines": "- [Defer] W — defer: outside-story=none of the 4 files are in\n" +
			"  the story's File List; cross-cycle=no\n",
		"three lines": "- [Defer] W — defer: outside-story=none of the 4 files\n" +
			"  are in the story's\n  File List; cross-cycle=no\n",
	}
	for name, bullet := range wrapped {
		t.Run(name, func(t *testing.T) {
			rec := ParseBullet(bullet)
			require.Contains(t, rec.OutsideStory, "File List", "the value spans the wrap")
			require.Equal(t, "no", rec.CrossCycle)
		})
	}
}

// TestSectionFrom_DoesNotInventAStoryFromProse: the slug anchor is `of
// <token>`, and prose is full of the word "of". Taking the last match
// unconditionally read "scope" out of "deemed out of scope" — a
// confidently wrong value that reads as authoritative, which is FW-1a's
// failure mode arriving through a different door.
func TestSectionFrom_DoesNotInventAStoryFromProse(t *testing.T) {
	cases := map[string]string{
		"## Deferred from: story review of 91-2, deemed out of scope (2026-07-30)":   "91-2",
		"## Deferred from: story review of 92-1 — part of the 92 batch (2026-08-01)": "92-1",
		"## Deferred from: review of the review of 90-1 (2026-01-01)":                "90-1",
		"## Deferred from: story review of 54-1, which is out of band (2026-08-21)":  "54-1",
	}
	for heading, want := range cases {
		t.Run(want, func(t *testing.T) {
			m := sectionRe.FindStringSubmatch(heading)
			require.NotNil(t, m)
			require.Equal(t, want, sectionFrom(m[1]).story)
		})
	}
}

// TestMigrate_EveryCountedBucketReachesDisk is the assertion that keeps
// verifyLossless honest.
//
// The check counts four buckets, and a bucket that is counted at parse
// time but discarded at write time makes it prove something about the
// PARSE while reading as a claim about the MIGRATION. Headings were
// exactly that: `source`, `source_story` and `created` are extractions,
// so everything else a provenance heading carried — here the attribution
// "retroactively backfilled by Story 89.2" — reached no file at all. The
// asymmetry was the tell: an UNRECOGNISED heading survived as a record
// Title, so the better-understood heading was the one losing text.
func TestMigrate_EveryCountedBucketReachesDisk(t *testing.T) {
	const heading = "## Deferred from: story review of 86-1_create-branch-cas " +
		"(retroactively backfilled by Story 89.2, 2026-07-26)"
	ledger := "# Deferred Work\n\n" +
		heading + "\n\n" +
		"- [Defer] The first item [a.go:1]\n" +
		"- [Defer] The second item [b.go:2]\n\n" +
		"## OPEN — an unrecognised heading (2026-07-27)\n\n" +
		"- the item under it\n\n" +
		"## Deferred-at-decision: titles nothing at all\n\n" +
		"## Deferred from: story review of 90-1 (2026-07-28)\n\n" +
		"- [Defer] The third item [c.go:3]\n"

	dir, legacy := writeLegacy(t, ledger)
	s := New(filepath.Join(dir, "deferred"))
	_, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
	require.NoError(t, err)

	// Everything the store wrote, read back THROUGH THE LOADER for the
	// records and raw for the preamble file, which is verbatim Markdown.
	//
	// Reading records as raw bytes instead would make this assertion
	// stricter than the property it checks: frontmatter is YAML, so a
	// heading containing an apostrophe is emitted single-quoted with the
	// quote doubled ('the story''s File List'), and one containing a tab
	// switches to double-quoted with the tab escaped. The value round
	// trips correctly in every case — nothing is lost — but a raw
	// substring search does not find it. That failure would read as the
	// migration dropping a line, and the natural reaction to it is to
	// loosen the assertion, which would hand back the teeth it exists to
	// have. So it reads disk the way the store reads disk. It still
	// refuses to consult doc.Headings.
	loaded, err := s.Load(LoadOptions{IncludeClosed: true})
	require.NoError(t, err)
	preamble, err := os.ReadFile(filepath.Join(s.Dir, PreambleFileName))
	require.NoError(t, err)

	survives := func(line string) bool {
		if strings.Contains(string(preamble), line) {
			return true
		}
		for i := range loaded.Records {
			rec := &loaded.Records[i]
			if strings.Contains(rec.Body, line) || strings.Contains(rec.SourceHeading, line) {
				return true
			}
		}
		return false
	}

	// Every significant ledger line, headings included, is somewhere in
	// the store. This is the migration's claim, checked end to end rather
	// than against the parser's own bookkeeping.
	for line := range strings.SplitSeq(ledger, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		require.Truef(t, survives(line), "the migration dropped a ledger line: %q", line)
	}

	// Specifically: the parts of a heading no structured field extracts.
	require.True(t, survives("retroactively backfilled by Story 89.2"),
		"attribution beyond source/source_story/created must survive")

	// And it lands on every record the heading provenances, not just one.
	var provenanced int
	for i := range loaded.Records {
		if loaded.Records[i].SourceHeading == heading {
			provenanced++
			require.Equal(t, "86-1_create-branch-cas", loaded.Records[i].SourceStory,
				"the verbatim heading sits alongside the extractions, not instead of them")
		}
	}
	require.Equal(t, 2, provenanced, "both records under the heading carry it")
}

// TestMigrate_HeadingsThatYAMLMustEscape: the escaping shapes that make a
// raw-bytes assertion lie. Each round trips through the store intact, so
// the guarantee holds — it just cannot be checked by grepping the file.
func TestMigrate_HeadingsThatYAMLMustEscape(t *testing.T) {
	headings := map[string]string{
		"apostrophe": "## Deferred from: story review of 59-1 the story's File List (2026-08-26)",
		"tab":        "## Deferred from: story review of 59-1 with a\ttab (2026-08-26)",
		"backslash":  "## Deferred from: story review of 59-1 a\\b (2026-08-26)",
		"both":       "## Deferred from: story review of 59-1 a\\b don't (2026-08-26)",
		"colon":      "## Deferred from: story review of 59-1: after a colon (2026-08-26)",
	}
	for name, heading := range headings {
		t.Run(name, func(t *testing.T) {
			ledger := heading + "\n\n- [Defer] The item [a.go:1]\n"
			dir, legacy := writeLegacy(t, ledger)
			s := New(filepath.Join(dir, "deferred"))
			_, err := s.Migrate(context.Background(), MigrateOptions{From: legacy})
			require.NoError(t, err)

			loaded, err := s.Load(LoadOptions{})
			require.NoError(t, err)
			require.Len(t, loaded.Records, 1)
			require.Equal(t, heading, loaded.Records[0].SourceHeading,
				"the heading survives YAML escaping byte-for-byte")
			require.Equal(t, "59-1", loaded.Records[0].SourceStory)
		})
	}
}
