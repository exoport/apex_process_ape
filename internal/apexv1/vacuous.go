package apexv1

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// H11: a check that passes by running nothing is not green. Two ways it
// happens, both language-neutral and configured in _apex/config.yaml:
//
//   - vacuous: the runner exits 0 and says it ran nothing ("no tests to
//     run") — a `-run` for a test not written yet, or renamed since (a
//     MODIFIED block cannot change a scenario's check, so a renamed test
//     leaves its old name in the spec). checks.vacuous_output lists the
//     phrases; the result is red, reason "vacuous".
//   - unrun: the check needs what the machine lacks — an environment
//     variable its tests skip without, a program it calls (requires:). It
//     is not run at all; the result is red, reason "unrun", never green.
//
// Both record a sentinel exit (ExitVacuous, ExitUnrun) so every reader
// that judges by exit — verify, the baseline, the gate, the stop hook,
// status — counts them red; Reason and Detail say why.

// Check result reasons, and their sentinel exits (a real process exits
// 0–255, so a negative exit is always ape's).
const (
	ReasonVacuous = "vacuous"
	ReasonUnrun   = "unrun"
	ExitVacuous   = -2
	ExitUnrun     = -3
)

// vacuousPhrase returns the phrase that makes a zero-exit output vacuous,
// "" when none does.
//
// A line says a phrase when it contains it with a word boundary on each
// side, so "10 passing" does not say "0 passing". The output is vacuous
// when some line says a phrase and no line that says none starts with the
// same word as one that does. That keeps a multi-unit run honest: `go test
// ./... -run TestX` that ran TestX in one package prints `ok pkgA 0.4s`,
// which shares "ok" with the `ok pkgB 0.01s [no tests to run]` lines
// beside it, so it is not vacuous; a cargo run whose other binary printed
// `running 3 tests` is not either. Lines of other shapes — `PASS`, a
// make recipe echoing its command — neither make nor unmake it.
func vacuousPhrase(output string, phrases []string) string {
	if len(phrases) == 0 {
		return ""
	}
	said := ""
	vacuousFirst := map[string]bool{}
	var plainFirst []string
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		hit := ""
		for _, ph := range phrases {
			if containsWord(line, ph) {
				hit = ph
				break
			}
		}
		if hit == "" {
			plainFirst = append(plainFirst, fields[0])
			continue
		}
		if said == "" {
			said = hit
		}
		vacuousFirst[fields[0]] = true
	}
	if said == "" {
		return ""
	}
	for _, w := range plainFirst {
		if vacuousFirst[w] {
			return ""
		}
	}
	return said
}

// containsWord reports whether s contains phrase with no letter or digit
// glued to either end of it.
func containsWord(s, phrase string) bool {
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	first, _ := utf8.DecodeRuneInString(phrase)
	last, _ := utf8.DecodeLastRuneInString(phrase)
	for off := 0; off < len(s); {
		i := strings.Index(s[off:], phrase)
		if i < 0 {
			return false
		}
		i += off
		before, _ := utf8.DecodeLastRuneInString(s[:i])
		after, _ := utf8.DecodeRuneInString(s[i+len(phrase):])
		if (!isWord(first) || !isWord(before)) && (!isWord(last) || !isWord(after)) {
			return true
		}
		off = i + 1
	}
	return false
}

// unmetRequirement names the first requirement (by name) that cmd needs
// and this machine lacks, "" when none:
//
//   - NAME: env — the check's text mentions $NAME or ${NAME (a comment
//     counts: `go test ./store/... # needs $RADIX_DSN` ties a check whose
//     tests read the variable themselves), and NAME is unset or empty;
//   - NAME: cmd — the check runs NAME as a command (parsed like the Bash
//     guards: chains, subshells, wrappers, sh -c, an absolute path), and
//     NAME is not on PATH. A check that needs a program only a script it
//     runs calls names it itself: `docker info >/dev/null && make smoke`.
//   - NAME: {probe, when} (v1.4) — when matches the check's text (or,
//     without when, the text names NAME as a word), and the probe does not
//     exit 0: "NAME is not ready". The probe runs once per ape command
//     (probeOnce), as a check: its own process group, APE_GATE=1, idle 1m
//     and max 5m, its output in logs/probe-<NAME>.log.
func (p *Project) unmetRequirement(ctx context.Context, cmd string, requires Requires) string {
	names := make([]string, 0, len(requires))
	for name := range requires {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		req := requires[name]
		switch req.Kind {
		case RequireProbe:
			bound := containsWord(cmd, name)
			if req.when != nil {
				bound = req.when.MatchString(cmd)
			}
			if bound {
				if why := p.probeOnce(ctx, name, req); why != "" {
					return why
				}
			}
		case RequireEnv:
			if mentionsVar(cmd, name) && os.Getenv(name) == "" {
				return "$" + name + " is unset (requires: " + name + ": env)"
			}
		case RequireCmd:
			if invokes(cmd, name) {
				if _, err := exec.LookPath(name); err != nil {
					return name + " is not on PATH (requires: " + name + ": cmd)"
				}
			}
		}
	}
	return ""
}

// probeResults holds each probe's outcome for the life of this ape command,
// so a probe runs once however many checks it binds: keyed by project
// root, name and probe command.
var probeResults sync.Map

// probeLimits bound a probe: it answers whether something is ready, so it
// is short.
var probeLimits = TierTimeout{Idle: time.Minute, Max: 5 * time.Minute}

// probeOnce is "" when name's probe exits 0, else why the bound checks
// are unrun.
func (p *Project) probeOnce(ctx context.Context, name string, req MachineRequirement) string {
	key := p.Root + "\x00" + name + "\x00" + req.Probe
	if v, ok := probeResults.Load(key); ok {
		return v.(string)
	}
	p.ensureState()
	logPath := filepath.Join(p.LogsDir(), "probe-"+safeLogName(name)+".log")
	r := runCheck(ctx, p.Root, req.Probe, logPath, "", probeLimits, nil)
	why := ""
	if r.Exit != 0 {
		why = fmt.Sprintf("%s is not ready: its probe `%s` exited %d (requires: %s: probe; output: %s)", name, req.Probe, r.Exit, name, p.Rel(logPath))
	}
	if ctx.Err() == nil {
		probeResults.Store(key, why)
	}
	return why
}

// safeLogName is name with what a file name should not hold replaced.
func safeLogName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ' ' || r < ' ' {
			return '_'
		}
		return r
	}, name)
}

// mentionsVar reports whether s holds $name or ${name not followed by
// another identifier character.
func mentionsVar(s, name string) bool {
	for _, ref := range []string{"$" + name, "${" + name} {
		for off := 0; ; {
			i := strings.Index(s[off:], ref)
			if i < 0 {
				break
			}
			end := off + i + len(ref)
			if end == len(s) || !isIdentByte(s[end]) {
				return true
			}
			off = end
		}
	}
	return false
}

// invokes reports whether the command line runs name as a command.
func invokes(cmd, name string) bool {
	return scanCmds(cmd, "", 0, func(words []string, _ string) string {
		if filepath.Base(words[0]) == name {
			return name
		}
		return ""
	}) != ""
}

// Outcome describes a check's result in a few words: "exit 1", or the
// vacuous/unrun reason with its detail.
func (r *CheckResult) Outcome() string {
	if r.Reason != "" {
		return r.Reason + ": " + r.Detail
	}
	return "exit " + strconv.Itoa(r.Exit)
}
