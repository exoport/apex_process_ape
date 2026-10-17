package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Human review (H4, D45). Only a person passes --allow-pending-human; an
// archived change's unaccepted human-review tasks stay visible in `ape
// status` (awaiting_human) and stay acceptable: `ape change accept` on an
// archived change updates its archived state.json on the base branch. An
// acceptance may carry the evidence it rests on.

// Evidence is what a person's acceptance rests on: a file in the
// repository, recorded by its path and SHA-256 at acceptance, or a URL,
// recorded as given.
type Evidence struct {
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	URL    string `json:"url,omitempty"`
}

var urlRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://\S+$`)

// resolveEvidence turns each --evidence value into Evidence: a URL as
// given; else a path — relative to the project root, or absolute — to a
// regular file inside the repository, recorded relative to the project
// root with its content's SHA-256.
func (p *Project) resolveEvidence(ctx context.Context, items []string) ([]Evidence, error) {
	var out []Evidence
	top := p.topLevel(ctx)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if urlRe.MatchString(item) {
			out = append(out, Evidence{URL: item})
			continue
		}
		path := item
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.Root, filepath.FromSlash(item))
		}
		path = filepath.Clean(path)
		inRepo := false
		if rel, err := filepath.Rel(top, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			inRepo = true
		}
		st, err := os.Stat(path)
		if !inRepo || err != nil || !st.Mode().IsRegular() {
			return nil, &UsageError{Code: "usage.evidence", Msg: "--evidence " + item + " is neither a URL (scheme://…) nor a file in the repository" +
				" — give a path relative to the project root, or a URL"}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		out = append(out, Evidence{Path: p.Rel(path), SHA256: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

// acceptance is one acceptance to record: a person's (Accept), or a
// delegate's on its pass (AcceptDelegated, D75), which also names its run,
// brings its evidence resolved already, and has its notes committed with
// the record.
type acceptance struct {
	task, by string
	// items are --evidence values, resolved where the record lands.
	items []string
	// run is a delegate's run id; ev its evidence, used instead of items.
	run string
	ev  []Evidence
	// notes are project-relative files (a delegate's accept-* notes)
	// committed with the record.
	notes []string
}

// evidence resolves a's evidence: a delegate's as given, else items
// through resolve.
func (a acceptance) evidence(resolve func([]string) ([]Evidence, error)) ([]Evidence, error) {
	if a.run != "" {
		return a.ev, nil
	}
	return resolve(a.items)
}

// record is the Accept a records, with its evidence.
func (a acceptance) record(ev []Evidence) Accept {
	return Accept{Task: a.task, By: a.by, At: Stamp(), Evidence: ev, Run: a.run}
}

// trailers is the record commit's trailer block: a delegate's
// Accepted-By line (the archive squash carries it too), then Change.
func (a acceptance) trailers(id string) string {
	if a.run == "" {
		return "Change: " + id
	}
	return acceptedByTrailer(Accept{Task: a.task, By: a.by, Run: a.run}) + "\nChange: " + id
}

// withAcceptance records rec in st, replacing an earlier acceptance of its
// task, and refreshes the last verify's pending list.
func withAcceptance(st *State, c *Change, rec Accept) {
	accepted := make([]Accept, 0, len(st.Accepted)+1)
	for _, a := range st.Accepted {
		if a.Task != rec.Task {
			accepted = append(accepted, a)
		}
	}
	accepted = append(accepted, rec)
	st.Accepted = accepted
	refreshPending(st, c)
}

// refreshPending recomputes the last verify's pending list from st's
// acceptances — on a copy of the verify, which st may share.
func refreshPending(st *State, c *Change) {
	if st.Verify == nil {
		return
	}
	v := *st.Verify
	v.PendingHuman = st.PendingHuman(c.HumanTasks())
	if v.PendingHuman == nil {
		v.PendingHuman = []string{}
	}
	st.Verify = &v
}

// humanTask is the change's task, which must be a human-review one.
func humanTask(c *Change, id, task string) error {
	t := c.Task(task)
	if t == nil {
		return &UsageError{Code: "usage.no-such-task", Msg: "change " + id + " has no task " + task}
	}
	if !t.Human() {
		return &FailError{Code: "accept.not-human-review", Msg: "task " + task + " is proven by `" + t.Check + "`, not by a person — `ape change verify " + id + "` runs it"}
	}
	return nil
}

// acceptArchived records an acceptance in an archived change's state.json
// on the base branch it was archived into — wherever HEAD is
// (editOnBranch): the archive folder lives there, and the change's own
// branch is gone.
func (p *Project) acceptArchived(ctx context.Context, id string, a acceptance, ev []Evidence) (*State, error) {
	task := a.task
	// The base is the branch the change was archived into, as its state
	// records it; else, when this tree does not hold the archive, the base
	// of the branch HEAD is on.
	base := ""
	if folder, ok := p.ArchivedIDs()[id]; ok {
		if st, err := LoadState(filepath.Join(p.ArchiveDir(), folder, "state.json")); err == nil && st.BaseBranch != "" && p.branchExists(ctx, st.BaseBranch) {
			base = st.BaseBranch
		}
	}
	if base == "" {
		b, err := p.baseBranch(ctx)
		if err != nil {
			return nil, err
		}
		base = b
	}
	folder := p.archivedFolderOn(ctx, base, id)
	if folder == "" {
		return nil, &FailError{Code: CodeChangeNotFound, Msg: "no change " + id + " in the archive on " + base + " — `ape status` lists the changes"}
	}
	stateRel := p.Rel(filepath.Join(p.ArchiveDir(), folder, "state.json"))
	docRel := p.Rel(filepath.Join(p.ArchiveDir(), folder, "change.md"))
	notes, err := p.readNotes(a.notes)
	if err != nil {
		return nil, err
	}
	var st *State
	_, err = p.editOnBranch(ctx, base, "apex: "+id+" accepted "+task+" (archived)\n\n"+a.trailers(id), []string{stateRel, docRel},
		func(cur map[string][]byte) (map[string][]byte, error) {
			if cur[stateRel] == nil || cur[docRel] == nil {
				return nil, &FailError{Code: "change.no-state", Msg: "the archived change " + id + " (" + folder + ") has no state.json or change.md"}
			}
			s, err := parseState(stateRel, cur[stateRel])
			if err != nil {
				return nil, &FailError{Code: CodeStateUnreadable, Msg: err.Error()}
			}
			if s.Status != StatusArchived {
				return nil, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is " + s.Status + " — accept applies to a change in flight or archived, not abandoned"}
			}
			doc := ParseChange(docRel, string(cur[docRel]))
			if err := humanTask(doc, id, task); err != nil {
				return nil, err
			}
			withAcceptance(s, doc, a.record(ev))
			if s.Baseline == nil {
				s.Baseline = []Baseline{}
			}
			data, err := json.MarshalIndent(s, "", "  ")
			if err != nil {
				return nil, err
			}
			st = s
			files := map[string][]byte{stateRel: append(data, '\n')}
			maps.Copy(files, notes)
			return files, nil
		})
	if err != nil {
		return nil, err
	}
	p.dropCommittedNotes(ctx, base, notes)
	return st, nil
}

// archivedFolderOn is the archive folder (<date>-<id>) of change id on
// branch, "" when it has none.
func (p *Project) archivedFolderOn(ctx context.Context, branch, id string) string {
	if here, err := p.checkedOutHere(ctx, branch); err == nil && here {
		return p.ArchivedIDs()[id]
	}
	out, err := git(ctx, p.Root, "ls-tree", "--name-only", "refs/heads/"+branch+":./"+p.Rel(p.ArchiveDir()))
	if err != nil {
		return ""
	}
	for name := range strings.SplitSeq(out, "\n") {
		if len(name) > 11 && name[10] == '-' && name[11:] == id {
			return name
		}
	}
	return ""
}

// awaitingHuman is a change's human-review tasks that wait for a person:
// those not accepted, once the change is reviewed or archived — nothing
// but the person is left. Earlier, the tasks are pending but the change is
// still the orchestrator's to verify and review. nil when none.
func awaitingHuman(st *State, c *Change) []string {
	if c == nil || st.Status != StatusReviewed && st.Status != StatusArchived {
		return nil
	}
	return st.PendingHuman(c.HumanTasks())
}

// archivedAwaitingHuman is the archived changes whose human-review tasks are not
// all accepted, as `ape status` lists them.
func (p *Project) archivedAwaitingHuman() []ChangeStatus {
	var out []ChangeStatus
	for _, c := range p.archivedChanges() {
		if c.State == nil || c.Doc == nil || c.State.Status != StatusArchived {
			continue
		}
		pending := c.State.PendingHuman(c.Doc.HumanTasks())
		if len(pending) == 0 {
			continue
		}
		cs := ChangeStatus{
			ID: c.ID, Title: c.Doc.Title, Milestone: c.Doc.Meta.Milestone, Status: StatusArchived, Lane: LaneMilestone,
			PendingHuman: pending, AwaitingHuman: pending,
			Accepted: c.State.Accepted, Delegate: p.delegateStatuses(c.ID),
		}
		if c.Doc.IsFix() {
			cs.Lane = LaneFix
		}
		out = append(out, cs)
	}
	return out
}
