package sprint

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ReconcileChange is one epic row the projection moved.
type ReconcileChange struct {
	Epic   int    `json:"epic"   yaml:"epic"`
	Key    string `json:"key"    yaml:"key"`
	From   string `json:"from"   yaml:"from"`
	To     string `json:"to"     yaml:"to"`
	Reason string `json:"reason" yaml:"reason"`
}

// ReconcileResult is the payload of `ape sprint reconcile`.
type ReconcileResult struct {
	Tracker string            `json:"tracker" yaml:"tracker"`
	Changes []ReconcileChange `json:"changes" yaml:"changes"`
	// Unchanged names the epics the projection deliberately left alone,
	// with why — "no rows" and "all cancelled" are decisions, not misses.
	Unchanged []ReconcileSkip `json:"unchanged,omitempty" yaml:"unchanged,omitempty"`
	// Unrecognised lists status values the projection did not know. They
	// can only hold an epic open, never close it, and are named rather
	// than swallowed.
	Unrecognised []string `json:"unrecognised_statuses,omitempty" yaml:"unrecognised_statuses,omitempty"`
	// BareRowKeys names story rows written as `N-M` with no separator and
	// slug, which THIS projection counted and the reconcile-epic-status.py
	// it replaces would not have. Reported on every run that reads such a
	// row — including a no-op — because the divergence is in what was
	// counted, not in what was written, and a run that changed nothing can
	// still have reached that answer differently from the script.
	BareRowKeys []string `json:"bare_row_keys,omitempty" yaml:"bare_row_keys,omitempty"`
	// BareRowKeyRemediation is the fix, carried in the payload so a caller
	// acting on BareRowKeys does not have to know it.
	BareRowKeyRemediation string `json:"bare_row_key_remediation,omitempty" yaml:"bare_row_key_remediation,omitempty"`
	Written               bool   `json:"written"                            yaml:"written"`
	UpdatedAt             string `json:"updated_at,omitempty"               yaml:"updated_at,omitempty"`
	// Clamped reports that updated_at was held at its existing value
	// because the supplied timestamp was earlier. Never fatal.
	Clamped bool `json:"clamped" yaml:"clamped"`
}

// ReconcileSkip records an epic left unchanged, and why.
type ReconcileSkip struct {
	Epic   int    `json:"epic"   yaml:"epic"`
	Reason string `json:"reason" yaml:"reason"`
}

// Changed reports whether anything moved.
func (r *ReconcileResult) Changed() bool { return len(r.Changes) > 0 }

// ReconcileOptions selects the scope.
type ReconcileOptions struct {
	// Epic reconciles one epic; zero with All false is an error.
	Epic int
	All  bool
	// Timestamp is written to the body `updated_at` on mutation. Clamped
	// forward: never moves backwards.
	Timestamp string
	// Check makes it a dry run.
	Check bool
}

// Reconcile projects each in-scope epic's row from its story rows and
// writes the result.
//
// Three properties are load-bearing, and all three are easy to lose in a
// port from the Python:
//
//  1. The write is TARGETED. Only the matched `epic-N:` line and the body
//     `updated_at` change; comments, key order, story rows and the
//     sync-generated header are untouched. That rules out the obvious
//     implementation — unmarshal, mutate, re-marshal — because yaml.v3
//     discards comments and normalises formatting.
//  2. updated_at never moves backwards. Clamped, reported, never fatal.
//  3. The read-modify-write is locked. Concurrent per-epic sub-agents
//     reconcile the SAME file, and without a lock the last writer
//     silently drops a sibling's update.
func Reconcile(path string, opts ReconcileOptions) (*ReconcileResult, error) {
	if !opts.All && opts.Epic <= 0 {
		return nil, errors.New("reconcile needs --epic <N> or --all")
	}
	// Non-nil so a no-op run marshals as `"changes": []`, not `null`.
	res := &ReconcileResult{Tracker: path, Changes: []ReconcileChange{}}

	unlock, err := lockFile(path)
	if err != nil {
		return nil, err
	}
	defer unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s does not exist", path)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	tracker, err := Load(path)
	if err != nil {
		return nil, err
	}

	epics := []int{opts.Epic}
	if opts.All {
		epics = EpicNumbers(tracker.Rows)
	}

	// Say what was counted differently, before saying what was written.
	if bare := BareStoryRowKeys(tracker.Rows); len(bare) > 0 {
		res.BareRowKeys = bare
		res.BareRowKeyRemediation = BareRowKeyRemediation
	}

	edited := data
	seenUnrecognised := map[string]bool{}
	for _, epic := range epics {
		if epic <= 0 {
			continue
		}
		key := fmt.Sprintf("epic-%d", epic)
		current, hasRow := tracker.RowStatus(key)
		if !HasStoryRows(tracker.Rows, epic) {
			res.Unchanged = append(res.Unchanged, ReconcileSkip{
				Epic: epic, Reason: "no story rows — never close an epic that has none",
			})
			continue
		}
		target, unrecognised := Project(tracker.Rows, epic)
		for _, u := range unrecognised {
			if !seenUnrecognised[u] {
				seenUnrecognised[u] = true
				res.Unrecognised = append(res.Unrecognised, u)
			}
		}
		if target == "" {
			res.Unchanged = append(res.Unchanged, ReconcileSkip{
				Epic: epic, Reason: "every story row is cancelled — a scope decision, not a closure",
			})
			continue
		}
		if !hasRow {
			res.Unchanged = append(res.Unchanged, ReconcileSkip{
				Epic: epic, Reason: "no epic-" + strconv.Itoa(epic) + " row in the tracker to update",
			})
			continue
		}
		if current == target {
			continue
		}
		next, ok := setRowLine(edited, key, target)
		if !ok {
			res.Unchanged = append(res.Unchanged, ReconcileSkip{
				Epic: epic, Reason: "row line not found in the file text",
			})
			continue
		}
		edited = next
		res.Changes = append(res.Changes, ReconcileChange{
			Epic: epic, Key: key, From: current, To: target,
			Reason: "projected from its story rows",
		})
	}

	if !res.Changed() {
		return res, nil
	}

	// updated_at moves only on mutation, and only forwards.
	if opts.Timestamp != "" {
		existing := tracker.TopLevelString("updated_at")
		stamp := opts.Timestamp
		if existing != "" && stamp < existing {
			stamp = existing
			res.Clamped = true
		}
		if next, ok := setTopLevelScalar(edited, "updated_at", stamp); ok {
			edited = next
			res.UpdatedAt = stamp
		}
	}

	if opts.Check {
		return res, nil
	}
	if err := writeFileAtomic(path, edited); err != nil {
		return nil, err
	}
	if err := verifyWriteLanded(path, tracker, res.Changes); err != nil {
		// RESTORE. A tracker left corrupt on disk is the failure this whole
		// command is built to avoid — every later writer reads it, and the
		// build loop's source of truth is gone. Exiting non-zero over a
		// broken file is not a safe landing; putting the original bytes back
		// and saying so is.
		if restoreErr := writeFileAtomic(path, data); restoreErr != nil {
			return nil, fmt.Errorf(
				"%w; CRITICAL: restore also failed (%w) — recover %s from git",
				err, restoreErr, path,
			)
		}
		return nil, fmt.Errorf("%w; the file was restored to its pre-edit content", err)
	}
	res.Written = true
	return res, nil
}

// verifyWriteLanded re-reads the tracker and asserts this run changed
// exactly the rows it named and nothing else.
//
// The write is a line-level text edit, which is what preserves comments and
// ordering — and which is also why it needs checking: a regex that crossed a
// line boundary would produce a file that still parses and no longer means
// what it did. A targeted edit that alters a row it never named has
// corrupted the tracker, which is worse than not having run.
func verifyWriteLanded(path string, before *Tracker, changes []ReconcileChange) error {
	after, err := Load(path)
	if err != nil {
		return fmt.Errorf("round-trip verification failed: %w", err)
	}
	if after.Missing {
		return fmt.Errorf("round-trip verification failed: %s vanished", path)
	}
	applied := make(map[string]string, len(changes))
	for _, c := range changes {
		applied[c.Key] = c.To
	}
	for key, want := range applied {
		got, ok := after.RowStatus(key)
		if !ok {
			return fmt.Errorf("round-trip verification failed: row %q disappeared", key)
		}
		if got != want {
			return fmt.Errorf("round-trip verification failed: row %q reads %q, expected %q", key, got, want)
		}
	}
	if len(after.Rows) != len(before.Rows) {
		return fmt.Errorf("round-trip verification failed: row count changed %d -> %d",
			len(before.Rows), len(after.Rows))
	}
	for _, row := range before.Rows {
		if _, touched := applied[row.Key]; touched {
			continue
		}
		got, ok := after.RowStatus(row.Key)
		if !ok {
			return fmt.Errorf("round-trip verification failed: untouched row %q disappeared", row.Key)
		}
		if got != row.Status {
			return fmt.Errorf(
				"round-trip verification failed: untouched row %q changed %q -> %q",
				row.Key, row.Status, got,
			)
		}
	}
	return nil
}

// lineRe builds a matcher for one `key: value` line, splitting the parts
// that must survive a rewrite into their own groups:
//
//	1 indent   2 value (non-greedy)   3 gap before a comment   4 comment   5 CR
//
// Capturing the gap separately is not fussiness: folding it into the
// value loses the alignment of a trailing comment, which turns a
// "two lines changed" diff into a noisy one and breaks the promise that
// only the value moves.
func lineRe(indent, key string) *regexp.Regexp {
	return regexp.MustCompile(
		`(?m)^(` + indent + `)` + regexp.QuoteMeta(key) + `:[ \t]*([^\r\n#]*?)([ \t]*)(#[^\r\n]*)?(\r?)$`,
	)
}

// setLine replaces the value on a matching line, preserving indentation,
// the gap before any trailing comment, the comment itself, and the line
// ending. This is the targeted write: one line changes and every other
// byte of the file is identical.
func setLine(data []byte, indent, key, value string) ([]byte, bool) {
	// Capture-group indexes from lineRe, named so the assembly below reads
	// as the line it rebuilds.
	const (
		groupIndent  = 1
		groupGap     = 3
		groupComment = 4
		groupCR      = 5
	)
	loc := lineRe(indent, key).FindSubmatchIndex(data)
	if loc == nil {
		return data, false
	}
	part := func(n int) string {
		if loc[2*n] < 0 {
			return ""
		}
		return string(data[loc[2*n]:loc[2*n+1]])
	}
	replacement := part(groupIndent) + key + ": " + value +
		part(groupGap) + part(groupComment) + part(groupCR)
	var out bytes.Buffer
	out.Grow(len(data))
	out.Write(data[:loc[0]])
	out.WriteString(replacement)
	out.Write(data[loc[1]:])
	return out.Bytes(), true
}

// setRowLine replaces an indented row's value. The indent requirement is
// what keeps a row key from matching a top-level key of the same name.
func setRowLine(data []byte, key, value string) ([]byte, bool) {
	return setLine(data, `[ \t]+`, key, value)
}

// setTopLevelScalar replaces an unindented `key: value` line. Timestamps
// are digit strings, so the value is quoted: unquoted, the next read
// would decode it as an integer.
func setTopLevelScalar(data []byte, key, value string) ([]byte, bool) {
	quoted := value
	if !strings.HasPrefix(value, "'") && !strings.HasPrefix(value, `"`) {
		quoted = "'" + value + "'"
	}
	return setLine(data, ``, key, quoted)
}

// writeFileAtomic writes via a temp file in the same directory and a
// rename, so a crash mid-write cannot truncate the tracker.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ape-"+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp beside %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("rename onto %s: %w", path, err)
	}
	return nil
}
