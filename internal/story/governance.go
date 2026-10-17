package story

// The two governance gates, both on the story-side verifier.
//
// Neither belongs on `ape registry verify`: that command is declared as
// one skill's work list over registry-index-versus-record drift, and it
// takes no story input at all.
//
// # 9.12b — the adrs_considered recomputation
//
// A story's `governance_pass` declares `adrs_considered` (the tag-match
// candidate count) and `adrs_applicable` (how many of them the digest
// pass judged applicable). Only the FIRST is mechanical, and only the
// first is recomputed here.
//
// `adrs_applicable` is the output of the digest algorithm's step 2,
// which the framework declares to be the producing agent's own inline
// judgement. It is compared as a BOUND and never as an equality: asking
// a deterministic CLI to reproduce a judgement would fire a finding on
// every legitimate keep-or-drop the agent made. So there are exactly two
// findings — `adrs_applicable > adrs_considered`, which is impossible
// rather than debatable, and `adrs_applicable == 0` against a non-zero
// candidate set, which is the self-certifying skip the check exists to
// catch.
//
// # 13.3b — ids resolve at HEAD
//
// An id cited in `governance.adrs` that resolves to no ADR is a gate
// failure. An id that resolves to an ADR whose status is not `accepted`
// is REPORTED AND EXITS 0 — a story may legitimately cite a proposed
// ADR, a reader needs to know, and stopping the run over it would turn a
// note into a halt.

// ADR is the subset of an ADR record these gates read.
type ADR struct {
	ID     string
	Status string
	Type   string
	Tags   []string
	Path   string
}

// Candidate is one member of the recomputed candidate set, with the tag
// that put it there.
type Candidate struct {
	ID  string `json:"id"  yaml:"id"`
	Tag string `json:"tag" yaml:"tag"`
}

// GovernancePass is the story's declared digest-pass record.
type GovernancePass struct {
	// Applicable is `adrs_applicable`, and Declared reports whether the
	// story stated one at all. A story with no governance_pass block is
	// not a finding: the key is new, and every story minted before it
	// would otherwise report one.
	Applicable int
	Declared   bool
}

// GovernanceCorpus is the ADR corpus a `--file` run resolved, and
// whether it found one at all.
type GovernanceCorpus struct {
	ADRs []ADR
	// Resolved reports that a project was found and its ADR directory
	// read. False means the two governance classes SKIPPED — which the
	// verdict reports rather than passing over, because "we could not
	// look" is not "we looked and it was clean".
	Resolved bool
	// Root is the project root that was found, for the report.
	Root string
}
