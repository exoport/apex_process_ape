package apexv1

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/betterleaks/betterleaks/v2/config"
	"github.com/betterleaks/betterleaks/v2/fingerprint"
	"github.com/betterleaks/betterleaks/v2/report"
	"github.com/betterleaks/betterleaks/v2/scan"
	"github.com/betterleaks/betterleaks/v2/sources"
	"github.com/betterleaks/betterleaks/v2/sources/prefilter"
	"gopkg.in/yaml.v3"

	bregexp "github.com/betterleaks/betterleaks/v2/regexp"
)

// D155 (rc.6, rc6-g): ape scans every commit it makes for secrets with the
// betterleaks SDK, and refuses one that adds a secret (commit.secret,
// person). What is scanned is the ADDED lines of exactly what the commit
// records — `git diff --cached -U0 HEAD -- <its pathspec>` — so a
// person's unrelated staged file, a deleted line and unchanged context are
// not; a pre-existing secret in a file ape merely edits does not block it
// (gitleaks `protect` semantics). A commit a model makes in its own
// session is not ape's: the archive squash re-commits the whole change, so
// a change's secret is caught there at the latest.
//
//	secrets:
//	  allow:   # guarded (config.changed): a person's call
//	    - {fingerprint: "sha256:<64 hex>", reason: "a test fixture's fake key"}
//
// The rules are betterleaks' defaults on Go's stdlib regexp engine; no
// provider expression runs, so the scan makes no network request. The
// inline `betterleaks:allow` / `gitleaks:allow` markers are disabled — the
// line's author, a model, could write one — and `.betterleaksignore` is
// not read: it is an unguarded file. A fingerprint is the sha256 of the
// secret's exact bytes, betterleaks' own committed-ignore design. The
// allow list is read from the guard's baseline (R23, rc.6c): the newest
// `apex: config accepted` commit since the v1 epoch on the commit's
// parent, else the epoch itself — the rev config.changed compares
// against — so an entry counts once a person has committed AND accepted
// it, never from an uncommitted edit nor from one a session committed
// itself. Before the epoch (no v1 rule applies yet: the lift commit, a
// project ape has not made v1) it is read from the parent's config.

func init() {
	findingRules = append(findingRules, rule("", `^rule [^ ]+: .* — (replace it with a placeholder|its allow entry is committed but not accepted)`, CodeCommitSecret))
}

// SecretsConfig is _apex/config.yaml's secrets block.
type SecretsConfig struct {
	Allow []SecretAllow `yaml:"allow"`
}

// SecretAllow lets one secret value through the commit scan.
type SecretAllow struct {
	// Fingerprint is "sha256:" and the 64 hex digits of the value's
	// sha256, as the refusal prints it.
	Fingerprint string `yaml:"fingerprint"`
	// Reason says why it is not a secret; required.
	Reason string `yaml:"reason"`
}

var fingerprintRe = regexp.MustCompile(`^sha256:[0-9a-fA-F]{64}$`)

// UnmarshalYAML validates the block as it parses.
func (s *SecretsConfig) UnmarshalYAML(n *yaml.Node) error {
	if err := checkKeys(n, "secrets", "allow"); err != nil {
		return err
	}
	for i := 0; n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != "allow" {
			continue
		}
		list := n.Content[i+1]
		if list.Kind != yaml.SequenceNode && (list.Kind != yaml.ScalarNode || list.Tag != "!!null") {
			return errors.New("secrets.allow: is not a list of {fingerprint, reason}")
		}
		for j, e := range list.Content {
			if err := checkKeys(e, fmt.Sprintf("secrets.allow[%d]", j), "fingerprint", "reason"); err != nil {
				return err
			}
		}
	}
	type plain SecretsConfig
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, a := range p.Allow {
		a.Fingerprint, a.Reason = strings.ToLower(strings.TrimSpace(a.Fingerprint)), strings.TrimSpace(a.Reason)
		switch {
		case !fingerprintRe.MatchString(a.Fingerprint):
			return fmt.Errorf("secrets.allow[%d].fingerprint: %q is not sha256: and 64 hex digits — copy it from the commit.secret refusal", i, a.Fingerprint)
		case a.Reason == "":
			return fmt.Errorf("secrets.allow[%d].reason: say why %s is not a secret", i, a.Fingerprint)
		case seen[a.Fingerprint]:
			return fmt.Errorf("secrets.allow[%d]: %s is listed twice", i, a.Fingerprint)
		}
		seen[a.Fingerprint] = true
		p.Allow[i] = a
	}
	*s = SecretsConfig(p)
	return nil
}

// ScanCommit is the scan every commit ape makes in a git index goes
// through, run just before the commit: the added lines of the index
// against HEAD (the empty tree in a repository with no commits), narrowed
// to paths (files or folders, relative to root) when the commit takes a
// pathspec — exactly what the commit records. A hit is a *FailError
// commit.secret, and the caller commits nothing. subject names the commit
// in the refusal.
func ScanCommit(ctx context.Context, root, subject string, paths ...string) error {
	from := "HEAD"
	if HeadSHA(ctx, root) == "" {
		empty, err := gitWith(ctx, root, nil, []byte{}, "hash-object", "-t", "tree", "--stdin")
		if err != nil {
			return &FailError{Code: CodeCommitFailed, Msg: "scanning " + subject + " for secrets: " + err.Error() + " — nothing was committed; fix git and run it again"}
		}
		from = empty
	}
	return scanDiff(ctx, root, from, subject, []string{"--cached", from}, paths)
}

// scanTrees is ScanCommit for a commit built in a temporary index
// (commitDetached): the tree `to` against the commit `from` it goes on.
func scanTrees(ctx context.Context, root, from, to, subject string, paths []string) error {
	return scanDiff(ctx, root, from, subject, []string{from, to}, paths)
}

// maxPathspecArgs bounds the pathspec git diff gets on its command line
// (Windows caps one at 32K characters, and git diff has no
// --pathspec-from-file): a longer one — the lift's — is applied to the
// whole diff here instead.
const maxPathspecArgs = 8 << 10

// scanDiff scans the added lines of `git diff <sides> -- <paths>`, with
// the allow list of the config at rev's guard baseline (allowBaseline).
func scanDiff(ctx context.Context, root, rev, subject string, sides, paths []string) error {
	// Fixed output whatever the user's git config: no colour, prefixes,
	// external diff or textconv; renames paired, so a moved file adds no
	// lines.
	args := append([]string{
		"-c", "core.quotepath=off", "diff", "-U0", "--no-color", "--no-ext-diff", "--no-textconv",
		"-M", "--no-relative", "--src-prefix=a/", "--dst-prefix=b/",
	}, sides...)
	filter := len(strings.Join(paths, " ")) > maxPathspecArgs
	if len(paths) > 0 && !filter {
		args = append(append(args, "--"), paths...)
	}
	out, err := gitRaw(ctx, root, args...)
	if err != nil {
		return &FailError{Code: CodeCommitFailed, Msg: "scanning " + subject + " for secrets: " + err.Error() + " — nothing was committed; fix git and run it again"}
	}
	hunks := parseAddedLines(out)
	if filter {
		prefix, _ := git(ctx, root, "rev-parse", "--show-prefix")
		hunks = slices.DeleteFunc(hunks, func(h addedHunk) bool { return !underAny(h.path, prefix, paths) })
	}
	if len(hunks) == 0 {
		return nil
	}
	base := allowBaseline(ctx, root, rev)
	allow, err := secretsAllowAt(ctx, root, base)
	if err != nil {
		return &FailError{Code: CodeCommitFailed, Msg: "scanning " + subject + " for secrets: " + configYAML + " at " + base + ": " + err.Error() + " — nothing was committed; fix the secrets block"}
	}
	found, err := scanAdded(ctx, hunks, allow)
	if err != nil {
		return &FailError{Code: CodeCommitFailed, Msg: "scanning " + subject + " for secrets failed: " + err.Error() + " — nothing was committed; run it again"}
	}
	if len(found) == 0 {
		return nil
	}
	return secretRefusal(subject, found, unacceptedAllow(ctx, root, rev, base, allow))
}

// allowBaseline is the rev secrets.allow is read at for a commit on rev
// (R23): the guard's baseline — the newest `apex: config accepted`
// commit since the v1 epoch, else the epoch — so a committed entry counts
// only once a person accepted it; rev itself when rev has no v1 epoch.
func allowBaseline(ctx context.Context, root, rev string) string {
	p := &Project{Root: root}
	epoch := p.v1Epoch(ctx, rev)
	if epoch == "" {
		return rev
	}
	if accepted := p.lastConfigAcceptanceOn(ctx, epoch, rev); accepted != "" {
		return accepted
	}
	return epoch
}

// unacceptedAllow are the fingerprints rev's config allows and the
// baseline's does not: committed, not accepted.
func unacceptedAllow(ctx context.Context, root, rev, base string, allow []SecretAllow) map[string]bool {
	if base == rev {
		return nil
	}
	cur, err := secretsAllowAt(ctx, root, rev)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, a := range cur {
		if !slices.ContainsFunc(allow, func(b SecretAllow) bool { return b.Fingerprint == a.Fingerprint }) {
			out[a.Fingerprint] = true
		}
	}
	return out
}

// underAny reports whether a repository path is one of paths (relative to
// the project at prefix) or inside one.
func underAny(path, prefix string, paths []string) bool {
	for _, p := range paths {
		p = strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/")
		if p == "" || p == "." {
			if strings.HasPrefix(path, prefix) {
				return true
			}
			continue
		}
		if p = prefix + p; path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// ErrorCode is an apexv1 error's diagnostic code, for a caller outside v1's
// exit table that prints it beside the message: "error.unclassified" for
// any other error.
func ErrorCode(err error) string {
	return Diagnostics(err)[0].Code
}

// commitFailed is a record commit's failure: a commit.secret refusal as it
// is, anything else CodeCommitFailed with msg.
func commitFailed(err error, msg string) error {
	if fe, ok := errors.AsType[*FailError](err); ok && fe.Code == CodeCommitSecret {
		return fe
	}
	return &FailError{Code: CodeCommitFailed, Msg: msg}
}

// secretsAllowAt is the allow list of the config committed at rev; none
// when it has no config.
func secretsAllowAt(ctx context.Context, root, rev string) ([]SecretAllow, error) {
	data, err := gitRaw(ctx, root, "show", rev+":./"+configYAML)
	if err != nil {
		return nil, nil //nolint:nilerr // no config at rev: nothing allowed
	}
	var c struct {
		Secrets SecretsConfig `yaml:"secrets"`
	}
	if err := yaml.Unmarshal([]byte(data), &c); err != nil {
		return nil, err
	}
	return c.Secrets.Allow, nil
}

// addedHunk is a run of consecutive added lines of one file, starting at
// line start of the new version.
type addedHunk struct {
	path  string
	start int
	text  string
}

var bingoSumRe = regexp.MustCompile(`(?:^|/)\.bingo/[^/]+\.sum$`)

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// parseAddedLines reads `git diff -U0` output into the added runs, one per
// hunk. A deleted file, a binary file and a pure deletion add none.
func parseAddedLines(diff string) []addedHunk {
	var out []addedHunk
	path := ""
	var cur *addedHunk
	var b strings.Builder
	flush := func() {
		if cur != nil && b.Len() > 0 {
			cur.text = b.String()
			out = append(out, *cur)
		}
		cur = nil
		b.Reset()
	}
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			path = ""
		case cur == nil && strings.HasPrefix(line, "+++ "):
			path = diffPath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@ "):
			flush()
			m := hunkRe.FindStringSubmatch(line)
			if m == nil || path == "" {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			cur = &addedHunk{path: path, start: start}
		case cur != nil && strings.HasPrefix(line, "+"):
			b.WriteString(line[1:])
			b.WriteByte('\n')
		}
	}
	flush()
	return out
}

// diffPath is the path of a `+++` line: "" for /dev/null.
func diffPath(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(s, "b/")
}

// secretRules is betterleaks' default rule set, parsed once.
var secretRules = sync.OnceValues(func() (*config.Config, error) { return config.Default() })

// secretFinding is one secret in the added lines.
type secretFinding struct {
	Path, Rule, Redacted, Fingerprint string
	Line                              int
}

// secretScanner is the scanner for one allow list, kept for the process: a
// command that commits several times builds it once (about 60 ms).
type secretScanner struct {
	scanner *scan.Scanner
	skip    sources.PrefilterFunc
}

var (
	secretScannersMu sync.Mutex
	secretScanners   = map[string]*secretScanner{}
)

func scannerFor(allow []SecretAllow) (*secretScanner, error) {
	fps := make([]string, 0, len(allow))
	for _, a := range allow {
		fps = append(fps, a.Fingerprint)
	}
	sort.Strings(fps)
	key := strings.Join(fps, ",")
	secretScannersMu.Lock()
	defer secretScannersMu.Unlock()
	if s := secretScanners[key]; s != nil {
		return s, nil
	}
	cfg, err := secretRules()
	if err != nil {
		return nil, err
	}
	engine := bregexp.Stdlib{}
	ignored := make([]fingerprint.Hash, 0, len(fps))
	for _, fp := range fps {
		h, err := fingerprint.Parse(strings.TrimPrefix(fp, "sha256:"))
		if err != nil {
			return nil, err
		}
		ignored = append(ignored, h)
	}
	// No allow signatures: the inline markers suppress nothing.
	scanner, err := scan.New(cfg, scan.WithRegexEngine(engine), scan.WithAllowSignatures(), scan.WithIgnoredFingerprints(ignored...))
	if err != nil {
		return nil, err
	}
	// The config's path prefilter (lock files, vendored code, images, …),
	// which a source applies, not the scanner; plus a bingo pin's .sum,
	// go.sum's format under another name, whose h1: hashes the defaults
	// skip only as go.sum.
	cfgSkip, err := prefilter.Compile(cfg.PrefilterExpr, prefilter.Options{RegexEngine: engine})
	if err != nil {
		return nil, err
	}
	skip := func(attrs map[string]string) bool {
		return bingoSumRe.MatchString(attrs[sources.AttrPath]) || (cfgSkip != nil && cfgSkip(attrs))
	}
	s := &secretScanner{scanner: scanner, skip: skip}
	secretScanners[key] = s
	return s, nil
}

// scanAdded runs the scanner over the added runs.
func scanAdded(ctx context.Context, hunks []addedHunk, allow []SecretAllow) ([]secretFinding, error) {
	sc, err := scannerFor(allow)
	if err != nil {
		return nil, err
	}
	scanner, skip := sc.scanner, sc.skip
	seen := map[secretFinding]bool{}
	var found []secretFinding
	_, err = scanner.Scan(ctx, addedSource{hunks: hunks, skip: skip}, func(f report.Finding) error {
		// A path-only rule (a .p12 file's name) carries no value to
		// fingerprint, so nothing could allow it: it is not a secret in
		// the added lines.
		if f.Match.Value == "" {
			return nil
		}
		s := secretFinding{
			Path: f.Location.Path, Line: f.Location.StartLine, Rule: f.RuleID,
			Redacted: redactSecret(f), Fingerprint: "sha256:" + f.Match.Fingerprint,
		}
		if s.Path == "" {
			s.Path = f.Attributes[sources.AttrPath]
		}
		if !seen[s] {
			seen[s] = true
			found = append(found, s)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Path != found[j].Path {
			return found[i].Path < found[j].Path
		}
		if found[i].Line != found[j].Line {
			return found[i].Line < found[j].Line
		}
		return found[i].Rule < found[j].Rule
	})
	return found, nil
}

// A refusal shows redactShown characters of a secret, and never more
// than 1/redactShare of it.
const redactShown, redactShare = 4, 4

// redactSecret shows about four characters of the value, and never more
// than a quarter of it.
func redactSecret(f report.Finding) string {
	n := len([]rune(f.Match.Value))
	keep := min(redactShown, n/redactShare)
	return f.RedactedCopy(uint(100 - keep*100/n)).Match.Value
}

// addedSource yields each added run through a sources.Reader, its line
// numbers shifted to the run's place in the file.
type addedSource struct {
	hunks []addedHunk
	skip  sources.PrefilterFunc
}

func (s addedSource) Fragments(ctx context.Context, yield sources.FragmentsFunc) error {
	for _, h := range s.hunks {
		r := &sources.Reader{
			Content:    strings.NewReader(h.text),
			Attributes: map[string]string{sources.AttrPath: h.path, sources.AttrResource: sources.ResourceFileContent},
			Prefilter:  s.skip,
		}
		err := r.Fragments(ctx, func(f sources.Fragment, err error) error {
			f.StartLine += h.start - 1
			return yield(f, err)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// secretRefusal is the commit.secret error: each secret as
// `<file>:<line> rule <id>: <redacted>`, the placeholder fix first
// (E-R6b-2: an example never needs a real value), and the allow entries a
// person may paste. A fingerprint in unaccepted is allowed by an entry
// committed but not accepted (R23): its finding says so.
func secretRefusal(subject string, found []secretFinding, unaccepted map[string]bool) error {
	var entries, pending []string
	list := make([]string, 0, len(found))
	findings := make([]Finding, 0, len(found))
	for _, s := range found {
		list = append(list, fmt.Sprintf("%s:%d rule %s: %s", s.Path, s.Line, s.Rule, s.Redacted))
		entry := fmt.Sprintf(`{fingerprint: %q, reason: "…"}`, s.Fingerprint)
		if unaccepted[s.Fingerprint] {
			if !slices.Contains(pending, s.Fingerprint) {
				pending = append(pending, s.Fingerprint)
			}
			findings = append(findings, Finding{File: s.Path, Line: s.Line, Message: "rule " + s.Rule + ": " + s.Redacted +
				" — its allow entry is committed but not accepted: a person runs `ape project accept-config --by <who>`; or replace it with a placeholder (`<token>`, `$TOKEN`)"})
			continue
		}
		if !slices.Contains(entries, entry) {
			entries = append(entries, entry)
		}
		findings = append(findings, Finding{File: s.Path, Line: s.Line, Message: "rule " + s.Rule + ": " + s.Redacted +
			" — replace it with a placeholder (`<token>`, `$TOKEN`) — an example in docs never needs a real value — or move a real secret to hot/ and read it from there;" +
			" if it is not a secret, a person adds " + entry + " to " + configYAML + " secrets.allow, commits it and accepts it (`ape project accept-config --by <who>`)"})
	}
	noun := "a secret"
	if len(found) > 1 {
		noun = strconv.Itoa(len(found)) + " secrets"
	}
	msg := "nothing was committed: " + subject + " adds " + noun + ": " + strings.Join(list, "; ") +
		" — replace each with a placeholder (`<token>`, `$TOKEN`) — an example in docs never needs a real value — or move a real secret to hot/ and read it from there"
	if len(entries) > 0 {
		msg += "; if one is not a secret, a person adds its entry to " + configYAML +
			", commits it and accepts it (`ape project accept-config --by <who>`): secrets: {allow: [" + strings.Join(entries, ", ") + "]}"
	}
	if len(pending) > 0 {
		msg += "; the allow entry for " + strings.Join(pending, ", ") + " is committed but not accepted: a person runs `ape project accept-config --by <who>`"
	}
	return &FailError{Code: CodeCommitSecret, Findings: findings, Msg: msg}
}
