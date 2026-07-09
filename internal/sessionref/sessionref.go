// Package sessionref resolves which Claude Code session a reporting
// command (PLAN-17 `ape event`/`log`/`metrics`/`transcript`) targets. It
// is pure filesystem logic — no NATS, no scanning — so it is fully
// table-testable against a fake ~/.claude tree.
//
// Resolution order (PLAN-17 D2), first match wins:
//
//  1. --session-id <uuid>     explicit id; its transcript is looked up best-effort.
//  2. --transcript <path>     explicit file; the id is parsed from the filename.
//  3. APE_SESSION_ID (env)    set by ape's runners in-run, or by a SessionStart hook.
//  4. auto-detect             the newest transcript for the current project.
//
// Auto-detect first tries the project's own directory
// (~/.claude/projects/<cwd-slug>/) and, if that yields nothing, falls back
// to matching any transcript whose recorded `cwd` equals the project root —
// robust to drift in Claude's directory-slug algorithm. Auto-detect is
// heuristic (newest wins); the env var is the reliable path for concurrent
// sessions and is documented as the recommended agent setup.
package sessionref

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EnvSessionID is the environment variable carrying the current Claude
// Code session id (resolution step 3).
const EnvSessionID = "APE_SESSION_ID"

// Source identifies which resolution step produced a Ref (diagnostics).
type Source string

const (
	SourceSessionIDFlag  Source = "session-id-flag"
	SourceTranscriptFlag Source = "transcript-flag"
	SourceEnv            Source = "env"
	SourceAuto           Source = "auto"
)

// Ref is a resolved session reference.
type Ref struct {
	// SessionID is the Claude session uuid (always set on success).
	SessionID string
	// Transcript is the main transcript's absolute path. It is best-effort
	// for the id/env sources (empty when the file can't be located) and
	// always set for the transcript-flag and auto sources. Commands that
	// must scan or upload (metrics/transcript) require it; event/log do not.
	Transcript string
	// Source records which step resolved the ref.
	Source Source
}

// Options are the resolver inputs. Home and Getenv are injectable for
// tests; zero values use the real environment.
type Options struct {
	SessionID  string // --session-id
	Transcript string // --transcript
	Cwd        string // project root (--cwd); defaults to os.Getwd()
	Home       string // ~ override (tests); defaults to os.UserHomeDir()
	Getenv     func(string) string
}

// UnresolvedError is the sentinel for "no session could be resolved" — the
// caller maps it to a usage/config exit code (exit 2). Its message lists
// where the resolver looked.
type UnresolvedError struct{ msg string }

func (e *UnresolvedError) Error() string { return e.msg }

// Resolve applies the four-step order and returns the resolved Ref.
func Resolve(opts Options) (Ref, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	home := opts.Home
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Ref{}, fmt.Errorf("sessionref: home dir: %w", err)
		}
		home = h
	}
	projectsRoot := filepath.Join(home, ".claude", "projects")

	// 1. --session-id
	if id := strings.TrimSpace(opts.SessionID); id != "" {
		return Ref{SessionID: id, Transcript: findByID(projectsRoot, id), Source: SourceSessionIDFlag}, nil
	}

	// 2. --transcript
	if tp := strings.TrimSpace(opts.Transcript); tp != "" {
		abs, err := filepath.Abs(tp)
		if err != nil {
			return Ref{}, fmt.Errorf("sessionref: --transcript path: %w", err)
		}
		if info, statErr := os.Stat(abs); statErr != nil || info.IsDir() {
			return Ref{}, &UnresolvedError{msg: fmt.Sprintf("sessionref: --transcript %s does not exist", abs)}
		}
		return Ref{SessionID: sessionIDFromTranscript(abs), Transcript: abs, Source: SourceTranscriptFlag}, nil
	}

	// 3. APE_SESSION_ID
	if id := strings.TrimSpace(getenv(EnvSessionID)); id != "" {
		return Ref{SessionID: id, Transcript: findByID(projectsRoot, id), Source: SourceEnv}, nil
	}

	// 4. auto-detect newest transcript for the project.
	cwd := opts.Cwd
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return Ref{}, fmt.Errorf("sessionref: getwd: %w", err)
		}
		cwd = wd
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return Ref{}, fmt.Errorf("sessionref: --cwd path: %w", err)
	}
	if path := autoDetect(projectsRoot, absCwd); path != "" {
		return Ref{SessionID: sessionIDFromTranscript(path), Transcript: path, Source: SourceAuto}, nil
	}
	return Ref{}, &UnresolvedError{msg: fmt.Sprintf(
		"sessionref: no Claude session transcript found for %s (looked in %s); pass --session-id or --transcript",
		absCwd, filepath.Join(projectsRoot, ProjectSlug(absCwd)))}
}

// autoDetect returns the newest transcript for cwd: first the project's own
// slug directory, else any transcript whose recorded cwd matches.
func autoDetect(projectsRoot, cwd string) string {
	slugDir := filepath.Join(projectsRoot, ProjectSlug(cwd))
	if p := newestJSONL(slugDir); p != "" {
		return p
	}
	return newestMatchingCwd(projectsRoot, cwd)
}

// newestJSONL returns the newest-mtime *.jsonl directly under dir, or "".
func newestJSONL(dir string) string {
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return ""
	}
	return newestOf(matches)
}

// newestMatchingCwd scans every project's transcripts newest-first and
// returns the first whose recorded `cwd` equals the target — robust when
// Claude's slug algorithm doesn't match ProjectSlug. Reads only as far down
// the mtime-sorted list as needed to find a match.
func newestMatchingCwd(projectsRoot, cwd string) string {
	matches, err := filepath.Glob(filepath.Join(projectsRoot, "*", "*.jsonl"))
	if err != nil {
		return ""
	}
	sortByMtimeDesc(matches)
	for _, p := range matches {
		if transcriptCwd(p) == cwd {
			return p
		}
	}
	return ""
}

// newestOf returns the newest-mtime regular file among paths, or "".
func newestOf(paths []string) string {
	best, bestMod := "", int64(-1)
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if m := info.ModTime().UnixNano(); m > bestMod {
			best, bestMod = p, m
		}
	}
	return best
}

func sortByMtimeDesc(paths []string) {
	mod := make(map[string]int64, len(paths))
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			mod[p] = info.ModTime().UnixNano()
		}
	}
	sort.SliceStable(paths, func(i, j int) bool { return mod[paths[i]] > mod[paths[j]] })
}

// transcriptCwd reads a transcript's recorded working directory from the
// first line that carries a non-empty `cwd`. Returns "" on any error.
func transcriptCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for range 50 { // cwd appears on the first user/system line
		var line struct {
			Cwd string `json:"cwd"`
		}
		if err := dec.Decode(&line); err != nil {
			return ""
		}
		if line.Cwd != "" {
			if abs, err := filepath.Abs(line.Cwd); err == nil {
				return abs
			}
			return line.Cwd
		}
	}
	return ""
}

// FindTranscript locates a session's main transcript on disk by id, using
// the real home dir — ~/.claude/projects/*/<id>.jsonl. Returns "" when it
// isn't found. Used by the runner at finalize to publish per-session
// metrics through the same scan path a standalone `ape metrics` uses.
func FindTranscript(id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return findByID(filepath.Join(home, ".claude", "projects"), id)
}

// findByID globs ~/.claude/projects/*/<id>.jsonl and returns the first
// match (best-effort; empty when the transcript isn't on disk).
func findByID(projectsRoot, id string) string {
	matches, err := filepath.Glob(filepath.Join(projectsRoot, "*", id+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return newestOf(matches)
}

// sessionIDFromTranscript derives the session id from a main transcript
// filename (<sid>.jsonl).
func sessionIDFromTranscript(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// ProjectSlug derives Claude Code's project-directory name from an absolute
// path: every non-alphanumeric rune becomes '-' (so `/home/u/_dev/x.y` →
// `-home-u--dev-x-y`), matching the observed ~/.claude/projects/<slug>
// layout. Consecutive specials are NOT collapsed — the doubled dash is part
// of the real scheme.
func ProjectSlug(absPath string) string {
	var b strings.Builder
	b.Grow(len(absPath))
	for _, r := range absPath {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// AsUnresolved reports whether err is an UnresolvedError (exit-code mapping).
func AsUnresolved(err error) bool {
	var e *UnresolvedError
	return errors.As(err, &e)
}
