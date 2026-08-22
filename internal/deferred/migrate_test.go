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
	require.Len(t, records, 5, "the preamble, three bullets and the free-form paragraph")

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
	require.Equal(t, 2, freeForm, "the preamble and the pasted paragraph")
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
	require.Equal(t, 5, res.RecordsIn)
	require.Equal(t, 2, res.FreeForm)
	require.True(t, res.StubWritten)

	loaded, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, loaded.Records, 5)
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
	require.Len(t, loaded.Records, 5, "no duplicates from the second run")
}

func TestMigrate_DryRunWritesNothing(t *testing.T) {
	dir, legacy := writeLegacy(t, legacyLedger)
	s := New(filepath.Join(dir, "deferred"))
	before, err := os.ReadFile(legacy)
	require.NoError(t, err)

	res, err := s.Migrate(context.Background(), MigrateOptions{From: legacy, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, 5, res.RecordsIn)
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
	require.Equal(t, 5, res.RecordsOut, "and the migration still completes")
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
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			m := sectionRe.FindStringSubmatch(tc.line)
			require.NotNil(t, m, "the heading must match")
			ctx := sectionFrom(m)
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
	require.Equal(t, 111, res.RecordsIn, "110 bullets plus the document preamble")

	loaded, err := s.Load(LoadOptions{})
	require.NoError(t, err)
	require.Len(t, loaded.Records, res.RecordsIn, "no id collision swallowed a record")

	report, err := s.Verify(VerifyOptions{})
	require.NoError(t, err)
	// Only the preamble is free-form; everything else parses.
	require.Equal(t, 1, report.Summary.ByCheck[CheckFreeForm])
}
