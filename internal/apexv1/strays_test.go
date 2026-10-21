package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Item 19 (F3, taskforge D15 r3): a check that litters the tree must not
// leave the change unarchivable. ape undoes exactly what the check did —
// new untracked files (and the directories they made) and tracked files
// it changed — and reports it; nothing that was there before is touched.
func TestRunChecks_UndoesWhatTheCheckLeft(t *testing.T) {
	p := fixture(t, baseFiles())
	write(t, p, "mine.txt", "the author's own untracked file\n")
	write(t, p, "development/changes/draft/notes.md", "planning work\n")
	cmd := "mkdir -p out/deep && echo x > out/deep/report.txt && echo y > coverage.txt && echo z >> greet.sh && " +
		"echo w > development/changes/draft/by-check.md && echo v >> mine.txt"
	res := p.RunChecks(ctx(), p.LogDir("x"), "", []PlannedCheck{{Ref: "T1", Kind: KindTask, Cmd: cmd}}, RunOptions{})
	require.Equal(t, 0, res[0].Exit)
	require.Equal(t, []string{"coverage.txt", "greet.sh", "out/deep/report.txt"}, res[0].StrayFiles)

	require.NoFileExists(t, filepath.Join(p.Root, "coverage.txt"))
	require.NoDirExists(t, filepath.Join(p.Root, "out"), "the directories the check made go too")
	require.Equal(t, "#!/bin/sh\necho Hello $1\n", read(t, p, "greet.sh"), "a tracked file the check changed is restored")
	require.Contains(t, read(t, p, "mine.txt"), "the author's own", "a file untracked before is the author's, not the check's")
	require.FileExists(t, filepath.Join(p.Root, "development", "changes", "draft", "by-check.md"), "the development folder is never cleaned")
}

// The F3 case end to end: the change's check writes a stray file, and
// verify then archive still go through — the stray is reported, not left.
func TestArchive_AfterALitteringCheck(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave,
		"- T1 add wave — check: ./wave.sh && ./wave.sh > wave.out\n"))
	commitAll(t, p, "plan")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.Equal(t, []string{"wave.out"}, v.StrayFiles)
	_, err = os.Stat(filepath.Join(p.Root, "wave.out"))
	require.True(t, os.IsNotExist(err))

	write(t, p, "development/changes/add-wave/review.md", "ok\n")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
}

func TestGate_UndoesWhatItLeft(t *testing.T) {
	files := baseFiles()
	files["gate.sh"] = "#!/bin/sh\necho log > gate.log\nexit 0\n"
	p := fixture(t, files)
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.Run.Green())
	require.Equal(t, []string{"gate.log"}, g.Run.StrayFiles)
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
}

// RK-4b (Q144, D300): while a check runs, ape keeps a marker the pre-tool
// hook honours; what the cleanup undoes — the check's litter, and an edit
// another writer made meanwhile (a shell write the hook cannot see) — is
// kept as it was, never lost.
func TestRunChecks_KeepsWhatItUndoes(t *testing.T) {
	p := fixture(t, baseFiles())
	// The check's own litter, then a pause in which another writer edits.
	cmd := "echo litter > coverage.txt && sleep 2"
	writer := make(chan *CheckMarker, 1)
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if m := p.LiveCheck(); m != nil {
				// Not require: this is not the test's goroutine.
				e1 := os.WriteFile(filepath.Join(p.Root, "greet.sh"), []byte("#!/bin/sh\necho edited by the person\n"), 0o755)
				e2 := os.WriteFile(filepath.Join(p.Root, "new_test.sh"), []byte("the person's new file\n"), 0o644)
				if e1 != nil || e2 != nil {
					m = nil
				}
				writer <- m
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		writer <- nil
	}()
	res := p.RunChecks(ctx(), p.LogDir("x"), "", []PlannedCheck{{Ref: "T1", Kind: KindTask, Cmd: cmd}}, RunOptions{})
	m := <-writer
	require.NotNil(t, m, "the marker was up while the check ran")
	require.Equal(t, os.Getpid(), m.PID)
	require.Equal(t, cmd, m.Cmd)
	require.Nil(t, p.LiveCheck(), "and is gone once the cleanup is done")

	require.Equal(t, []string{"coverage.txt", "greet.sh", "new_test.sh"}, res[0].StrayFiles)
	require.Len(t, res[0].StraysKept, 1)
	kept := filepath.Join(p.Root, filepath.FromSlash(res[0].StraysKept[0]))
	require.True(t, strings.HasPrefix(res[0].StraysKept[0], p.StateRel()+"/strays/"), res[0].StraysKept[0])
	got := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(kept, rel))
		require.NoError(t, err)
		return string(data)
	}
	require.Equal(t, "litter\n", got("coverage.txt"), "the check's own stray is still removed, and kept")
	require.Equal(t, "#!/bin/sh\necho edited by the person\n", got("greet.sh"), "a concurrent edit survives, in the copy")
	require.Equal(t, "the person's new file\n", got("new_test.sh"))
	require.NoFileExists(t, filepath.Join(p.Root, "coverage.txt"))
	require.Equal(t, "#!/bin/sh\necho Hello $1\n", read(t, p, "greet.sh"))
	require.Equal(t, StraysKept(res), res[0].StraysKept)
	out, err := gitRaw(ctx(), p.Root, "status", "--porcelain")
	require.NoError(t, err)
	require.Empty(t, out, "the copies are under the ignored state folder")
}
