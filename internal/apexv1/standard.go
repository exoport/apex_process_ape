package apexv1

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// D205: APEX ships one standard ADR-0001, "Record architecture decisions"
// — the decision file, its sections, the proposed → accepted lifecycle and
// change only by superseding — the same in every project. init writes it
// (Q118), the lift writes it in place of the governance standard an
// apex-v0 project carried (Q119), and `ape decision standard` brings a
// project made before rc.7b there (Q120): the framework's migration
// v1.0.0_seq-08 runs it (D210; seq-07 is frozen as rc.7 built it).
//
// D206: ADR-0001 is framework-owned, like the payload. Its text is one
// shipped version of the standard, recorded in decisions.json with that
// version and digest; when a framework release changes the standard, its
// migration entry runs `ape decision standard`, which replaces it in its
// own commit (Q123, D210). A project that wants another rule supersedes
// ADR-0001 with an accepted ADR of its own, and from then on is opted out:
// ape leaves ADR-0001 alone and says so.
//
// The text is the framework's: its payload ships _apex/standards/adr-0001.md,
// and ape embeds the same bytes for a project with no payload installed
// (init may run before `ape framework setup`, D162). `make
// check-framework` fails when the two differ (Q117).

// embeddedStandardADR is the payload's _apex/standards/adr-0001.md byte for
// byte: the text the operator approved (D211), framework payload-rc7c.
//
//go:embed standards/adr-0001.md
var embeddedStandardADR string

const (
	// StandardADRID is the standard's number in every project.
	StandardADRID = "ADR-0001"
	// StandardADRSlug names its file, 0001-<slug>.md.
	StandardADRSlug = "record-architecture-decisions"
	// StandardADRPayload is where an installed framework payload keeps
	// the standard's text, relative to the project root.
	StandardADRPayload = "_apex/standards/adr-0001.md"
	// StandardSourceApe is the source of the standard when ape's own
	// copy is the one written: no payload copy is installed.
	StandardSourceApe = "ape"
	// standardByPrefix starts the by: of every decisions.json record ape
	// writes for the standard: no person accepted it (Q118).
	standardByPrefix = "APEX standard"
)

// StandardBy is the by: of the decisions.json record ape writes for the
// standard, cmd the ape command that wrote it.
func StandardBy(cmd string) string { return standardByPrefix + " (" + cmd + ")" }

// EmbeddedStandardADR is ape's own copy of the standard's text, which
// check-framework compares with the payload's.
func EmbeddedStandardADR() string { return embeddedStandardADR }

// StandardADRFile is the standard's file name in decisions/.
func StandardADRFile() string { return "0001-" + StandardADRSlug + ".md" }

// StandardRecord is decisions.json's record of the shipped standard
// ADR-0001 holds (D206 Q123): the framework version it came from ("" for
// ape's own copy, with no payload installed) and the SHA-256 of its bytes.
type StandardRecord struct {
	FrameworkVersion string `json:"framework_version"`
	SHA256           string `json:"sha256"`
}

// installedStandard is the standard's text for one project.
type installedStandard struct {
	doc string
	// source is StandardADRPayload or StandardSourceApe.
	source string
	// version is the installed framework's version_tag, for a payload copy.
	version string
}

// label names the standard's version in prose: the framework version, or
// ape's own copy.
func (s installedStandard) label() string {
	switch {
	case s.source == StandardSourceApe:
		return "ape's own copy"
	case s.version == "":
		return "the installed payload's copy"
	}
	return s.version
}

// standardADR is the text ape writes as ADR-0001 in the project at root:
// the installed payload's copy when there is one that is a well-formed
// accepted ADR-0001, else ape's own.
func standardADR(root string) installedStandard {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(StandardADRPayload)))
	d := string(data)
	if err != nil || len(ValidateDecision(StandardADRPayload, StandardADRFile(), d)) > 0 || adrStatus(d) != ADRAccepted {
		return installedStandard{doc: embeddedStandardADR, source: StandardSourceApe}
	}
	s := installedStandard{doc: d, source: StandardADRPayload}
	if meta, err := os.ReadFile(filepath.Join(root, frameworkYAML)); err == nil {
		if m := readMeta(string(meta)); m != nil {
			s.version = m.Framework.VersionTag
		}
	}
	return s
}

// isStandardText reports whether doc is the standard std: the same text
// but for its Status: line, which superseding it rewrites.
func isStandardText(doc, std string) bool {
	return adrBodyDigest(doc) == adrBodyDigest(std)
}

func bytesSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// StandardADRResult is what init, the lift or `ape decision standard`
// did with ADR-0001 — or, under --check, what is due.
type StandardADRResult struct {
	// Action: created (ADR-0001 was absent: the standard is written),
	// replaced (an earlier standard, or the apex-v0 governance standard,
	// replaced by the installed one), kept (it is the installed standard
	// already), opted-out (an accepted ADR supersedes it: left alone),
	// differs (the project's own decision numbered 0001: left alone).
	Action string `json:"action"`
	Path   string `json:"path"`
	// Source is where the standard's text comes from: the payload's
	// _apex/standards/adr-0001.md, or "ape" (its own copy).
	Source           string `json:"source"`
	FrameworkVersion string `json:"framework_version,omitempty"`
	// Replaces is what the standard replaced: the apex-v0 record the
	// lift did not convert ("ADR-NNNN, <its path>"), or the ADR-0001 file
	// replaced.
	Replaces string `json:"replaces,omitempty"`
	// OptedOutBy are the accepted ADRs that supersede ADR-0001.
	OptedOutBy []string `json:"opted_out_by,omitempty"`
	// Removed are the proposed ADRs `ape decision standard` removes with
	// the apex-v0 governance standard it replaces.
	Removed []RemovedADR `json:"removed_adrs,omitempty"`
	Commit  string       `json:"commit,omitempty"`
}

// The standard ADR's actions.
const (
	StandardCreated  = "created"
	StandardReplaced = "replaced"
	StandardKept     = "kept"
	StandardOptedOut = "opted-out"
	StandardDiffers  = "differs"
)

// Due reports whether the result has a write left: created or replaced.
func (r *StandardADRResult) Due() bool {
	return r != nil && (r.Action == StandardCreated || r.Action == StandardReplaced)
}

// adr0001Files are the files of ADR-0001 in decisions/.
func (p *Project) adr0001Files() []string {
	matches, _ := filepath.Glob(filepath.Join(p.DecisionsDir(), "0001-*.md"))
	return matches
}

// standardPlan is what the standard asks of a project now, writing
// nothing.
type standardPlan struct {
	StandardADRResult
	std installedStandard
	// old is ADR-0001's file as it is ("" when absent), dst the file the
	// standard is written to.
	old, dst string
	doc      string
	// proposed are the proposed ADRs that supersede ADR-0001:
	// planRemovals removes them with the apex-v0 governance standard
	// (Q120).
	proposed []adrRef
}

// planStandardADR decides, in order: more than one ADR-0001 file, or one
// that is neither the standard nor framework-owned, is the project's own
// (differs) — ape never replaces a project's decision; an accepted ADR
// that supersedes ADR-0001 opts the project out; an absent ADR-0001 is
// created; the installed standard is kept; a framework-owned one that
// differs (an earlier standard, the apex-v0 governance standard) is
// replaced.
func (p *Project) planStandardADR() *standardPlan {
	pl := &standardPlan{std: standardADR(p.Root)}
	pl.Source, pl.FrameworkVersion = pl.std.source, pl.std.version
	pl.dst = filepath.Join(p.DecisionsDir(), StandardADRFile())
	files := p.adr0001Files()
	if len(files) > 0 {
		pl.old = files[0]
		pl.doc = readFileOrEmpty(files[0])
		pl.Path = p.Rel(files[0])
	} else {
		pl.Path = p.Rel(pl.dst)
	}
	standard := pl.old != "" && isStandardText(pl.doc, pl.std.doc)
	if len(files) > 1 || pl.old != "" && !standard && !p.frameworkOwnedADR0001(pl.doc) {
		pl.Action = StandardDiffers
		return pl
	}
	for _, s := range p.supersedersOfStandard() {
		switch s.status {
		case ADRAccepted:
			pl.OptedOutBy = append(pl.OptedOutBy, s.id)
		case ADRProposed:
			pl.proposed = append(pl.proposed, s)
		}
	}
	switch {
	case len(pl.OptedOutBy) > 0:
		pl.Action = StandardOptedOut
	case pl.old == "":
		pl.Action = StandardCreated
	case standard:
		pl.Action = StandardKept
	default:
		pl.Action = StandardReplaced
		pl.Replaces = pl.Path
		pl.Path = p.Rel(pl.dst)
	}
	return pl
}

// applyStandardADR writes the plan's standard (created or replaced): the
// old ADR-0001 file removed when its name is not the standard's, the
// standard written and recorded in decisions.json by StandardBy(cmd). It
// returns the paths it changed, project-relative, for the caller's
// commit; nothing when nothing is due.
func (p *Project) applyStandardADR(pl *standardPlan, cmd string) ([]string, error) {
	if !pl.Due() {
		return nil, nil
	}
	var paths []string
	if pl.old != "" && pl.old != pl.dst {
		if err := os.Remove(pl.old); err != nil {
			return nil, err
		}
		paths = append(paths, p.Rel(pl.old))
	}
	if err := p.writeStandardADR(pl.dst, pl.std, cmd); err != nil {
		return nil, err
	}
	return append(paths, p.Rel(pl.dst), p.Rel(p.decisionsJSON()), p.Rel(p.StateGitignore())), nil
}

// writeStandardADR writes the standard as ADR-0001 at path and records it
// in decisions.json. It commits nothing.
func (p *Project) writeStandardADR(path string, std installedStandard, cmd string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(path, std.doc); err != nil {
		return err
	}
	return p.recordStandardADR(std, cmd)
}

// recordStandardADR records the standard ADR-0001 in decisions.json:
// accepted by StandardBy(cmd), with its body digest (what project check
// holds an accepted ADR to) and, as standard, the shipped version and the
// digest of its bytes (D206 Q123).
func (p *Project) recordStandardADR(std installedStandard, cmd string) error {
	recs := p.loadDecisionRecords()
	recs[StandardADRID] = DecisionRecord{
		AcceptedAt: Stamp(), By: StandardBy(cmd), BodySHA256: adrBodyDigest(std.doc),
		Standard: &StandardRecord{FrameworkVersion: std.version, SHA256: bytesSHA256(std.doc)},
	}
	return p.saveDecisionRecords(recs)
}

// initStandardADR is init's part of D205 (Q118): with no ADR-0001, the
// standard is written as <dev>/decisions/0001-record-architecture-decisions.md,
// accepted, and recorded in decisions.json by "APEX standard (ape project
// init)" — no person act: it is the framework's own record. An ADR-0001
// that is the standard is kept (recorded, when decisions.json has no
// record of it); any other is left as it is and reported: init never
// replaces an ADR (`ape decision standard` does, and project check's
// notices say which applies).
func (p *Project) initStandardADR(res *InitResult) error {
	pl := p.planStandardADR()
	res.StandardADR = &pl.StandardADRResult
	recs := p.decisionsJSON()
	recsExisted := fileExists(recs)
	switch pl.Action {
	case StandardCreated:
		if err := p.writeStandardADR(pl.dst, pl.std, "ape project init"); err != nil {
			return err
		}
		res.Created = append(res.Created, pl.Path)
	case StandardKept:
		if _, ok := p.loadDecisionRecords()[StandardADRID]; ok {
			return nil
		}
		if err := p.recordStandardADR(installedStandard{doc: pl.doc, source: pl.std.source, version: pl.std.version}, "ape project init"); err != nil {
			return err
		}
	default:
		// replaced and differs are left alone: Action says differs.
		res.StandardADR.Action, res.StandardADR.Replaces = StandardDiffers, ""
		if pl.Action == StandardReplaced {
			res.StandardADR.Path = pl.Replaces
		}
		return nil
	}
	if recsExisted {
		res.Amended = append(res.Amended, p.Rel(recs))
	} else {
		res.Created = append(res.Created, p.Rel(recs))
	}
	return nil
}

func readFileOrEmpty(path string) string {
	s, _ := readFileString(path)
	return s
}

// liftedV0StandardRe is the Lifted from: line of an ADR an earlier lift
// converted from the apex-v0 governance standard: the record whose canon
// source is governance/adrs/adr-NNNN_use-markdown-adrs.md ("Use Markdown
// Architectural Decision Records"), a project's copy or the canon one.
var liftedV0StandardRe = regexp.MustCompile(`(?m)^Lifted from: ADR-\d+, \S*adr-\d+_use-markdown-adrs\.md at `)

// v0StandardSourceRe is the canon source of the apex-v0 governance
// standard: its file in the governance repository, which a project's copy
// keeps as its own file name and names in output_document.
var v0StandardSourceRe = regexp.MustCompile(`(^|/)adr-\d+_use-markdown-adrs\.md$`)

// isV0Standard reports whether an apex-v0 record is the governance
// standard, "Use Markdown Architectural Decision Records", by its canon
// source: the canon record itself, or a project's copy of it — whose
// output_document names its path in the governance repository, and whose
// file keeps the canon file name, adr-NNNN_use-markdown-adrs.md. Its
// number is not the test: the governance repository has numbered it
// ADR-0001 and ADR-0002, and a project's own ADR-0001 is often its first
// decision (tenax's is "NATS as the sole substrate").
func isV0Standard(a *v0ADR) bool {
	return v0StandardSourceRe.MatchString(a.rel) || v0StandardSourceRe.MatchString(a.source)
}

// frameworkOwnedADR0001 reports whether an ADR-0001 that is not the
// installed standard is still the framework's to replace (Q120, Q123): the
// apex-v0 governance standard as an earlier lift converted it, or a
// standard ape wrote itself (its decisions.json record is a standard one:
// an earlier text of the standard, the placeholder included). A project's
// own decision numbered 0001 — one `ape decision new` made, or a
// project's first apex-v0 ADR the rc.7 lift numbered 0001 — is not.
func (p *Project) frameworkOwnedADR0001(doc string) bool {
	if liftedV0StandardRe.MatchString(doc) {
		return true
	}
	rec, ok := p.loadDecisionRecords()[StandardADRID]
	return ok && (rec.Standard != nil || strings.HasPrefix(rec.By, standardByPrefix))
}

// standardADRUnchanged reports whether an ADR-0001 that differs from its
// decisions.json body digest is the shipped standard ape recorded: the
// one change decision.accepted-edited accepts (D206 Q123). A hand edit
// matches no recorded digest.
func standardADRUnchanged(id, doc string, rec DecisionRecord) bool {
	return id == StandardADRID && rec.Standard != nil && rec.Standard.SHA256 == bytesSHA256(doc)
}

// rc.7b (rc7b-adr).
const (
	// CodeStandardDiffers: project check's notice for an ADR-0001 that is
	// the project's own decision (Q118, Q120).
	CodeStandardDiffers = "decision.standard-differs"
	// CodeStandardSuperseded: project check's notice for an accepted ADR
	// that supersedes ADR-0001 — the project opted out (Q120, Q123).
	CodeStandardSuperseded = "decision.standard-superseded"
	// CodeStandardStale: project check's notice for an ADR-0001 the
	// framework owns that is not the installed standard (Q123).
	CodeStandardStale = "decision.standard-stale"
	// CodeStandardPending: `ape decision standard --check` while a
	// write of the standard is due (Q123).
	CodeStandardPending = "decision.standard-pending"
)

// standardADRNotices are project check's notices about ADR-0001 (D205,
// D206): the project's own decision numbered 0001, which ape never
// replaces (decision.standard-differs); an accepted ADR that supersedes
// it, so the project opted out (decision.standard-superseded); an
// ADR-0001 the framework owns that is absent or not the installed
// standard, which `ape decision standard` writes
// (decision.standard-stale) — so a framework release that changes the
// standard without a migration entry cannot go unseen.
func (p *Project) standardADRNotices() []Diagnostic {
	pl := p.planStandardADR()
	switch pl.Action {
	case StandardDiffers:
		return []Diagnostic{notice(Diagnostic{Code: CodeStandardDiffers, File: pl.Path, Line: 1, Message: StandardADRID +
			" is the project's own decision, not APEX's standard ADR-0001 (Record architecture decisions), so the project does not record the standard" +
			" — ape never replaces a project's own decision: a person decides whether the project adopts the standard (its text: " + StandardADRPayload + ", or ape's own copy)"})}
	case StandardOptedOut:
		var out []Diagnostic
		for _, id := range pl.OptedOutBy {
			out = append(out, notice(Diagnostic{Code: CodeStandardSuperseded, File: pl.Path, Line: 1, Message: id + " is accepted and supersedes " + StandardADRID +
				", APEX's standard (Record architecture decisions): the project opted out, and framework updates leave ADR-0001 alone" +
				" — a person keeps it, or records a new ADR that supersedes " + id + " to opt back in"}))
		}
		return out
	case StandardCreated, StandardReplaced:
		what := "has no ADR-0001"
		if pl.Action == StandardReplaced {
			what = "has an ADR-0001 (" + pl.Replaces + ") that is not the installed standard (" + pl.std.label() + ")"
		}
		return []Diagnostic{notice(Diagnostic{Code: CodeStandardStale, File: pl.Path, Line: 1, Message: "the project " + what +
			", and no accepted ADR supersedes ADR-0001 — `ape decision standard` writes the standard in its own commit (the framework's migration v1.0.0_seq-08 runs it, and so does the entry of a release that changes the standard)"})}
	}
	return nil
}

// adrRef is one ADR file: its id, project-relative path and status.
type adrRef struct {
	id, rel, status string
}

// supersedersOfStandard are the ADRs other than ADR-0001 whose
// Supersedes: line names ADR-0001, in number order.
func (p *Project) supersedersOfStandard() []adrRef {
	entries, _ := os.ReadDir(p.DecisionsDir())
	var out []adrRef
	for _, e := range entries {
		m := adrFileRe.FindStringSubmatch(e.Name())
		if len(m) < 2 || m[1] == "0001" {
			continue
		}
		path := filepath.Join(p.DecisionsDir(), e.Name())
		doc, err := readFileString(path)
		if err != nil {
			continue
		}
		for _, sm := range adrSupersedesRe.FindAllStringSubmatch(doc, -1) {
			if sm[1] == StandardADRID {
				out = append(out, adrRef{id: "ADR-" + m[1], rel: p.Rel(path), status: adrStatus(doc)})
				break
			}
		}
	}
	return out
}

// standardSubject is the commit `ape decision standard` makes.
func standardSubject(s installedStandard) string {
	return "apex: " + StandardADRID + " standard " + s.label()
}

// DecisionStandardCheck is `ape decision standard --check` (D210): the
// check of the framework's migration entries that run the verb (seq-08,
// and one per release that changes the standard). nil when nothing is due
// — the standard is in place, the project opted out, or ADR-0001 is its
// own decision — else decision.standard-pending (exit 1). It writes
// nothing.
func (p *Project) DecisionStandardCheck(ctx context.Context) (*StandardADRResult, error) {
	pl := p.planStandardADR()
	p.planRemovals(ctx, pl)
	if !pl.Due() {
		return &pl.StandardADRResult, nil
	}
	what := []string{"ADR-0001 is absent"}
	if pl.Action == StandardReplaced {
		what[0] = pl.Replaces + " is not the installed standard (" + pl.std.label() + ")"
	}
	for _, r := range pl.Removed {
		what = append(what, "the proposed "+r.ID+" ("+r.Path+"), which supersedes it, is to remove")
	}
	return &pl.StandardADRResult, &FailError{Code: CodeStandardPending, Msg: "the standard ADR-0001 is due: " + strings.Join(what, ", and ") +
		" — `ape decision standard` writes it in its own commit"}
}

// DecisionStandard is `ape decision standard` (D205 Q120, D206 Q123, D210):
// ADR-0001 brought to the installed standard — created when absent,
// replaced when it is an earlier standard or the apex-v0 governance
// standard an earlier lift converted — and recorded in decisions.json
// with its version and digest, in its own commit "apex: ADR-0001
// standard <framework version>". With the apex-v0 governance standard
// replaced, each proposed ADR that superseded it is removed in the same
// commit (an unaccepted supersede of what is replaced has nothing left to
// supersede; pdfa's ADR-0031), and the lines that still cite it are
// listed, not rewritten. It does nothing when ADR-0001 is the installed
// standard already, when an accepted ADR supersedes it (the project opted
// out), or when it is the project's own decision. Refused outside a git
// repository, on a change branch, and while a file it writes has
// uncommitted changes.
func (p *Project) DecisionStandard(ctx context.Context) (*StandardADRResult, error) {
	pl := p.planStandardADR()
	p.planRemovals(ctx, pl)
	res := &pl.StandardADRResult
	if !pl.Due() {
		return res, nil
	}
	if !p.isWorkTree(ctx) {
		return res, &FailError{Code: "git.not-a-repo", Msg: "not a git repository — the standard ADR-0001 is one commit"}
	}
	if err := p.recoverArchive(ctx); err != nil {
		return res, err
	}
	if err := p.refuseOnChangeBranch(ctx, "writing the standard ADR-0001"); err != nil {
		return res, err
	}
	rels := pl.paths(p)
	if err := p.refuseDirty(ctx, "the standard ADR-0001", rels); err != nil {
		return res, err
	}
	own := p.beforeOwnEdit(ctx, rels...)
	paths, err := p.applyStandardADR(pl, "ape decision standard")
	if err != nil {
		return res, err
	}
	for _, r := range pl.Removed {
		if err := os.Remove(filepath.Join(p.Root, filepath.FromSlash(r.Path))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
		paths = append(paths, r.Path)
	}
	sha, err := p.commitOwnRecord(ctx, own, standardSubject(pl.std), "", paths...)
	if err != nil {
		return res, commitFailed(err, "the standard ADR-0001 was written but its commit failed: "+err.Error())
	}
	res.Commit = sha
	return res, nil
}

// paths are the files writing the plan's standard touches: the old
// ADR-0001, the standard's file and decisions.json, with the state
// whitelist that keeps decisions.json tracked (written when absent), and
// the proposed ADRs it removes.
func (pl *standardPlan) paths(p *Project) []string {
	var out []string
	if pl.old != "" && pl.old != pl.dst {
		out = append(out, p.Rel(pl.old))
	}
	out = append(out, p.Rel(pl.dst), p.Rel(p.decisionsJSON()), p.Rel(p.StateGitignore()))
	for _, r := range pl.Removed {
		out = append(out, r.Path)
	}
	return out
}

// refuseDirty refuses while one of rels has uncommitted changes (tree.dirty).
func (p *Project) refuseDirty(ctx context.Context, what string, rels []string) error {
	dirty, err := gitRaw(ctx, p.Root, append([]string{"status", "--porcelain=v1", "--untracked-files=normal", "--"}, rels...)...)
	if err != nil {
		return &FailError{Code: CodeGitStatusFailed, Msg: "cannot read git status: " + err.Error()}
	}
	if strings.TrimSpace(dirty) != "" {
		return &FailError{Code: "tree.dirty", Msg: what + " is one commit of " + strings.Join(rels, ", ") +
			", and one of them has uncommitted changes — commit or discard them first, then run it again"}
	}
	return nil
}

// RemovedADR is a proposed ADR that superseded the apex-v0 governance
// standard `ape decision standard` replaced, removed with it (Q120), and
// where it is still cited — listed, not rewritten.
type RemovedADR struct {
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Citations []string `json:"citations"`
}

// planRemovals adds to a plan that replaces the apex-v0 governance
// standard the proposed ADRs that supersede it (Q120). Only then: a
// proposed supersede of a standard ape wrote is a person's pending
// opt-out, which an update of the standard leaves alone.
func (p *Project) planRemovals(ctx context.Context, pl *standardPlan) {
	if pl.Action != StandardReplaced || !liftedV0StandardRe.MatchString(pl.doc) {
		return
	}
	for _, s := range pl.proposed {
		pl.Removed = append(pl.Removed, RemovedADR{ID: s.id, Path: s.rel, Citations: p.adrCitations(ctx, s.id, s.rel, pl.Replaces)})
	}
}

// adrCitations are the lines of tracked text files that cite id —
// "<file>:<line>" — but those of the files skip names (the ADR itself,
// and the ADR-0001 replaced with it). None outside a git work tree.
func (p *Project) adrCitations(ctx context.Context, id string, skip ...string) []string {
	out, err := gitRaw(ctx, p.Root, "grep", "-n", "-I", "-w", "-F", "--full-name", "-e", id, "--", ".")
	if err != nil {
		return []string{}
	}
	cites := []string{}
	for l := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		file, rest, ok := strings.Cut(l, ":")
		if !ok || slices.Contains(skip, file) {
			continue
		}
		n, _, _ := strings.Cut(rest, ":")
		cites = append(cites, file+":"+n)
	}
	return cites
}
