package sessionref

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Shared test literals (goconst).
const (
	testCwd   = "/home/u/proj"
	testEnvID = "sid-env"
)

// noEnv is a Getenv that always returns "" (no APE_SESSION_ID), so tests
// are isolated from the real environment.
func noEnv(string) string { return "" }

// writeTranscript creates <projectsRoot>/<slug>/<sid>.jsonl with a single
// user line recording cwd, and stamps its mtime.
func writeTranscript(t *testing.T, projectsRoot, slug, sid, cwd string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(projectsRoot, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	line := `{"type":"user","cwd":"` + cwd + `","sessionId":"` + sid + `","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveSessionIDFlag(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")
	cwd := testCwd
	want := writeTranscript(t, projects, ProjectSlug(cwd), "sid-explicit", cwd, time.Now())

	// Explicit id wins over everything and locates its transcript.
	ref, err := Resolve(Options{SessionID: "sid-explicit", Home: home, Cwd: cwd, Getenv: noEnv})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.SessionID != "sid-explicit" || ref.Source != SourceSessionIDFlag {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.Transcript != want {
		t.Fatalf("Transcript = %q, want %q", ref.Transcript, want)
	}

	// Missing-on-disk id still resolves (id set, transcript empty) — event/
	// log don't need the file.
	ref, err = Resolve(Options{SessionID: "sid-ghost", Home: home, Cwd: cwd, Getenv: noEnv})
	if err != nil {
		t.Fatalf("Resolve ghost: %v", err)
	}
	if ref.SessionID != "sid-ghost" || ref.Transcript != "" {
		t.Fatalf("ghost ref = %+v, want id set + empty transcript", ref)
	}
}

func TestResolveTranscriptFlag(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")
	path := writeTranscript(t, projects, "slug", "sid-from-file", "/x", time.Now())

	ref, err := Resolve(Options{Transcript: path, Home: home, Getenv: noEnv})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.SessionID != "sid-from-file" || ref.Source != SourceTranscriptFlag {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.Transcript != path {
		t.Fatalf("Transcript = %q, want %q", ref.Transcript, path)
	}

	// A non-existent --transcript is unresolvable (exit 2).
	_, err = Resolve(Options{Transcript: filepath.Join(home, "nope.jsonl"), Home: home, Getenv: noEnv})
	if !AsUnresolved(err) {
		t.Fatalf("missing transcript err = %v, want ErrUnresolved", err)
	}
}

func TestResolveEnv(t *testing.T) {
	home := t.TempDir()
	env := func(k string) string {
		if k == EnvSessionID {
			return testEnvID
		}
		return ""
	}
	ref, err := Resolve(Options{Home: home, Cwd: testCwd, Getenv: env})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.SessionID != testEnvID || ref.Source != SourceEnv {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestResolvePrecedence(t *testing.T) {
	home := t.TempDir()
	env := func(k string) string {
		if k == EnvSessionID {
			return testEnvID
		}
		return ""
	}
	// session-id flag beats env.
	ref, err := Resolve(Options{SessionID: "sid-flag", Home: home, Cwd: "/x", Getenv: env})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.SessionID != "sid-flag" {
		t.Fatalf("precedence: got %q, want sid-flag (flag over env)", ref.SessionID)
	}
}

func TestResolveAutoNewestInSlugDir(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")
	cwd := testCwd
	slug := ProjectSlug(cwd)
	old := time.Now().Add(-1 * time.Hour)
	writeTranscript(t, projects, slug, "sid-old", cwd, old)
	writeTranscript(t, projects, slug, "sid-new", cwd, time.Now())

	ref, err := Resolve(Options{Home: home, Cwd: cwd, Getenv: noEnv})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.SessionID != "sid-new" || ref.Source != SourceAuto {
		t.Fatalf("ref = %+v, want newest sid-new via auto", ref)
	}
}

func TestResolveAutoCwdFallback(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")
	cwd := testCwd
	// Transcript filed under a WRONG slug dir (simulating a slug-algorithm
	// mismatch), but its recorded cwd matches — the fallback must find it.
	writeTranscript(t, projects, "totally-different-dir", "sid-cwd", cwd, time.Now())
	// A newer transcript for a DIFFERENT project must not be chosen.
	writeTranscript(t, projects, "other-proj", "sid-other", "/somewhere/else", time.Now().Add(time.Hour))

	ref, err := Resolve(Options{Home: home, Cwd: cwd, Getenv: noEnv})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.SessionID != "sid-cwd" {
		t.Fatalf("cwd-fallback: got %q, want sid-cwd", ref.SessionID)
	}
}

func TestResolveUnresolved(t *testing.T) {
	home := t.TempDir()
	_, err := Resolve(Options{Home: home, Cwd: "/home/u/empty", Getenv: noEnv})
	if !AsUnresolved(err) {
		t.Fatalf("err = %v, want ErrUnresolved", err)
	}
}

func TestProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/home/diegos/_dev/exoport/apex_process_ape": "-home-diegos--dev-exoport-apex-process-ape",
		"/tmp/a.b": "-tmp-a-b",
		"/x":       "-x",
		"relative": "relative",
	}
	for in, want := range cases {
		if got := ProjectSlug(in); got != want {
			t.Errorf("ProjectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
