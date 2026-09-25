package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// fullADR is an ADR record whose frontmatter supplies every schema field.
const fullADR = `---
id: ADR-0001
title: "First"
type: technology
status: accepted
version: v2
created_at: "20260101000000"
updated_at: "20260201000000"
tags: [go]
---
`

func (f *fixture) readIndex(family string) string {
	f.t.Helper()
	body, err := os.ReadFile(filepath.Join(f.dir(family), IndexFileName))
	require.NoError(f.t, err)
	return string(body)
}

// TestBackfill_FillsAbsentFieldsInSchemaOrder is the repair for entries an
// older sync wrote: the fields come from the record and land where the
// framework's generators put them, not at the end.
func TestBackfill_FillsAbsentFieldsInSchemaOrder(t *testing.T) {
	f := newFixture(t)
	f.raw("adrs", "adr-0001_first.md", fullADR)
	f.raw("adrs", IndexFileName, `generated_at: '20260101000000'
adrs:
  - id: ADR-0001
    title: First
    status: accepted
    type: technology
    file: adr-0001_first.md
`)
	res, err := Backfill(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260925000000"})
	require.NoError(t, err)
	require.Equal(t, []BackfillFill{{
		Family: "adrs", ID: "ADR-0001", File: "adr-0001_first.md",
		Fields: []string{"slug", "tags", "version", "created_at", "updated_at"},
	}}, res.Fills)
	require.Empty(t, res.Gaps)
	require.True(t, res.Families[0].Written)

	body := f.readIndex("adrs")
	require.Equal(t, []string{
		"id", "slug", "title", "status", "type", "tags", "version", "created_at", "updated_at", "file",
	}, entryKeys(t, body, "adrs", "ADR-0001"),
		"each fill lands after its nearest present predecessor; existing keys keep their places")
	require.Contains(t, body, "20260925000000", "generated_at moves on a write")

	again, err := Backfill(f.cfg, SyncOptions{Only: []string{"adrs"}})
	require.NoError(t, err)
	require.False(t, again.Pending(), "a second run has nothing to do")
}

// TestBackfill_NeverChangesAPresentValue: an entry that says something was
// authored. Even an empty value, even one that disagrees with the record.
func TestBackfill_NeverChangesAPresentValue(t *testing.T) {
	f := newFixture(t)
	f.raw("adrs", "adr-0001_first.md", fullADR)
	f.raw("adrs", IndexFileName, `generated_at: '20260101000000'
adrs:
  - id: ADR-0001
    slug: ""
    file: adr-0001_first.md
    status: proposed
    type: technology
    tags: []
    version: v1
    created_at: "20250101000000"
    updated_at: "20250101000000"
    title: An Authored Title
`)
	before := f.readIndex("adrs")
	res, err := Backfill(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260925000000"})
	require.NoError(t, err)
	require.False(t, res.Pending())
	require.False(t, res.Families[0].Written)
	require.Equal(t, before, f.readIndex("adrs"), "not a byte moves — generated_at included")
}

// TestBackfill_MakesNoStructuralChange is the narrowness a migration needs:
// an orphan record, a phantom entry and an entry with no file: are all
// sync's to fix, and backfill leaves every one of them exactly as found.
func TestBackfill_MakesNoStructuralChange(t *testing.T) {
	f := newFixture(t)
	f.raw("adrs", "adr-0001_first.md", fullADR)
	f.raw("adrs", "adr-0002_orphan.md", "---\nid: ADR-0002\ntitle: Orphan\n---\n")
	f.raw("adrs", IndexFileName, `generated_at: '20260101000000'
adrs:
  - id: ADR-0001
    title: First
  - id: ADR-0009
    title: Phantom
`)
	res, err := Backfill(f.cfg, SyncOptions{Only: []string{"adrs"}, GeneratedAt: "20260925000000"})
	require.NoError(t, err)
	body := f.readIndex("adrs")

	require.NotContains(t, body, "ADR-0002", "no add")
	require.Contains(t, body, "ADR-0009", "no remove")
	require.NotContains(t, body, "file:", "file: is never filled — that is a repoint")
	require.Len(t, res.Fills, 1)
	require.NotContains(t, res.Fills[0].Fields, "file")

	require.Len(t, res.Gaps, 1)
	require.Equal(t, "ADR-0009", res.Gaps[0].ID, "an entry with no record is a gap, not a fill")
}

// TestBackfill_ReportsWhatTheRecordCannotSupply: a gap is reported, never
// filled — and never counted as pending, or a migration's check could
// never pass on a project whose records are themselves incomplete.
func TestBackfill_ReportsWhatTheRecordCannotSupply(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001") // id, title, status only
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"})

	res, err := Backfill(f.cfg, SyncOptions{Only: []string{"adrs"}})
	require.NoError(t, err)
	require.Equal(t, []string{"slug"}, res.Fills[0].Fields, "the file name still supplies the slug")
	require.Len(t, res.Gaps, 1)
	require.Equal(t, []string{"type", "tags", "version", "created_at", "updated_at"}, res.Gaps[0].Fields)

	again, err := Backfill(f.cfg, SyncOptions{Only: []string{"adrs"}})
	require.NoError(t, err)
	require.False(t, again.Pending(), "gaps alone are not pending work")
	require.Len(t, again.Gaps, 1, "and are still reported")
}

func TestBackfill_CheckWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.record("adrs", "adr-0001_first.md", "ADR-0001")
	f.listIndex([2]string{"ADR-0001", "adr-0001_first.md"})
	before := f.readIndex("adrs")

	res, err := Backfill(f.cfg, SyncOptions{Only: []string{"adrs"}, Check: true, GeneratedAt: "20260925000000"})
	require.NoError(t, err)
	require.True(t, res.Pending())
	require.False(t, res.Families[0].Written)
	require.Equal(t, before, f.readIndex("adrs"))
}

// TestBackfill_MappingShapedFeatures: features are keyed by id and carry no
// id field, so an insertion with no present predecessor goes first.
func TestBackfill_MappingShapedFeatures(t *testing.T) {
	f := newFixture(t)
	f.raw("features", "feat-1-1_greeting-form.md", `---
id: FEAT-1-1
name: "Greeting Form"
capability: CAP-1
status: proposed
created_at: "20260925005346"
updated_at: "20260925005346"
---
`)
	f.raw("features", IndexFileName, `generated_at: '20260101000000'
features:
  FEAT-1-1:
    file: feat-1-1_greeting-form.md
    status: proposed
`)
	res, err := Backfill(f.cfg, SyncOptions{Only: []string{"features"}})
	require.NoError(t, err)
	require.Empty(t, res.Gaps)
	require.Equal(t, []string{
		"name", "slug", "file", "capability", "status", "created_at", "updated_at",
	}, entryKeys(t, f.readIndex("features"), "features", "FEAT-1-1"))
}

// TestBackfill_NoIndexIsNotAnAdd: creating an index is sync's job.
func TestBackfill_NoIndexIsNotAnAdd(t *testing.T) {
	f := newFixture(t)
	f.raw("adrs", "adr-0001_first.md", fullADR)
	res, err := Backfill(f.cfg, SyncOptions{Only: []string{"adrs"}})
	require.NoError(t, err)
	require.False(t, res.Pending())
	require.True(t, res.Families[0].Skipped)
	_, statErr := os.Stat(filepath.Join(f.dir("adrs"), IndexFileName))
	require.True(t, os.IsNotExist(statErr))
}
