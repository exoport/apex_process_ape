package apexv1

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Caps (D55, D69). Every artefact cap is a default with a hard ceiling,
// in _apex/config.yaml's caps: block, under the config.changed guard:
//
//	caps:
//	  project_md: 16KB     # development/project.md          (default 16 KB, ceiling 48 KB)
//	  agents_md: 16KB      # AGENTS.md                       (default 16 KB, ceiling 48 KB)
//	  change_md: 16KB      # a change's change.md            (default 16 KB, ceiling 48 KB)
//	  design_md: 16KB      # a change's design.md            (default 16 KB, ceiling 48 KB)
//	  spec: 24KB           # one capability's spec.md        (default 24 KB, ceiling 64 KB)
//	  evidence_log: 1MiB   # one check's output in evidence  (default 1 MiB, ceiling 16 MiB)
//
// A size is plain bytes (16384) or a number with a unit: B, KB or KiB
// (1024 bytes: a KB is 1024 here, as everywhere ape states a cap), MB or
// MiB (1024 KB). Bytes, not runes or lines: a cap is about what a model
// has to read, and bytes are what a context window pays for. A ceiling
// keeps an artefact readable in one Read call; a configured value above
// it is caps.over-ceiling — `project check` reports it, and the verbs that
// enforce a cap (change check, start, archive, abandon) refuse until a
// person fixes the config. Measured meanwhile, a cap stops at its ceiling.
//
// change_md measures the plan: change.md less its ## Notes section
// (Change.PlanBytes, D89). The whole file, Notes included, is held to the
// change_md ceiling whatever the cap (change.over-ceiling).

// Cap keys, as caps: names them.
const (
	CapProjectMD   = "project_md"
	CapAgentsMD    = "agents_md"
	CapChangeMD    = "change_md"
	CapDesignMD    = "design_md"
	CapSpec        = "spec"
	CapEvidenceLog = "evidence_log"
)

// CodeCapsOverCeiling: a caps: value above its hard ceiling.
const CodeCapsOverCeiling = "caps.over-ceiling"

// capDef is one cap: its key, what it caps (for messages), its default
// and hard ceiling in bytes, and its field in Caps.
type capDef struct {
	key, what    string
	def, ceiling int
	field        func(*Caps) *int
}

var capDefs = []capDef{
	{CapProjectMD, "project.md", 16 << 10, 48 << 10, func(c *Caps) *int { return &c.ProjectMD }},
	{CapAgentsMD, "AGENTS.md", 16 << 10, 48 << 10, func(c *Caps) *int { return &c.AgentsMD }},
	{CapChangeMD, "change.md", 16 << 10, 48 << 10, func(c *Caps) *int { return &c.ChangeMD }},
	{CapDesignMD, "design.md", 16 << 10, 48 << 10, func(c *Caps) *int { return &c.DesignMD }},
	{CapSpec, "a capability's spec.md", 24 << 10, 64 << 10, func(c *Caps) *int { return &c.Spec }},
	{CapEvidenceLog, "one check's output in its evidence file", 1 << 20, 16 << 20, func(c *Caps) *int { return &c.EvidenceLog }},
}

// Caps is caps:, in bytes: as configured (0 is unset), or, from Limits,
// the caps in force.
type Caps struct {
	ProjectMD   int `json:"project_md"`
	AgentsMD    int `json:"agents_md"`
	ChangeMD    int `json:"change_md"`
	DesignMD    int `json:"design_md"`
	Spec        int `json:"spec"`
	EvidenceLog int `json:"evidence_log"`
}

// UnmarshalYAML parses and validates the block: a key ape does not know,
// or a size it cannot read, makes the config unparseable. A value above
// its ceiling parses (OverCeiling reports it), so `project check` can say
// so rather than refuse to read the project.
func (c *Caps) UnmarshalYAML(n *yaml.Node) error {
	keys := make([]string, 0, len(capDefs))
	for _, d := range capDefs {
		keys = append(keys, d.key)
	}
	if err := checkKeys(n, "caps", keys...); err != nil {
		return err
	}
	var raw map[string]string
	if err := n.Decode(&raw); err != nil {
		return fmt.Errorf("caps: %w", err)
	}
	for _, d := range capDefs {
		v, ok := raw[d.key]
		if !ok {
			continue
		}
		b, err := ParseByteSize(v)
		if err != nil {
			return fmt.Errorf("caps.%s: %w", d.key, err)
		}
		*d.field(c) = b
	}
	return nil
}

var byteSizeRe = regexp.MustCompile(`^(\d+)\s*([A-Za-z]*)$`)

// maxCapBytes is the largest size a cap may name: 1 TiB.
const maxCapBytes = 1 << 40

// ParseByteSize reads a caps: size: plain bytes, or a whole number with
// B, KB, KiB, MB or MiB (KB = KiB = 1024 bytes, MB = MiB = 1024 KB).
func ParseByteSize(s string) (int, error) {
	m := byteSizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("%q is not a size (16KB, 1MiB, or plain bytes)", s)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive size", s)
	}
	unit := 1
	switch strings.ToLower(m[2]) {
	case "", "b":
	case "kb", "kib":
		unit = 1 << 10
	case "mb", "mib":
		unit = 1 << 20
	default:
		return 0, fmt.Errorf("%q: the unit is not B, KB, KiB, MB or MiB", s)
	}
	if n > maxCapBytes/unit {
		return 0, fmt.Errorf("%q is too large", s)
	}
	return n * unit, nil
}

// DefaultCaps are the caps with nothing configured.
func DefaultCaps() Caps { return Caps{}.Limits() }

// Limits are the caps in force: each configured value, else its
// default, and never past its ceiling.
func (c Caps) Limits() Caps {
	out := c
	for _, d := range capDefs {
		f := d.field(&out)
		if *f <= 0 {
			*f = d.def
		} else {
			*f = min(*f, d.ceiling)
		}
	}
	return out
}

// OverCeiling is one finding per configured value above its ceiling, on
// _apex/config.yaml.
func (c Caps) OverCeiling() []Finding {
	var out []Finding
	for _, d := range capDefs {
		if v := *d.field(&c); v > d.ceiling {
			out = append(out, Finding{File: configYAML, Message: "caps." + d.key + ": " + capSize(v) + " is over its " + capSize(d.ceiling) +
				" ceiling (" + d.what + ") — a person sets it at most " + capSize(d.ceiling) + " (default " + capSize(d.def) + ") in _apex/config.yaml"})
		}
	}
	return out
}

// capSize renders a cap the way caps: states it.
func capSize(n int) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.Itoa(n>>20) + " MiB"
	case n%(1<<10) == 0:
		return strconv.Itoa(n>>10) + " KB"
	}
	return strconv.Itoa(n) + " bytes"
}

// capsConfig is the project's caps: block; the zero Caps when the config
// does not parse (Open refused that one already).
func (p *Project) capsConfig() Caps {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return Caps{}
	}
	return cfg.Caps
}

// caps are the caps in force in this project.
func (p *Project) caps() Caps { return p.capsConfig().Limits() }

// requireCaps refuses a verb that enforces a cap while caps: holds a value
// above its ceiling (caps.over-ceiling, a person's).
func (p *Project) requireCaps() error {
	if fs := p.capsConfig().OverCeiling(); len(fs) > 0 {
		return &FailError{Code: CodeCapsOverCeiling, Msg: "_apex/config.yaml caps: holds a value above its hard ceiling — a person fixes caps: in _apex/config.yaml, then run this again", Findings: fs}
	}
	return nil
}

// capCeiling is key's hard ceiling in bytes.
func capCeiling(key string) int {
	for _, d := range capDefs {
		if d.key == key {
			return d.ceiling
		}
	}
	panic("apexv1: no cap " + key)
}

// changeMDCeiling is the whole change.md's ceiling, ## Notes included,
// whatever caps.change_md says (D89): the change_md ceiling.
func changeMDCeiling() int { return capCeiling(CapChangeMD) }

// overCeilingMsg is change.over-ceiling's message, the size given.
func overCeilingMsg(size int) string {
	return "change.md is " + sizeKB(size) + " in all, over the " + capSize(changeMDCeiling()) +
		" file ceiling — ## Notes is for working notes only: move bulk to the change's assets/"
}

// abandonMeasuresChange keeps abandon refusing a change over its cap or
// the file ceiling, as archive does (D69, D89). Q40 proposes dropping it
// (moving a change out of the way should never require editing it):
// that is this line set to false.
const abandonMeasuresChange = true

// requireChangeFits refuses verb (archive, abandon) on a change whose plan
// is over caps.change_md or whose change.md is over the file ceiling: the
// measure of check and start (D69, D89). Nothing here edits the change —
// the plan is frozen at start, so only ## Why can have grown since; the
// remedy is the model's, and archive moves the folder exactly as used.
func (p *Project) requireChangeFits(ci ChangeInfo, verb string) error {
	if err := p.requireCaps(); err != nil {
		return err
	}
	if ci.Doc == nil || (verb == "abandon" && !abandonMeasuresChange) {
		return nil
	}
	if limit, plan := p.caps().ChangeMD, ci.Doc.PlanBytes(); plan > limit {
		return &FailError{Code: "change.too-big", Msg: "change.md of " + ci.ID + " is " + sizeKB(plan) + " without ## Notes, over the " + capSize(limit) + " cap (caps.change_md) — " +
			"the plan is frozen at start, so only ## Why can have grown: move what ## Why gained since start to ## Notes, commit, then " + verb + " again"}
	}
	if size := len(ci.Doc.raw); size > changeMDCeiling() {
		return &FailError{Code: CodeChangeOverCeiling, Msg: "change.md of " + ci.ID + " is " + sizeKB(size) + " in all, over the " + capSize(changeMDCeiling()) +
			" file ceiling — ## Notes is for working notes only: move bulk to the change's assets/, commit, then " + verb + " again"}
	}
	return nil
}
