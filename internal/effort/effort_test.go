package effort

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/stretchr/testify/require"
)

// frameworkTable is the file the framework ships, verbatim.
const frameworkTable = `version: 1
defaults:        # model family word -> effort
  opus: medium
  sonnet: xhigh
  haiku: medium
fallback: high   # the model is unknown, or its family is not listed
`

func writeTable(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644))
	return dir
}

func TestLoad_TheFrameworksTable(t *testing.T) {
	d, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)
	require.Equal(t, 1, d.Version)
	require.Equal(t, map[string]string{"opus": "medium", "sonnet": "xhigh", "haiku": "medium"}, d.Defaults)
	require.Equal(t, "high", d.Fallback)
}

// A missing file is the legacy behaviour, not an error: ape tags before
// the framework raises its floor, so a new ape meets old installs.
func TestLoad_MissingFileIsNoTable(t *testing.T) {
	d, err := Load(t.TempDir())
	require.NoError(t, err)
	require.Nil(t, d)
}

func TestLoad_Rejects(t *testing.T) {
	for name, body := range map[string]string{
		"an unknown level":                     "version: 1\ndefaults: {opus: extreme}\nfallback: high\n",
		"max, which modelSettings cannot hold": "version: 1\ndefaults: {opus: max}\nfallback: high\n",
		"max as the fallback":                  "version: 1\ndefaults: {}\nfallback: max\n",
		"no fallback":                          "version: 1\ndefaults: {opus: low}\n",
		"a future version":                     "version: 2\ndefaults: {}\nfallback: high\n",
		"a misspelled key":                     "version: 1\ndefault: {opus: low}\nfallback: high\n",
		"a family that is not a word":          "version: 1\ndefaults: {'claude opus': low}\nfallback: high\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeTable(t, body))
			require.Error(t, err)
			require.Contains(t, err.Error(), FileName, "the error names the file")
		})
	}
}

func TestCheckLevel(t *testing.T) {
	for _, ok := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		require.NoError(t, CheckLevel(ok), ok)
	}
	require.Error(t, CheckLevel("xtreme"))
	require.Error(t, CheckLevel("High"), "levels are lowercase, as claude reads them")
}

func TestFor(t *testing.T) {
	d, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)

	level, fromFamily := d.For("claude-sonnet-5")
	require.Equal(t, "xhigh", level)
	require.True(t, fromFamily)

	level, fromFamily = d.For("claude-opus-5-5[1m]")
	require.Equal(t, "medium", level)
	require.True(t, fromFamily)

	level, fromFamily = d.For("claude-fable-5-1")
	require.Equal(t, "high", level, "a family the table does not list takes the fallback")
	require.False(t, fromFamily)

	level, fromFamily = d.For("")
	require.Equal(t, "high", level, "no --model: ape cannot attribute claude's default")
	require.False(t, fromFamily)
}

// The keys are the measured part: a family word matches only the model it
// currently aliases, so every known id of the family is written as well.
func TestSettings_WritesTheFamilyWordAndEveryKnownID(t *testing.T) {
	d, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)
	s := d.Settings()
	require.Equal(t, "high", s["effortLevel"], "top-level effortLevel is the fallback")
	models, ok := s["modelSettings"].(map[string]any)
	require.True(t, ok)

	require.Equal(t, map[string]any{"effortLevel": "medium"}, models["opus"])
	for _, id := range cost.KnownModels() {
		family := cost.ModelFamily(id)
		want, listed := d.Defaults[family]
		if !listed {
			require.NotContains(t, models, id, "%s: an unlisted family gets no row, so the fallback applies", id)
			continue
		}
		require.Equal(t, map[string]any{"effortLevel": want}, models[id], id)
	}
	require.Contains(t, models, "claude-opus-5", "an older id of a listed family is covered")
}

func TestDecide(t *testing.T) {
	d, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)

	p := Decide("low", SourceStep, d, "claude-sonnet-5")
	require.Equal(t, "low", p.Env, "an explicit override is process-wide")
	require.Equal(t, []string{"CLAUDE_CODE_EFFORT_LEVEL=low"}, p.EnvEntries())
	require.NotNil(t, p.Table, "the table stays in --settings; the env outranks it")
	require.Equal(t, "low", p.Resolved)

	p = Decide("", "", d, "claude-sonnet-5")
	require.Empty(t, p.EnvEntries(), "the table governs: no process-wide override")
	require.Equal(t, SourceTable, p.Source)
	require.Equal(t, "xhigh", p.Resolved)
	require.True(t, p.FromFamily)

	p = Decide("", "", nil, "claude-sonnet-5")
	require.Equal(t, []string{"CLAUDE_CODE_EFFORT_LEVEL=xhigh"}, p.EnvEntries(), "no table: the legacy default")
	require.Equal(t, SourceLegacy, p.Source)
	require.Nil(t, p.Table)
}

// With no --model the launch-time Resolved is only the fallback. A record
// written after the session ran must say the row of the model it ran on.
func TestPlan_Observed(t *testing.T) {
	d, err := Load(writeTable(t, frameworkTable))
	require.NoError(t, err)

	p := Decide("", "", d, "")
	require.True(t, p.Unattributed(""))
	require.Equal(t, d.Fallback, p.Resolved, "at launch ape can only assume the fallback")

	got := p.Observed("", "claude-opus-5-5")
	require.Equal(t, d.Defaults["opus"], got.Resolved, "the row of the model that ran")
	require.True(t, got.FromFamily)
	require.Equal(t, SourceTable, got.Source)

	require.Equal(t, p, p.Observed("", ""), "no observed model: unchanged")

	pinned := Decide("", "", d, "claude-sonnet-5")
	require.False(t, pinned.Unattributed("claude-sonnet-5"))
	require.Equal(t, pinned, pinned.Observed("claude-sonnet-5", "claude-opus-5-5"),
		"a pinned model is already attributed; telemetry does not overrule it")

	explicit := Decide("low", SourceFlag, d, "")
	require.False(t, explicit.Unattributed(""))
	require.Equal(t, "low", explicit.Observed("", "claude-opus-5-5").Resolved, "an override is process-wide")

	legacy := Decide("", "", nil, "")
	require.False(t, legacy.Unattributed(""))
	require.Equal(t, LegacyDefault, legacy.Observed("", "claude-opus-5-5").Resolved)
}
