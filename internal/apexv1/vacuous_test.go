package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// H11: the vacuous rule, on the real phrasing of each runner the default
// list names, and on the multi-unit runs it must leave alone.
func TestVacuousPhrase(t *testing.T) {
	def := DefaultVacuousOutput
	for name, tc := range map[string]struct {
		out  string
		want string
	}{
		"go, one package, no match": {"ok  \tgithub.com/x/greet\t0.003s [no tests to run]\n", "no tests to run"},
		"go -v, no match":           {"testing: warning: no tests to run\nPASS\nok  \tgithub.com/x/greet\t0.002s [no tests to run]\n", "no tests to run"},
		"go ./..., nothing matched anywhere": {
			"ok  \tgithub.com/x/a\t0.01s [no tests to run]\n?   \tgithub.com/x/cmd\t[no test files]\nok  \tgithub.com/x/b\t0.01s [no tests to run]\n",
			"no tests to run",
		},
		"go ./..., one package ran the test": {
			"ok  \tgithub.com/x/a\t0.41s\n?   \tgithub.com/x/cmd\t[no test files]\nok  \tgithub.com/x/b\t0.01s [no tests to run]\n", "",
		},
		"go ./... -v, one package ran": {
			"=== RUN   TestX\n--- PASS: TestX (0.00s)\nPASS\nok  \tgithub.com/x/a\t0.4s\ntesting: warning: no tests to run\nPASS\nok  \tgithub.com/x/b\t0.01s [no tests to run]\n", "",
		},
		"go ./..., packages without tests beside ones that ran": {"ok  \tgithub.com/x/a\t0.41s\n?   \tgithub.com/x/cmd\t[no test files]\n", ""},
		"make echoes its recipe":                                {"go test -run TestNew ./greet\nok  \tgithub.com/x/greet\t0.003s [no tests to run]\n", "no tests to run"},
		"cargo, filter matched nothing": {
			"running 0 tests\n\ntest result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 4 filtered out; finished in 0.00s\n\n   Doc-tests x\n\nrunning 0 tests\n",
			"running 0 tests",
		},
		"cargo, one binary ran": {"running 3 tests\ntest a ... ok\n\nrunning 0 tests\n", ""},
		"unittest before 3.12":  {"\n----------------------------------------------------------------------\nRan 0 tests in 0.000s\n\nOK\n", "Ran 0 tests"},
		"unittest ran":          {"...\nRan 3 tests in 0.010s\n\nOK\n", ""},
		"mocha, nothing":        {"\n\n  0 passing (2ms)\n\n", "0 passing"},
		"mocha, 10 passing":     {"\n  10 passing (40ms)\n", ""},
		"pytest, exit 5 masked": {"============================= test session starts ==============================\ncollected 0 items\n\n============================ no tests ran in 0.01s =============================\n", "collected 0 items"},
		"pytest, ran":           {"collected 10 items\n\ntest_x.py ..........\n", ""},
		"plain output":          {"Hello Ann\n", ""},
		"empty output":          {"", ""},
	} {
		require.Equal(t, tc.want, vacuousPhrase(tc.out, def), name)
	}
	require.Empty(t, vacuousPhrase("ok x [no tests to run]\n", nil), "an empty list turns the rule off")
	require.Equal(t, "0 examples", vacuousPhrase("0 examples, 0 failures\n", []string{"0 examples"}))
}

func TestContainsWordAndMentionsVar(t *testing.T) {
	require.True(t, containsWord("  0 passing (2ms)", "0 passing"))
	require.False(t, containsWord("  10 passing (2ms)", "0 passing"))
	require.False(t, containsWord("Ran 0 testsuites", "Ran 0 tests"))
	require.True(t, containsWord("x [no tests to run]", "no tests to run"))
	require.True(t, containsWord("10 passing, 0 passing", "0 passing"), "a later bounded match counts")

	require.True(t, mentionsVar("psql $RADIX_DSN -c 1", "RADIX_DSN"))
	require.True(t, mentionsVar(`: "${RADIX_DSN:?}" && go test ./...`, "RADIX_DSN"))
	require.True(t, mentionsVar("go test ./store/... # needs $RADIX_DSN", "RADIX_DSN"))
	require.False(t, mentionsVar("echo $RADIX_DSN_OLD", "RADIX_DSN"))
	require.False(t, mentionsVar("echo RADIX_DSN", "RADIX_DSN"))

	require.True(t, invokes("docker compose up -d && go test ./...", "docker"))
	require.True(t, invokes("sudo /usr/bin/docker info", "docker"))
	require.True(t, invokes(`sh -c "docker ps"`, "docker"))
	require.False(t, invokes("go test ./internal/docker/...", "docker"))
	require.False(t, invokes(`echo "docker"`, "docker"))
}

// H11: checks: and requires: validate as they parse.
func TestConfig_ChecksAndRequires(t *testing.T) {
	load := func(t *testing.T, yaml string) (Config, error) {
		t.Helper()
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
		require.NoError(t, os.WriteFile(ConfigPath(root), []byte(yaml), 0o644))
		return LoadConfig(root)
	}
	cfg, err := load(t, "project_name: x\n")
	require.NoError(t, err)
	require.Equal(t, DefaultVacuousOutput, cfg.Checks.Vacuous())
	cfg, err = load(t, "checks: {vacuous_output: []}\n")
	require.NoError(t, err)
	require.Empty(t, cfg.Checks.Vacuous(), "an empty list turns the rule off")
	cfg, err = load(t, "checks:\n  vacuous_output: [\"0 examples\"]\nrequires: {RADIX_DSN: env, docker: cmd}\n")
	require.NoError(t, err)
	require.Equal(t, []string{"0 examples"}, cfg.Checks.Vacuous())
	require.Equal(t, Requires{"RADIX_DSN": {Kind: RequireEnv}, "docker": {Kind: RequireCmd}}, cfg.Requires)
	require.Equal(t, DefaultSlowRegression, cfg.Checks.SlowRegressionAt(), "W2 Q-W2a: 5m")
	cfg, err = load(t, "checks: {slow_regression: 90s}\n")
	require.NoError(t, err)
	require.Equal(t, 90*time.Second, cfg.Checks.SlowRegressionAt())
	require.Equal(t, DefaultVacuousOutput, cfg.Checks.Vacuous(), "vacuous_output absent: the defaults")
	// v1.4 (T4): vacuous_output_extra appends to the list in force.
	cfg, err = load(t, "checks: {vacuous_output_extra: [\"harness unavailable\"]}\n")
	require.NoError(t, err)
	require.Equal(t, append(append([]string(nil), DefaultVacuousOutput...), "harness unavailable"), cfg.Checks.Vacuous())
	cfg, err = load(t, "checks: {vacuous_output: [\"0 examples\"], vacuous_output_extra: [\"harness unavailable\"]}\n")
	require.NoError(t, err)
	require.Equal(t, []string{"0 examples", "harness unavailable"}, cfg.Checks.Vacuous())
	require.Equal(t, DefaultVacuousOutput, ChecksConfig{}.Vacuous())

	for yaml, want := range map[string]string{
		"checks: {vacuous_output_extra: [\" \"]}\n": "checks.vacuous_output_extra: an empty phrase",
		"checks: {vacuous: [x]}\n":                  "checks.vacuous: not a key",
		"checks: {vacuous_output: [\"\"]}\n":        "empty phrase",
		"checks: {slow_regression: soon}\n":         "checks.slow_regression",
		"checks: {slow_regression: -1m}\n":          "checks.slow_regression",
		"requires: {RADIX_DSN: envvar}\n":           "is not env or cmd",
		"requires: {RADIX-DSN: env}\n":              "not an environment variable name",
		"requires: {/usr/bin/docker: cmd}\n":        "without a path",
	} {
		_, err := load(t, yaml)
		require.ErrorContains(t, err, want, yaml)
	}
}

// H11 end to end: a task check that ran nothing is red (vacuous), a
// scenario whose requirement is missing is red (unrun) and not run, both
// are named in verify's results and in `ape status`, and the gate is red
// for them.
func TestVerify_VacuousAndUnrun(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: greeter\nrequires: {GREETER_DSN: env}\n"
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	files["development/specs/greeting/spec.md"] = greetingSpec + `
### Requirement: Store greetings
The system SHALL store each greeting.
#### Scenario: stored
- GIVEN a database WHEN greet runs THEN the greeting is stored
- check: ./greet.sh Ann # needs $GREETER_DSN
`
	p := fixture(t, files)
	t.Setenv("GREETER_DSN", "")
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave,
		"- T1 add wave — check: ./wave.sh\n- T2 wave is tested — check: ./fake-go-test.sh\n"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	write(t, p, "fake-go-test.sh", "#!/bin/sh\nprintf 'ok  \\tgithub.com/x/greet\\t0.003s [no tests to run]\\n'\n")
	commitAll(t, p, "feat: wave")

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.False(t, v.Green)
	byRef := map[string]CheckResult{}
	for i := range v.Results {
		byRef[v.Results[i].Task] = v.Results[i]
	}
	t2 := byRef["T2"]
	require.Equal(t, ReasonVacuous, t2.Reason)
	require.Equal(t, ExitVacuous, t2.Exit)
	require.Contains(t, t2.Detail, `"no tests to run"`)
	require.True(t, t2.Red())
	stored := byRef["greeting/Store greetings/stored"]
	require.Equal(t, ReasonUnrun, stored.Reason)
	require.Equal(t, ExitUnrun, stored.Exit)
	require.Equal(t, "$GREETER_DSN is unset (requires: GREETER_DSN: env)", stored.Detail)
	log, err := os.ReadFile(filepath.Join(p.Root, stored.Log))
	require.NoError(t, err)
	require.Contains(t, string(log), "unrun")
	require.Equal(t, 0, byRef["T1"].Exit)

	cs := p.Status().Changes[0]
	failing := strings.Join(cs.Failing, "; ")
	require.Contains(t, failing, "T2 (vacuous: ")
	require.Contains(t, failing, "greeting/Store greetings/stored (unrun: $GREETER_DSN is unset")

	// With the variable set, the scenario runs and passes.
	t.Setenv("GREETER_DSN", "postgres://localhost/greet")
	write(t, p, "fake-go-test.sh", "#!/bin/sh\nprintf 'ok  \\tgithub.com/x/greet\\t0.2s\\n'\n")
	commitAll(t, p, "test: wave")
	v, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green, "%+v", v.Results)

	// The gate runs the whole spec: unrun is red there too.
	t.Setenv("GREETER_DSN", "")
	run(t, p.Root, "switch", "-q", "main")
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.False(t, g.Run.Green())
	require.Contains(t, g.Run.Why(), "greeting/Store greetings/stored (unrun)")
}

// H11: a gate command that needs a program the machine lacks is unrun,
// not green, and an unrun regression command fails however red its
// baseline was.
func TestGate_UnrunCommand(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: greeter\nrequires: {no-such-tool-xyz: cmd}\n"
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Gate: ./gate.sh", "Gate: no-such-tool-xyz --smoke", 1)
	p := fixture(t, files)
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.False(t, g.Run.Green())
	require.Equal(t, ExitUnrun, g.Run.Exit)
	require.Equal(t, ReasonUnrun, g.Run.Reason)
	require.Contains(t, g.Run.Why(), "gate command unrun: no-such-tool-xyz is not on PATH")

	r := CheckResult{Cmd: "suite", Exit: ExitUnrun, Reason: ReasonUnrun, Detail: "x"}
	v := judgeRegression(&r, &RegressionBaseline{Cmd: "suite", Exit: 1}, "")
	require.True(t, v.Failed)
	require.False(t, v.PreExistingRed)
}
