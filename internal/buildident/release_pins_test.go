package buildident

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestReleaseWorkflowPinsGoreleaser holds release.yml's goreleaser to the bingo
// pin. `make snapshot` (in ci-local) builds with .bingo/goreleaser.mod's version;
// a workflow installing another one, or `latest`, publishes a release built by a
// goreleaser no local gate ran (V15-REL-GORELEASER-PIN).
func TestReleaseWorkflowPinsGoreleaser(t *testing.T) {
	root := filepath.Join("..", "..")
	// A Windows CI checkout converts both files to CRLF.
	read := func(rel ...string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(append([]string{root}, rel...)...))
		if err != nil {
			t.Fatal(err)
		}
		return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	}
	mod := read(".bingo", "goreleaser.mod")
	m := regexp.MustCompile(`(?m)^require github\.com/goreleaser/goreleaser/v2 (v\d+\.\d+\.\d+)$`).FindSubmatch(mod)
	if m == nil {
		t.Fatalf(".bingo/goreleaser.mod has no goreleaser require line:\n%s", mod)
	}
	pin := string(m[1])

	wf := read(".github", "workflows", "release.yml")
	step := regexp.MustCompile(`(?s)uses: goreleaser/goreleaser-action@\S+\s+with:\n((?:\s{10}\S.*\n)+)`).FindSubmatch(wf)
	if step == nil {
		t.Fatal("release.yml has no goreleaser-action step with a `with:` block")
	}
	v := regexp.MustCompile(`(?m)^\s+version: (\S+)$`).FindSubmatch(step[1])
	if v == nil {
		t.Fatalf("release.yml's goreleaser step sets no version:\n%s", step[1])
	}
	if got := string(v[1]); got != pin {
		t.Fatalf("release.yml installs goreleaser %q; the bingo pin is %q", got, pin)
	}
}
