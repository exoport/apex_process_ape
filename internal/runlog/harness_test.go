package runlog

import (
	"os"
	"path/filepath"
	"testing"
)

// stubClaudeVersion replaces the probe and clears the process-wide cache
// for one test, restoring both afterwards. Tests that use it cannot run in
// parallel: the probe and its cache are package state.
func stubClaudeVersion(t *testing.T, fn func(bin string) string) *int {
	t.Helper()
	prevFn, prevCache := claudeVersionFn, claudeVersionCache
	calls := 0
	claudeVersionFn = func(bin string) string {
		calls++
		return fn(bin)
	}
	claudeVersionCache = map[string]string{}
	t.Cleanup(func() {
		claudeVersionFn, claudeVersionCache = prevFn, prevCache
	})
	return &calls
}

func readHarness(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, HarnessFile))
	if err != nil {
		t.Fatalf("read %s: %v", HarnessFile, err)
	}
	return string(b)
}

func TestNew_StampsHarnessVersion(t *testing.T) {
	stubClaudeVersion(t, func(string) string { return "2.1.251 (Claude Code)" })
	dir := t.TempDir()

	w, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	if got, want := readHarness(t, dir), "claude_version: 2.1.251 (Claude Code)\n"; got != want {
		t.Errorf("harness.yaml = %q, want %q", got, want)
	}
}

// The default matters: only the pipeline runner lets the binary be
// overridden, so every other producer stamps whatever `claude` resolves to.
func TestNew_ProbesDefaultBinary(t *testing.T) {
	var seen string
	stubClaudeVersion(t, func(bin string) string {
		seen = bin
		return "2.1.251"
	})
	w, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	if seen != DefaultClaudeBin {
		t.Errorf("probed %q, want %q", seen, DefaultClaudeBin)
	}
}

func TestWithClaudeBin(t *testing.T) {
	for _, tc := range []struct {
		name string
		bin  string
		want string
	}{
		{"override is used", "/opt/claude", "/opt/claude"},
		// An unset override must not become a probe of "".
		{"empty keeps the default", "", DefaultClaudeBin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			stubClaudeVersion(t, func(bin string) string {
				seen = bin
				return "2.1.251"
			})
			w, err := New(t.TempDir(), WithClaudeBin(tc.bin))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer w.Close()

			if seen != tc.want {
				t.Errorf("probed %q, want %q", seen, tc.want)
			}
		})
	}
}

// New is also the reopen path — a resumed run appends to the same streams.
// Re-stamping there would relabel events an earlier harness wrote.
func TestNew_DoesNotRestampOnReopen(t *testing.T) {
	version := "2.1.250 (Claude Code)"
	stubClaudeVersion(t, func(string) string { return version })
	dir := t.TempDir()

	w1, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w1.Close()

	version = "2.1.251 (Claude Code)"
	claudeVersionCache = map[string]string{} // simulate a new process
	w2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer w2.Close()

	if got, want := readHarness(t, dir), "claude_version: 2.1.250 (Claude Code)\n"; got != want {
		t.Errorf("harness.yaml = %q, want the version that opened the run (%q)", got, want)
	}
}

// A probe failure must leave no file. An empty value would assert that the
// run happened under no version; absent lets hookdrift fall back.
func TestNew_NoStampWhenProbeFails(t *testing.T) {
	stubClaudeVersion(t, func(string) string { return "" })
	dir := t.TempDir()

	w, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	if _, err := os.Stat(filepath.Join(dir, HarnessFile)); !os.IsNotExist(err) {
		t.Errorf("stat harness.yaml = %v, want not-exist", err)
	}
}

// A missing claude must not fail the run — the stamp is diagnostic.
func TestNew_SucceedsWithoutClaude(t *testing.T) {
	stubClaudeVersion(t, func(string) string { return "" })
	w, err := New(t.TempDir(), WithClaudeBin("/nonexistent/claude"))
	if err != nil {
		t.Fatalf("New must not fail when the harness cannot be probed: %v", err)
	}
	w.Close()
}

// `ape service` opens many runlogs in one process; each must not re-exec.
func TestClaudeVersion_ProbedOncePerBinary(t *testing.T) {
	calls := stubClaudeVersion(t, func(string) string { return "2.1.251" })

	for range 3 {
		w, err := New(t.TempDir())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		w.Close()
	}

	if *calls != 1 {
		t.Errorf("probed %d times, want 1", *calls)
	}
}
