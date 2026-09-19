package change

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Residue: the edits a run left in the tree that ape did not commit.
//
// Every exit that leaves product edits behind saves them here — the
// refusals, the halt, and the dispatches that died mid-edit. ape still
// never restores the tree: it saves what is there so the operator, or
// the persona on the operator's word, can clear the tree without losing
// the work.
//
// Two artifacts, because one does not cover it:
//
//   - residue.patch holds the tracked changes, written with PLUMBING.
//     Porcelain `git diff` output fails `git apply` under
//     `color.ui=always` or `diff.noprefix=true`, both of which are
//     ordinary things to have in a global config, and a patch that does
//     not apply is worse than no patch because it looks like one;
//   - untracked/ holds a byte copy of every untracked file, at its own
//     path. A patch cannot carry a file git has never seen, and a listed
//     name keeps no content.
//
// The patch is self-tested with `git apply --check -R` before it is
// offered: reversing it against the current tree is the closest thing to
// "would this restore what is here", and a patch that fails its own
// check is reported as failed rather than left to be discovered later.
//
//nolint:tagliatelle // snake_case is the wire contract, as it is for ape task
type Residue struct {
	// Paths is every path left in the tree.
	Paths []string `json:"paths" yaml:"paths"`
	// PatchPath and UntrackedDir are project-relative, or empty when
	// there was nothing of that kind to save.
	PatchPath    string `json:"patch_path,omitempty"    yaml:"patch_path,omitempty"`
	UntrackedDir string `json:"untracked_dir,omitempty" yaml:"untracked_dir,omitempty"`
	// Truncated marks a patch too large to save whole. The file then
	// holds a marker instead of a partial patch, which would apply
	// cleanly up to the cut and silently lose the rest.
	Truncated bool `json:"truncated,omitempty" yaml:"truncated,omitempty"`
	// Verified records that the saved patch passed `git apply --check -R`.
	Verified bool `json:"verified" yaml:"verified"`
	// Note carries a failure that must not fail the run: the residue is
	// a diagnostic saved after the work is done.
	Note string `json:"note,omitempty" yaml:"note,omitempty"`
}

// maxPatchBytes caps residue.patch. Large enough for any hand-sized
// change, small enough that a runaway generated file does not fill the
// change directory.
const maxPatchBytes = 8 << 20

// truncationMarker replaces the patch when it is over the cap. It is
// deliberately NOT a valid patch: a partial one would apply up to the
// cut and lose the rest without saying so.
const truncationMarker = `ape: the working tree's diff was larger than the cap for a saved patch.
Nothing was written here, because half a patch applies cleanly and loses the rest.
The changes are still in the working tree; the paths are listed in change.yaml.
`

// Save writes the residue into dir and returns what it saved.
//
// It runs AFTER ape's own commits, so what it captures is what is
// genuinely left over. It takes a context that survives cancellation:
// the run's own context dies on SIGINT or SIGTERM, and it would take
// these git invocations with it — losing the work at exactly the moment
// the operator most needs it saved.
func Save(ctx context.Context, root, dir string) (*Residue, error) {
	ctx = context.WithoutCancel(ctx)

	changed, err := Changed(ctx, root)
	if err != nil {
		return nil, err
	}
	res := &Residue{Paths: changed}
	if len(changed) == 0 {
		return res, nil // a clean tree leaves nothing to save
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create the residue directory: %w", err)
	}

	patch, patchErr := trackedPatch(ctx, root)
	switch {
	case patchErr != nil:
		res.Note = "the tracked changes could not be captured: " + patchErr.Error()
	case len(patch) == 0:
		// Everything left over is untracked; the patch would be empty.
	case len(patch) > maxPatchBytes:
		res.Truncated = true
		res.PatchPath = filepath.Join(dir, "residue.patch")
		if writeErr := os.WriteFile(res.PatchPath, []byte(truncationMarker), 0o600); writeErr != nil {
			return nil, fmt.Errorf("write the truncation marker: %w", writeErr)
		}
	default:
		res.PatchPath = filepath.Join(dir, "residue.patch")
		if writeErr := os.WriteFile(res.PatchPath, patch, 0o600); writeErr != nil {
			return nil, fmt.Errorf("write the residue patch: %w", writeErr)
		}
		if checkErr := applyCheckReverse(ctx, root, res.PatchPath); checkErr != nil {
			res.Note = "the saved patch does not pass `git apply --check -R`: " + checkErr.Error()
		} else {
			res.Verified = true
		}
	}

	saved, untrackedErr := saveUntracked(ctx, root, dir)
	if untrackedErr != nil {
		res.Note = strings.TrimSpace(res.Note + " " +
			"the untracked files could not be copied: " + untrackedErr.Error())
	} else if saved > 0 {
		res.UntrackedDir = filepath.Join(dir, "untracked")
	}
	return res, nil
}

// trackedPatch renders the working tree's difference from HEAD with
// plumbing, so no porcelain setting can make the output unappliable.
//
// --binary carries a changed binary file, which a text diff would report
// and not reproduce; --full-index writes whole blob ids, so the patch
// applies against a repository whose short-hash length differs.
func trackedPatch(ctx context.Context, root string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git",
		"diff-index", "-p", "--binary", "--full-index", "HEAD", "--")
	cmd.Dir = root
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff-index: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// applyCheckReverse asks git whether the patch reverses cleanly against
// the tree it came from.
func applyCheckReverse(ctx context.Context, root, patchPath string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "apply", "--check", "-R", patchPath)
	cmd.Dir = root
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// saveUntracked copies every untracked file under dir/untracked/, path
// and all, and returns how many it saved.
func saveUntracked(ctx context.Context, root, dir string) (int, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--others", "--exclude-standard", "-z")
	cmd.Dir = root
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("git ls-files --others: %w", err)
	}
	saved := 0
	for rel := range strings.SplitSeq(stdout.String(), "\x00") {
		if rel == "" {
			continue
		}
		src := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(src)
		if err != nil {
			// A file that vanished between the listing and the read, or
			// one ape may not read. Neither is worth failing the save
			// for: the path is still listed in Paths.
			continue
		}
		dst := filepath.Join(dir, "untracked", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return saved, fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return saved, fmt.Errorf("copy %s: %w", rel, err)
		}
		saved++
	}
	return saved, nil
}
