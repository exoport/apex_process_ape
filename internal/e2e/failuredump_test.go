//go:build !windows

package e2e

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dumpOnFailure logs what a failed test's runs left behind — each run's
// manifest, logs and hook events, and the fake claude's notes — before
// t.TempDir removes them. D572: TestReviewRun_ModelAndEffort failed once in
// ci-local with review.no-verdict and the log that would have said why went
// with the temp dir. KEEP_FAILED_TMP=1 also copies the dirs somewhere kept.
// Call it after the dirs are created: cleanups run last-registered first.
func dumpOnFailure(t *testing.T, dirs ...string) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, dir := range dirs {
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					if d != nil && d.IsDir() && (d.Name() == ".git" || d.Name() == "transcripts") {
						return filepath.SkipDir
					}
					return nil
				}
				if !dumpWorthy(p, dir) {
					return nil
				}
				data, err := os.ReadFile(p)
				if err != nil {
					return nil
				}
				t.Logf("=== %s\n%s", p, tailBytes(string(data), 8<<10))
				return nil
			})
		}
		if os.Getenv("KEEP_FAILED_TMP") != "1" {
			return
		}
		kept, err := os.MkdirTemp("", "ape-failed-"+strings.ReplaceAll(t.Name(), "/", "_")+"-")
		if err != nil {
			return
		}
		for i, dir := range dirs {
			_ = os.CopyFS(filepath.Join(kept, strings.Repeat("d", i+1)), os.DirFS(dir))
		}
		t.Logf("KEEP_FAILED_TMP: kept in %s", kept)
	})
}

// dumpWorthy: ape's records under _apex/state (runs, logs, events), and
// anything at the top of a fake claude's dir (what the fake saw and did).
func dumpWorthy(p, root string) bool {
	rel, _ := filepath.Rel(root, p)
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "_apex/") {
		return !strings.Contains(rel, "/") && filepath.Base(p) != "claude"
	}
	if !strings.HasPrefix(rel, "_apex/state/") {
		return false
	}
	switch filepath.Ext(p) {
	case ".yaml", ".log", ".out", ".jsonl", ".json", ".md", ".txt":
		return true
	}
	return false
}

// tailBytes keeps the last n bytes of s.
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
