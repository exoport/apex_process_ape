package apexv1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.3 (D508, D512): `check (full):` is the kind's name, and `check
// (live):`, its v1.2 name, parses identically, on a scenario and on a
// task, with no finding.
func TestFullCheck_FullAndLiveParseIdentically(t *testing.T) {
	for _, kind := range []string{"full", "live"} {
		doc := strings.Replace(liveSpec, "- check (live): ./svc.sh", "- check ("+kind+"): ./svc.sh", 1)
		s := ParseSpec(doc)
		served := s.Find("Serve").Scenarios[0]
		require.True(t, served.Full(), kind)
		require.Equal(t, []string{"./svc.sh"}, served.Checks, kind)
		require.Empty(t, findingMessages(ValidateSpec("s.md", doc, 1<<20)), kind)
		pcs := scenarioChecks("greeting", s.Requirements, false)
		require.True(t, pcs[len(pcs)-1].Full, kind)

		c := ParseChange("c.md", changeDoc("add-svc", "greeting", addedWave,
			"- T1 serve — check ("+kind+"): ./svc.sh\n"))
		require.Empty(t, c.parse, kind)
		require.True(t, c.Tasks[0].Full, kind)
		require.True(t, c.Tasks[0].Live, kind)
		require.Equal(t, "./svc.sh", c.Tasks[0].Check, kind)
		require.Equal(t, "serve", c.Tasks[0].What, kind)
	}
}

// v1.3: an unknown kind is check.bad-kind, and the finding names
// `check (full):` and says `(live)` is still accepted.
func TestFullCheck_UnknownKindNamesFull(t *testing.T) {
	doc := strings.Replace(liveSpec, "- check (live): ./svc.sh", "- check (fast): ./svc.sh", 1)
	fs := ClassifyFindings(ValidateSpec("s.md", doc, 1<<20))
	require.Len(t, fs, 1)
	require.Equal(t, CodeCheckBadKind, fs[0].Code)
	require.Contains(t, fs[0].Message, "`(fast)` is not `check (full):`")
	require.Contains(t, fs[0].Message, "`(live)`, its v1.2 name, is still accepted")

	c := ParseChange("c.md", changeDoc("add-svc", "greeting", addedWave, "- T1 serve — check (fast): ./svc.sh\n"))
	tf := ClassifyFindings(c.parse)
	require.Len(t, tf, 1)
	require.Equal(t, CodeCheckBadKind, tf[0].Code)
	require.Contains(t, tf[0].Message, "`check (full):`")

	// Full, or live, on human-review is still the finding, named as written.
	for _, kind := range []string{"full", "live"} {
		d := strings.Replace(liveSpec, "- check (live): ./svc.sh", "- check ("+kind+"): human-review", 1)
		hf := ClassifyFindings(ValidateSpec("s.md", d, 1<<20))
		require.Len(t, hf, 1, kind)
		require.Equal(t, CodeCheckBadKind, hf[0].Code, kind)
		require.Contains(t, hf[0].Message, "is `check ("+kind+"):` on human-review", kind)
	}

	for _, code := range DiagnosticCodes {
		if code.Code == CodeCheckBadKind {
			require.Contains(t, code.Meaning, "`- check (full): <command>`")
			require.Contains(t, code.Meaning, "a service, a network, a browser, or long")
			require.Contains(t, code.Meaning, "`(live)`, its v1.2 name, is still accepted")
		}
	}
}

// v1.3 (D512): a task's JSON carries full and, deprecated, live; a plain
// task carries neither.
func TestFullCheck_TaskJSONHasFullAndLive(t *testing.T) {
	c := ParseChange("c.md", changeDoc("add-svc", "greeting", addedWave,
		"- T1 serve — check (full): ./svc.sh\n- T2 add wave — check: ./wave.sh\n"))
	data, err := json.Marshal(c.Tasks)
	require.NoError(t, err)
	var tasks []map[string]any
	require.NoError(t, json.Unmarshal(data, &tasks))
	require.Equal(t, true, tasks[0]["full"])
	require.Equal(t, true, tasks[0]["live"])
	require.NotContains(t, tasks[1], "full")
	require.NotContains(t, tasks[1], "live")
}

// v1.3 (D512): a gate run or verify record ape v1.2 wrote has
// live_skipped alone; read back, both names carry its count.
func TestFullCheck_V12RecordsReadBothNames(t *testing.T) {
	var g GateRun
	require.NoError(t, json.Unmarshal([]byte(`{"exit":1,"live_skipped":2}`), &g))
	require.Equal(t, 2, g.FullSkipped)
	require.Equal(t, 2, g.LiveSkipped)
	out, err := json.Marshal(&g)
	require.NoError(t, err)
	require.Contains(t, string(out), `"full_skipped":2`)
	require.Contains(t, string(out), `"live_skipped":2`)

	var v Verify
	require.NoError(t, json.Unmarshal([]byte(`{"sha":"abc","live_skipped":3}`), &v))
	require.Equal(t, 3, v.FullSkipped)
	require.Equal(t, 3, v.LiveSkipped)

	var none GateRun
	require.NoError(t, json.Unmarshal([]byte(`{"exit":0}`), &none))
	out, err = json.Marshal(&none)
	require.NoError(t, err)
	require.NotContains(t, string(out), "skipped", "omitempty as before")
}
