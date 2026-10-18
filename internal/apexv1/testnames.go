package apexv1

import (
	"regexp"
	"strings"
)

// The failing tests a red run names, read from its output: what tells a
// red that was already there from one the work under judgement caused,
// when both runs are red. The lift compares its two regression runs by
// them (R4), and a change's verify and a milestone's gate compare the
// regression command against its baseline by them (Q47).
var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	// failNameRes each capture a failing test (or package, or suite)
	// name: go test's "--- FAIL: TestX/sub" and "FAIL pkg [build
	// failed]", gotestsum's "=== FAIL: pkg TestX (0.00s)" (package and
	// test, the duration dropped) and "✖  pkg (4m19s)" (a package, R11),
	// pytest's "FAILED path::test" and "ERROR path", cargo's "test name
	// ... FAILED", jest's "✕ name" and "FAIL path", and flutter's and
	// dart's "00:01 +121 ~2 -1: path_test.dart: name [E]" (the counters
	// dropped: they change with every test that ran, R18).
	failNameRes = []*regexp.Regexp{
		goTestFailRe,
		gotestsumFailRe,
		regexp.MustCompile(`^\s*✖\s+(\S+(?:\s+\[[a-z ]+ failed\])?)(?:\s+\([^()]*\))?\s*$`),
		regexp.MustCompile(`^FAIL\s+(\S+(?:\s+\[[a-z ]+ failed\])?)`),
		regexp.MustCompile(`^(?:FAILED|ERROR) (\S+)`),
		regexp.MustCompile(`^test (\S+) \.{3} FAILED\s*$`),
		regexp.MustCompile(`^\s*[✕×] (.+?)(?:\s+\(\d+(?:\.\d+)?\s*m?s\))?\s*$`),
		regexp.MustCompile(`^\d+:\d+ \+\d+(?: ~\d+)? -\d+: (.+) \[E\]$`),
	}
	goTestFailRe    = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	gotestsumFailRe = regexp.MustCompile(`^=== FAIL: (\S+ \S+)`)
)

// failingTests are the failing test names a red run's output reports, in
// order, once each; nil when no runner's report could be read. An output
// with gotestsum's "=== FAIL:" lines names each test there with its
// package, so a "--- FAIL:" line it may also carry is the same test again
// and is not read.
func failingTests(out string) []string {
	out = ansiRe.ReplaceAllString(out, "")
	gotestsum := false
	for l := range strings.SplitSeq(out, "\n") {
		if gotestsumFailRe.MatchString(l) {
			gotestsum = true
			break
		}
	}
	seen := map[string]bool{}
	var names []string
	for l := range strings.SplitSeq(out, "\n") {
		l = strings.TrimRight(l, "\r")
		for _, re := range failNameRes {
			if gotestsum && re == goTestFailRe {
				continue
			}
			if m := re.FindStringSubmatch(l); m != nil {
				if n := strings.TrimSpace(m[1]); n != "" && !seen[n] {
					seen[n] = true
					names = append(names, n)
				}
				break
			}
		}
	}
	return names
}

// namesNotIn are the names of now that before does not hold, in now's
// order.
func namesNotIn(now, before []string) []string {
	had := make(map[string]bool, len(before))
	for _, n := range before {
		had[n] = true
	}
	var fresh []string
	for _, n := range now {
		if !had[n] {
			fresh = append(fresh, n)
		}
	}
	return fresh
}
