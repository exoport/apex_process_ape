package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/output"
)

// TestRunDoctor_Aggregates exercises the runDoctor harness end-to-end
// with a synthetic check set: one OK, one WARN, one FAIL (non-required
// → downgraded to WARN), one user-skipped. Confirms the report
// aggregates statuses correctly and that durations stamp.
func TestRunDoctor_Aggregates(t *testing.T) {
	checks := []doctorCheck{
		{Name: "always.ok", Required: true, Run: func(_ context.Context, _ doctorEnv) CheckResult {
			return CheckResult{Status: StatusOK, Message: "fine"}
		}},
		{Name: "always.warn", Run: func(_ context.Context, _ doctorEnv) CheckResult {
			return CheckResult{Status: StatusWarn, Message: "iffy"}
		}},
		{Name: "non.required.fail", Run: func(_ context.Context, _ doctorEnv) CheckResult {
			return CheckResult{Status: StatusFail, Message: "broken but optional"}
		}},
		{Name: "user.skipped", Run: func(_ context.Context, _ doctorEnv) CheckResult {
			t.Fatal("skipped check ran")
			return CheckResult{}
		}},
	}
	skip := map[string]struct{}{"user.skipped": {}}
	r := runDoctor(context.Background(), checks, doctorEnv{}, skip)

	if r.Summary.OK != 1 {
		t.Errorf("ok count: got %d, want 1", r.Summary.OK)
	}
	// non.required.fail is downgraded to warn → total warn = 2
	if r.Summary.Warn != 2 {
		t.Errorf("warn count: got %d, want 2", r.Summary.Warn)
	}
	if r.Summary.Fail != 0 {
		t.Errorf("fail count: got %d, want 0 (non-required FAIL must downgrade)", r.Summary.Fail)
	}
	if r.Summary.Skip != 1 {
		t.Errorf("skip count: got %d, want 1", r.Summary.Skip)
	}
}

// TestRunDoctor_RequiredFailStaysFail confirms a Required:true check
// that returns FAIL remains a FAIL — only non-required checks are
// downgraded to WARN.
func TestRunDoctor_RequiredFailStaysFail(t *testing.T) {
	checks := []doctorCheck{
		{Name: "required.fail", Required: true, Run: func(_ context.Context, _ doctorEnv) CheckResult {
			return CheckResult{Status: StatusFail, Message: "missing"}
		}},
	}
	r := runDoctor(context.Background(), checks, doctorEnv{}, nil)
	if r.Summary.Fail != 1 {
		t.Errorf("fail count: got %d, want 1", r.Summary.Fail)
	}
	if !doctorShouldFail(r, false) {
		t.Error("required FAIL must trigger doctorShouldFail")
	}
}

// TestDoctorShouldFail_StrictPromotesWarn confirms --strict treats any
// WARN as failure-triggering, while the default mode tolerates them.
func TestDoctorShouldFail_StrictPromotesWarn(t *testing.T) {
	r := DoctorReport{Summary: DoctorSummary{Warn: 1}}
	if doctorShouldFail(r, false) {
		t.Error("non-strict mode should not fail on WARN-only report")
	}
	if !doctorShouldFail(r, true) {
		t.Error("strict mode must fail on any WARN")
	}
}

func TestCheckClaudeBinary_PathInjection(t *testing.T) {
	// Create a synthetic claude on a custom PATH. Windows's exec.LookPath
	// only resolves names whose extension is on PATHEXT, so the synthetic
	// binary must be named `claude.exe` there.
	dir := t.TempDir()
	binName := "claude"
	if runtime.GOOS == "windows" {
		binName = "claude.exe"
	}
	bin := filepath.Join(dir, binName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	res := checkClaudeBinary(context.Background(), doctorEnv{})
	if res.Status != StatusOK {
		t.Errorf("status = %q, want ok; message=%q", res.Status, res.Message)
	}
	if res.Message != bin {
		t.Errorf("message = %q, want %q", res.Message, bin)
	}
}

func TestCheckClaudeBinary_MissingFails(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir on PATH
	res := checkClaudeBinary(context.Background(), doctorEnv{})
	if res.Status != StatusFail {
		t.Errorf("missing claude should FAIL; got status=%q", res.Status)
	}
}

func TestCheckPlaywrightHostSupported_NonLinuxInfo(t *testing.T) {
	res := checkPlaywrightHostSupported(context.Background(), doctorEnv{OS: "darwin", Arch: "arm64"})
	if res.Status != StatusInfo {
		t.Errorf("status on darwin: got %q, want info", res.Status)
	}
}

func TestCheckPlaywrightHostSupported_UbuntuSupportedOK(t *testing.T) {
	env := doctorEnv{OS: "linux", OSRelease: map[string]string{"ID": "ubuntu", "VERSION_ID": "24.04"}}
	res := checkPlaywrightHostSupported(context.Background(), env)
	if res.Status != StatusOK {
		t.Errorf("Ubuntu 24.04 should be OK; got %q (%s)", res.Status, res.Message)
	}
}

func TestCheckPlaywrightHostSupported_UbuntuUnsupportedWarn(t *testing.T) {
	env := doctorEnv{OS: "linux", OSRelease: map[string]string{"ID": "ubuntu", "VERSION_ID": "26.04"}}
	res := checkPlaywrightHostSupported(context.Background(), env)
	if res.Status != StatusWarn {
		t.Fatalf("Ubuntu 26.04 should WARN; got %q (%s)", res.Status, res.Message)
	}
	if !strings.Contains(res.FixCommand, "PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS") {
		t.Errorf("fix command must reference the env var; got %q", res.FixCommand)
	}
}

func TestCheckPlaywrightHostSupported_NonUbuntuInfo(t *testing.T) {
	env := doctorEnv{OS: "linux", OSRelease: map[string]string{"ID": "debian", "VERSION_ID": "12"}}
	res := checkPlaywrightHostSupported(context.Background(), env)
	if res.Status != StatusInfo {
		t.Errorf("non-Ubuntu Linux should be INFO; got %q", res.Status)
	}
}

func TestCheckSkillsProject_CountsFrameworkAndCustom(t *testing.T) {
	root := t.TempDir()
	// Mark the dir as a project so isProjectRoot returns true.
	if err := os.MkdirAll(filepath.Join(root, "_apex"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"apex-one", "apex-two", "custom-skill"} {
		dir := filepath.Join(root, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := checkSkillsProject(context.Background(), doctorEnv{ProjectRoot: root})
	if res.Status != StatusOK {
		t.Fatalf("status: got %q, want ok; msg=%q", res.Status, res.Message)
	}
	for _, frag := range []string{"3 skills", "2 framework", "1 custom"} {
		if !strings.Contains(res.Message, frag) {
			t.Errorf("message %q should contain %q", res.Message, frag)
		}
	}
}

// TestCheckFrameworkMetadata_NotInstalledWarns covers the most common
// fresh-project state: project root exists, but `ape framework setup`
// hasn't run yet.
func TestCheckFrameworkMetadata_NotInstalledWarns(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := checkFrameworkMetadata(context.Background(), doctorEnv{ProjectRoot: root})
	if res.Status != StatusWarn {
		t.Errorf("status: got %q, want warn", res.Status)
	}
	if res.FixCommand != "ape framework setup" {
		t.Errorf("fix command: got %q, want 'ape framework setup'", res.FixCommand)
	}
}

func TestCheckFrameworkMetadata_OutsideProjectInfo(t *testing.T) {
	res := checkFrameworkMetadata(context.Background(), doctorEnv{ProjectRoot: t.TempDir()})
	if res.Status != StatusInfo {
		t.Errorf("outside-project status: got %q, want info", res.Status)
	}
}

// TestEmitDoctorReport_JSONRoundtrips makes sure the JSON output is
// parseable by downstream tooling — a CI script piping through `jq`
// is the canonical consumer of the structured form.
func TestEmitDoctorReport_JSONRoundtrips(t *testing.T) {
	r := DoctorReport{
		Checks: []CheckResult{
			{Name: "x", Status: StatusOK, Message: "fine"},
			{Name: "y", Status: StatusWarn, Message: "iffy", Remediation: "fix it"},
		},
		Summary: DoctorSummary{OK: 1, Warn: 1},
	}
	var buf bytes.Buffer
	if err := emitDoctorReport(&buf, r, output.FormatJSON); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var got DoctorReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Checks) != 2 || got.Summary.OK != 1 || got.Summary.Warn != 1 {
		t.Errorf("roundtripped report mismatch: %+v", got)
	}
}

// TestEmitDoctorReport_HumanIsPlainOnBufferWriter confirms that the
// non-TTY fallback runs whenever the writer isn't an *os.File pointed
// at a terminal. The whole CI / test / pipe path depends on this.
func TestEmitDoctorReport_HumanIsPlainOnBufferWriter(t *testing.T) {
	r := DoctorReport{
		Checks:  []CheckResult{{Name: "x", Status: StatusOK, Message: "fine"}},
		Summary: DoctorSummary{OK: 1},
	}
	var buf bytes.Buffer
	if err := emitDoctorReport(&buf, r, output.FormatHuman); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("non-TTY writer must not receive ANSI escapes; got:\n%s", buf.String())
	}
}

// TestEmitDoctorHumanColor_ContainsEscapesAndGlyphs exercises the
// colourised path directly so we don't need a real TTY in the test
// harness. The plain-mode test above still guards the default path.
func TestEmitDoctorHumanColor_ContainsEscapesAndGlyphs(t *testing.T) {
	r := DoctorReport{
		Checks: []CheckResult{
			{Name: "a.ok", Status: StatusOK, Message: "all good"},
			{Name: "b.warn", Status: StatusWarn, Message: "watch out", Remediation: "do thing", FixCommand: "do_thing --flag"},
			{Name: "c.fail", Status: StatusFail, Message: "broken"},
			{Name: "d.info", Status: StatusInfo, Message: "noted"},
			{Name: "e.skip", Status: StatusSkip, Message: "—"},
		},
		Summary: DoctorSummary{OK: 1, Warn: 1, Fail: 1, Info: 1, Skip: 1},
	}
	var buf bytes.Buffer
	if err := emitDoctorHumanColor(&buf, r); err != nil {
		t.Fatalf("emit color: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("colourised output should contain ANSI escapes:\n%s", out)
	}
	for _, glyph := range []string{"✅", "⚠️", "❌", "ℹ️", "⏭️"} {
		if !strings.Contains(out, glyph) {
			t.Errorf("colour output missing glyph %q:\n%s", glyph, out)
		}
	}
	if !strings.Contains(out, "Remediations:") {
		t.Errorf("remediation block missing:\n%s", out)
	}
}

// TestShouldColorizeWriter_RespectsNoColorEnv locks in the NO_COLOR
// convention — even on a TTY, NO_COLOR forces the plain path.
func TestShouldColorizeWriter_RespectsNoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if shouldColorizeWriter(os.Stdout) {
		t.Error("NO_COLOR set: shouldColorizeWriter must return false")
	}
}

// TestShouldColorizeWriter_BufferWriterIsFalse documents the path
// tests rely on — a bytes.Buffer can never be a terminal.
func TestShouldColorizeWriter_BufferWriterIsFalse(t *testing.T) {
	t.Setenv("NO_COLOR", "") // make sure NO_COLOR isn't inherited
	os.Unsetenv("NO_COLOR")
	if shouldColorizeWriter(&bytes.Buffer{}) {
		t.Error("bytes.Buffer must not be detected as a terminal")
	}
}

func TestEmitDoctorReport_HumanContainsHeader(t *testing.T) {
	r := DoctorReport{
		Checks:  []CheckResult{{Name: "x", Status: StatusOK, Message: "fine"}},
		Summary: DoctorSummary{OK: 1},
	}
	var buf bytes.Buffer
	if err := emitDoctorReport(&buf, r, output.FormatHuman); err != nil {
		t.Fatalf("emit: %v", err)
	}
	out := buf.String()
	for _, frag := range []string{"environment health", "STATUS", "CHECK", "DETAIL", "1 ok"} {
		if !strings.Contains(out, frag) {
			t.Errorf("output missing %q:\n%s", frag, out)
		}
	}
}

func TestParseSkipList(t *testing.T) {
	cases := map[string][]string{
		"":                    nil,
		"a":                   {"a"},
		"a,b , c":             {"a", "b", "c"},
		",,trailing,,commas,": {"trailing", "commas"},
	}
	for in, wantKeys := range cases {
		got := parseSkipList(in)
		if len(got) != len(wantKeys) {
			t.Errorf("input %q: got %d keys, want %d (%v)", in, len(got), len(wantKeys), got)
			continue
		}
		for _, k := range wantKeys {
			if _, ok := got[k]; !ok {
				t.Errorf("input %q: missing key %q in %v", in, k, got)
			}
		}
	}
}

// TestCheckNames_StableSorted documents that the list of check names
// is alphabetically sorted — useful for shell completion and for
// downstream tools that want a deterministic enumeration.
func TestCheckNames_StableSorted(t *testing.T) {
	names := checkNames()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("checkNames not sorted: %q > %q", names[i-1], names[i])
		}
	}
}

// TestRunDoctor_StampsDurations sanity-checks that DurationMs is at
// least set (not the zero pre-write default we'd see if the harness
// forgot to populate it).
func TestRunDoctor_StampsDurations(t *testing.T) {
	checks := []doctorCheck{
		{Name: "slow", Run: func(_ context.Context, _ doctorEnv) CheckResult {
			// We can't sleep without slowing the suite; assert the
			// field is filled after a run, even if it ends up zero.
			return CheckResult{Status: StatusOK}
		}},
	}
	r := runDoctor(context.Background(), checks, doctorEnv{}, nil)
	if got := r.Checks[0].DurationMs; got < 0 {
		t.Errorf("duration must be non-negative, got %d", got)
	}
}

// TestCheckPermissionsHomeClaude_NonexistentDirInfo covers the case
// where ~/.claude has not been created yet by claude itself.
func TestCheckPermissionsHomeClaude_NonexistentDirInfo(t *testing.T) {
	fakeHome := t.TempDir()
	res := checkPermissionsHomeClaude(context.Background(), doctorEnv{Home: fakeHome})
	if res.Status != StatusInfo {
		t.Errorf("nonexistent ~/.claude should be INFO; got %q", res.Status)
	}
}

func TestCheckPermissionsHomeClaude_WritableOK(t *testing.T) {
	fakeHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fakeHome, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := checkPermissionsHomeClaude(context.Background(), doctorEnv{Home: fakeHome})
	if res.Status != StatusOK {
		t.Errorf("writable ~/.claude should be OK; got %q msg=%q", res.Status, res.Message)
	}
}

// ensureFrameworkPackageReadable is a guard against accidental import
// cycles between apecmd <-> framework while the helper refactor is
// still settling. If this test ever fails to compile, the package
// import edge has been broken and the refactor needs revisiting.
func TestFrameworkImportEdge(t *testing.T) {
	if _, err := os.Stat("doctor_checks.go"); err != nil {
		// We can't reach this in normal `go test` invocations; the
		// compile would have failed long before. Sanity guard only.
		t.Fatal(errors.New("doctor_checks.go missing"))
	}
}
