package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The defect, in one line: a list value went through
// `fmt.Sprintf("%v", …)` like every other value, and Go renders a []any as
// `[FEAT-1-2 FEAT-1-3]` — space-separated, not YAML, not JSON. It landed
// in index.yaml as a quoted STRING.
//
// Found by a framework capture: seven dependency lists in one feature
// index, on `depends_on` and `depended_on_by`. The skill that read the
// index back refused to commit and halted a $56.67 stage, which is the
// only reason it surfaced instead of propagating into epic and story
// generation.
func TestParseUpdates_ListStaysAList(t *testing.T) {
	t.Parallel()

	got, err := ParseUpdates([]byte(
		`{"FEAT-1-1": {"depended_on_by": ["FEAT-1-2", "FEAT-1-3"], "depends_on": [], "status": "specified"}}`))
	require.NoError(t, err)

	require.Equal(t, map[string]map[string]UpdateValue{
		"FEAT-1-1": {
			"depended_on_by": {Items: []string{"FEAT-1-2", "FEAT-1-3"}, IsList: true},
			"depends_on":     {Items: []string{}, IsList: true},
			"status":         {Scalar: "specified"},
		},
	}, got)

	require.NotContains(t, got["FEAT-1-1"]["depended_on_by"].Scalar, "[FEAT-1-2 FEAT-1-3]",
		"the Go-formatted rendering is the corruption itself")
}

// The scalar coercion this file must NOT break. It is why values were
// stringified in the first place: YAML 1.1 turns `no` into false and
// `0001` into 1, and an index field is text.
func TestParseUpdates_ScalarsStayText(t *testing.T) {
	t.Parallel()

	got, err := ParseUpdates([]byte(`{"ADR-0001": {"status": "no", "seq": "0001", "count": 4, "flag": true}}`))
	require.NoError(t, err)

	fields := got["ADR-0001"]
	for key, want := range map[string]string{"status": "no", "seq": "0001", "count": "4", "flag": "true"} {
		require.False(t, fields[key].IsList, "%s is a scalar", key)
		require.Equal(t, want, fields[key].Scalar, "%s", key)
	}
}

// Rejected rather than flattened: `fmt.Sprintf("%v", map…)` yields
// `map[a:1]`, which reads back as a string and replaces structure with a
// Go debug rendering — the same failure in a different costume.
func TestParseUpdates_RejectsWhatItCannotRepresent(t *testing.T) {
	t.Parallel()

	for name, payload := range map[string]string{
		"nested object":      `{"FEAT-1-1": {"meta": {"a": 1}}}`,
		"list of objects":    `{"FEAT-1-1": {"deps": [{"id": "FEAT-1-2"}]}}`,
		"list within a list": `{"FEAT-1-1": {"deps": [["FEAT-1-2"]]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseUpdates([]byte(payload))
			require.Error(t, err, "a value with no faithful text form must be refused, not flattened")
			require.Contains(t, err.Error(), "FEAT-1-1", "the error names the entry it came from")
		})
	}
}

// End to end, through the real index writer: the value must arrive as a
// YAML sequence a parser reads back as a list.
func TestUpdate_WritesARealSequence(t *testing.T) {
	f := newFixture(t)
	f.record("features", "feat-1-1_a.md", "FEAT-1-1")
	f.mappingIndex([2]string{"FEAT-1-1", "feat-1-1_a.md"})

	family, err := FamilyByName("features")
	require.NoError(t, err)
	_, err = Update(f.cfg, family, map[string]map[string]UpdateValue{
		"FEAT-1-1": {
			"depended_on_by": {Items: []string{"FEAT-1-2", "FEAT-1-3"}, IsList: true},
			"depends_on":     {Items: []string{}, IsList: true},
		},
	}, "20260822010203")
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(f.dir("features"), IndexFileName))
	require.NoError(t, err)

	// The shape, asserted on the bytes: the corrupted form was a quoted
	// string, so a test that only unmarshals would pass on `'[A B]'` in a
	// language where that is a valid string.
	require.Contains(t, string(body), "depended_on_by: [FEAT-1-2, FEAT-1-3]")
	require.NotContains(t, string(body), "'[FEAT-1-2 FEAT-1-3]'")

	// Asserted through a map rather than a tagged struct: the index's keys
	// are snake_case, which the repo's tagliatelle rule would reject in a
	// struct tag, and the question here is the VALUE's kind anyway.
	// map[string]any at the top: the index holds the scalar generated_at
	// beside the features mapping, so a stricter shape fails on the file's
	// own header rather than on the thing under test.
	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal(body, &parsed),
		"the index must still parse against the schema's own shape")
	features, ok := parsed["features"].(map[string]any)
	require.True(t, ok, "features: is not a mapping")
	entry, ok := features["FEAT-1-1"].(map[string]any)
	require.True(t, ok, "FEAT-1-1 is not a mapping")
	require.Equal(t, []any{"FEAT-1-2", "FEAT-1-3"}, entry["depended_on_by"])
	require.Equal(t, []any{}, entry["depends_on"], "an empty list stays a list, not an empty string")
}

// The repair path, which is the answer to "the corruption has no in-band
// remedy": re-running the update over a field the old code corrupted
// replaces the value node outright. No hand-edit of index.yaml, which the
// framework forbids, and no new command.
func TestUpdate_RepairsAFieldTheOldBehaviourCorrupted(t *testing.T) {
	f := newFixture(t)
	f.record("features", "feat-1-1_a.md", "FEAT-1-1")
	// Verbatim from the preserved capture, quoted string and all.
	f.raw("features", IndexFileName, `generated_at: '20260918134904'
features:
  FEAT-1-1:
    name: "Greeting Form"
    file: feat-1-1_a.md
    status: specified
    depends_on: []
    depended_on_by: '[FEAT-1-2 FEAT-1-3 FEAT-1-4]'
`)

	var before struct {
		Features map[string]map[string]any `yaml:"features"`
	}
	raw, err := os.ReadFile(filepath.Join(f.dir("features"), IndexFileName))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(raw, &before))
	require.IsType(t, "", before.Features["FEAT-1-1"]["depended_on_by"],
		"precondition: the fixture carries the corruption this repairs")

	family, err := FamilyByName("features")
	require.NoError(t, err)
	_, err = Update(f.cfg, family, map[string]map[string]UpdateValue{
		"FEAT-1-1": {"depended_on_by": {Items: []string{"FEAT-1-2", "FEAT-1-3", "FEAT-1-4"}, IsList: true}},
	}, "20260918140000")
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(f.dir("features"), IndexFileName))
	require.NoError(t, err)
	var after struct {
		Features map[string]map[string]any `yaml:"features"`
	}
	require.NoError(t, yaml.Unmarshal(body, &after))
	require.IsType(t, []any{}, after.Features["FEAT-1-1"]["depended_on_by"],
		"re-running the update is the repair — the quoted string is replaced by a sequence")
	require.Equal(t, []any{"FEAT-1-2", "FEAT-1-3", "FEAT-1-4"}, after.Features["FEAT-1-1"]["depended_on_by"])
}
