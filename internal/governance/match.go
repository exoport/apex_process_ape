// Package governance answers which stories own a path, and what that
// ownership means for a maintenance change.
//
// It is the deterministic half of the lane's first condition: a fix is
// rung 1 only where no IN-FLIGHT story owns the path it touches. The
// skill claims that in its contract; ape re-derives it here over the
// paths the run actually changed, so an owner the skill missed stops the
// commit and a `Carries:` row nobody wrote still appears.
//
// The table it implements is the framework's (apex-maintenance's
// lane-rules.md, § Ownership), and it is implemented verbatim rather
// than from an understanding of it:
//
//	in-progress, review                    veto
//	statuses disagree, or unknown          veto — fail-closed
//	blocked                                Carries:
//	backlog, ready-for-dev                 Carries:
//	done, whatever its slice               Carries: — history
//	cancelled                              Carries:
//	no owner                               nothing
//
// Three rules inside it are the ones a re-implementation drops, so they
// are stated here and tested individually: ownership is a WHOLE PATH
// TOKEN in a `### File List` — a mention under `## Tasks` is advisory
// and is never read; a `(planned)` or `(deferred)` entry records an
// intention and claims nothing; and a story whose file and tracker
// disagree is treated as owned, because a story mid-transition is
// exactly the one a patch must not quietly overwrite.
package governance

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"github.com/exoport/apex_process_ape/internal/mdscan"
	"github.com/exoport/apex_process_ape/internal/sprint"
	"gopkg.in/yaml.v3"
)

// Effect is what an owner's status means for the lane.
const (
	// EffectVeto: the path is owned and this is not lane work.
	EffectVeto = "veto"
	// EffectCarries: the fix may proceed and the commit records that this
	// story's file is no longer the whole truth about the path.
	EffectCarries = "carries"
)

// Owner is one story whose File List claims a path.
//
//nolint:tagliatelle // snake_case is the wire contract, as it is for ape task
type Owner struct {
	Key    string `json:"key"              yaml:"key"`
	Marker string `json:"marker,omitempty" yaml:"marker,omitempty"`
	// Both statuses, because the verdict depends on the pair and a reader
	// must be able to disagree with it.
	FrontmatterStatus string `json:"frontmatter_status,omitempty" yaml:"frontmatter_status,omitempty"`
	TrackerStatus     string `json:"tracker_status,omitempty"     yaml:"tracker_status,omitempty"`
	Effect            string `json:"effect"                       yaml:"effect"`
	// Reason states which row of the table decided, so a verdict can be
	// checked rather than trusted.
	Reason string `json:"reason" yaml:"reason"`
}

// PathResult is one queried path and what owns it.
type PathResult struct {
	Path   string  `json:"path"   yaml:"path"`
	Owners []Owner `json:"owners" yaml:"owners"`
	Veto   bool    `json:"veto"   yaml:"veto"`
	// Carries are the trailer rows this path earns, already rendered.
	Carries []string `json:"carries,omitempty" yaml:"carries,omitempty"`
}

// Report is `ape governance match`.
//
// The OWNER ARM ONLY. There is deliberately no `adrs` or `patterns`
// field, not even empty: an empty list reads as "nothing applies", and
// what is true is that this ape does not answer that question. The
// fields can be added later without changing what these mean.
type Report struct {
	Paths []PathResult `json:"paths" yaml:"paths"`
	// Veto is true when any path is owned by an in-flight story, or by
	// one whose ownership could not be established.
	Veto bool `json:"veto" yaml:"veto"`
	// Carries is every row, deduplicated, in the order a commit takes
	// them.
	Carries  []string `json:"carries,omitempty"  yaml:"carries,omitempty"`
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// sharedOwnerThreshold: above this many FINISHED owners, a path carries
// one `shared` row instead of one row per owner.
//
// The framework's number, and the reason is readability: a path thirty
// finished stories touched says nothing useful thirty times, and a
// commit message is read by a person.
const sharedOwnerThreshold = 5

// notOwning are the File List markers that name a path without claiming
// it. A planned or deferred entry records an intention, not a change,
// and the file need not even exist yet.
var notOwning = map[string]bool{"planned": true, "deferred": true}

// Match answers, for each path, which stories own it and what follows.
func Match(cfg *apexcfg.Resolved, paths []string) (*Report, error) {
	index, warnings, err := loadOwnership(cfg)
	if err != nil {
		return nil, err
	}
	out := &Report{Paths: []PathResult{}, Warnings: warnings}
	seen := map[string]bool{}

	for _, raw := range paths {
		p := filepath.ToSlash(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		res := PathResult{Path: p, Owners: []Owner{}}
		owners := index[p]
		sort.SliceStable(owners, func(i, j int) bool { return owners[i].Key < owners[j].Key })

		finished := 0
		for i := range owners {
			o := decide(owners[i])
			res.Owners = append(res.Owners, o)
			switch {
			case o.Effect == EffectVeto:
				res.Veto = true
				out.Veto = true
			case isFinished(o):
				finished++
			}
		}
		res.Carries = carriesFor(p, res.Owners, finished)
		for _, row := range res.Carries {
			if !seen[row] {
				seen[row] = true
				out.Carries = append(out.Carries, row)
			}
		}
		out.Paths = append(out.Paths, res)
	}
	return out, nil
}

// decide applies the table to one owner.
func decide(o Owner) Owner {
	fm := strings.ToLower(strings.TrimSpace(o.FrontmatterStatus))
	tracker := strings.ToLower(strings.TrimSpace(o.TrackerStatus))

	switch {
	case fm != "" && tracker != "" &&
		sprint.NormalizeStatus(fm) != sprint.NormalizeStatus(tracker):
		// Fail-closed. A story mid-transition is exactly the one a patch
		// must not quietly overwrite, and ape cannot tell which side is
		// ahead.
		o.Effect = EffectVeto
		o.Reason = "the story file and the tracker disagree (" + fm + " vs " + tracker +
			") — treated as owned"
		return o
	case fm == "" && tracker == "":
		o.Effect = EffectVeto
		o.Reason = "no status on either side — ownership could not be established"
		return o
	}

	status := sprint.NormalizeStatus(fm)
	if status == "" {
		status = sprint.NormalizeStatus(tracker)
	}
	switch status {
	case sprint.StatusInProgress, "review":
		o.Effect = EffectVeto
		o.Reason = "an in-flight story owns this path"
	case "blocked":
		o.Effect = EffectCarries
		o.Reason = "a blocked story owns this path"
	case sprint.StatusBacklog, sprint.StatusReadyForDev:
		o.Effect = EffectCarries
		o.Reason = "a not-yet-started story names this path"
	case sprint.StatusDone:
		o.Effect = EffectCarries
		o.Reason = "a finished story's file is no longer the whole truth about this path"
	case sprint.StatusCancelled:
		o.Effect = EffectCarries
		o.Reason = "a cancelled story names this path"
	default:
		// A status outside the vocabulary is not a status ape can read,
		// and the table's own fail-closed row covers it.
		o.Effect = EffectVeto
		o.Reason = "status " + status + " is not one the lane's table knows — treated as owned"
	}
	return o
}

// isFinished reports the two statuses the shared rule counts.
func isFinished(o Owner) bool {
	status := sprint.NormalizeStatus(strings.ToLower(strings.TrimSpace(o.FrontmatterStatus)))
	if status == "" {
		status = sprint.NormalizeStatus(strings.ToLower(strings.TrimSpace(o.TrackerStatus)))
	}
	return status == sprint.StatusDone || status == sprint.StatusCancelled
}

// carriesFor renders the rows, collapsing a widely-shared path.
func carriesFor(path string, owners []Owner, finished int) []string {
	if finished > sharedOwnerThreshold {
		return []string{fmt.Sprintf("shared %s (%d owners)", path, finished)}
	}
	var rows []string
	for i := range owners {
		if owners[i].Effect == EffectCarries {
			rows = append(rows, owners[i].Key+" "+path)
		}
	}
	return rows
}

// loadOwnership reads every story file's File List once, and pairs each
// owner with its tracker row.
func loadOwnership(cfg *apexcfg.Resolved) (index map[string][]Owner, warnings []string, err error) {
	if cfg.Paths.Implementation == "" {
		return nil, nil, fmt.Errorf("%s", apexcfg.MsgImplementationFolderUnset)
	}
	trackerStatus := map[string]string{}
	if t, err := sprint.Load(cfg.Paths.SprintStatus); err == nil && !t.Missing {
		for _, row := range t.Rows {
			trackerStatus[row.Key] = row.Status
		}
	}

	index = map[string][]Owner{}
	err = filepath.WalkDir(cfg.Paths.Implementation, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable subtree is a gap in the answer, not a
			// failure of it: the paths under it simply have no owner
			// recorded, and the caller is told.
			warnings = append(warnings, "could not read "+path+": "+walkErr.Error())
			return nil //nolint:nilerr // reported as a warning; a gap is not a failure
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // a project's own story files, by their own path
		if readErr != nil {
			warnings = append(warnings, "could not read "+path)
			return nil //nolint:nilerr // same: one unreadable story is not a failed answer
		}
		key := sprint.StoryKeyFromPath(path)
		status := frontmatterStatus(data)
		for _, entry := range fileListEntries(string(data)) {
			index[entry.path] = append(index[entry.path], Owner{
				Key:               key,
				Marker:            entry.marker,
				FrontmatterStatus: status,
				TrackerStatus:     trackerStatus[key],
			})
		}
		return nil
	})
	if err != nil {
		return nil, warnings, fmt.Errorf("read story files: %w", err)
	}
	return index, warnings, nil
}

// frontmatterStatus reads `status:` from a story's frontmatter. A file
// without frontmatter is not a story, and contributes no status.
func frontmatterStatus(data []byte) string {
	fm, _, err := frontmatter.Split(data)
	if err != nil {
		return ""
	}
	var head struct {
		Status string `yaml:"status"`
	}
	if err := yaml.Unmarshal(fm, &head); err != nil {
		return ""
	}
	return head.Status
}

// entry is one File List line.
type entry struct {
	path   string
	marker string
}

// fileListEntries reads the `### File List` section, and only that.
//
// A `## Tasks` mention is advisory and never ownership, which is
// enforced by reading one section rather than by filtering afterwards:
// the other sections are never looked at, so there is nothing to filter.
func fileListEntries(text string) []entry {
	section := mdscan.Section(text, "### File List")
	if section == "" {
		return nil
	}
	var out []entry
	for line := range strings.SplitSeq(section, "\n") {
		m := mdscan.FileListEntryRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		path := m[1]
		if path == "" {
			path = m[2] // an entry that forgot its backticks is still an entry
		}
		marker := ""
		if mk := mdscan.MarkerRe.FindStringSubmatch(strings.TrimSpace(m[3])); mk != nil {
			marker = mk[1]
		}
		if notOwning[marker] {
			continue
		}
		out = append(out, entry{path: filepath.ToSlash(strings.TrimSpace(path)), marker: marker})
	}
	return out
}
