// Package layout is APEX v1.5's NATS layout, frozen (v1.5 stage 0, step 1):
// the subjects coordination adds to ape's taxonomy, the buckets, streams and
// Object Stores, each key's class and single writer, and the grammar of
// lease keys, slot keys, lease tokens and event message ids. It is the one
// table both the code and docs/reference/coordination.md read; a test keeps
// the page equal to it.
//
// Source: the v1.5 proposal, rev 5 (§4.1–§4.4, §3.1, OQ13, OQ14, OQ22),
// plus R-2 (ape-meta) of D648+.
package layout

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Class says which record wins when a key and its source disagree.
type Class string

const (
	// Native: NATS is the record.
	Native Class = "native"
	// View: a copy of git; git wins.
	View Class = "view"
	// ViewGitHub: a copy of GitHub's state; GitHub wins (OQ11).
	ViewGitHub Class = "view (GitHub)"
)

// Store is one bucket, stream or Object Store.
type Store struct {
	Name      string
	Type      string   // "KV", "KV, per-key TTL", "stream", "Object Store"
	Keys      []string // key patterns (KV, Object Store) or subjects (stream)
	Class     Class
	Writer    string // the single writer
	Retention string // live retention
	Exists    bool   // already shipped before v1.5
}

// byRunner is the single writer of a run's records: the ape that ran it.
const byRunner = "the ape that ran it"

// Stores are every bucket, stream and Object Store v1.5 uses, in the order
// the page lists them.
var Stores = []Store{
	{Name: "ape-meta", Type: "KV", Keys: []string{"epoch"}, Class: Native, Writer: "the `sup.leader` holder", Retention: "no TTL; rewritten at each rebuild"},
	{Name: "ape-leases", Type: "KV, per-key TTL", Keys: []string{"steward.<ens>", "coord.<ens>", "sup.leader", "milestone.<project>.<M>", "close.<project>", "conductor.<user>.<ens>"}, Class: Native, Writer: "the claimant", Retention: "TTL; deleted on release or close"},
	{Name: "ape-slots", Type: "KV, per-key TTL", Keys: []string{"slot.<host>.<resource>.<n>"}, Class: Native, Writer: "the holder", Retention: "TTL"},
	{Name: "ape-runs", Type: "KV", Keys: []string{"run.<project>.<run-id>"}, Class: Native, Writer: byRunner, Retention: "90 days"},
	{Name: "ape-attempts", Type: "KV", Keys: []string{"attempt.<project>.<change>.<n>"}, Class: Native, Writer: "the change's ape", Retention: "deleted at milestone close"},
	{Name: "ape-usage", Type: "KV", Keys: []string{"usage.<user>"}, Class: Native, Writer: "that developer's ape", Retention: "latest only; history in the stream"},
	{Name: "ape-state", Type: "KV", Keys: []string{"milestone.<project>.<M>", "change.<project>.<id>", "contract.<ens>.<name>.v<N>", "ensemble.<ens>"}, Class: View, Writer: "ape, from git", Retention: "until the referencing close"},
	{Name: "ape-queue", Type: "KV", Keys: []string{"pr.<project>.<M>.change.<id>", "pr.<project>.<M>.close"}, Class: ViewGitHub, Writer: "the coordinator", Retention: "deleted at merge or close"},
	{Name: "APE_EVENTS", Type: "stream", Keys: []string{"ape.evt.>"}, Class: Native, Writer: "each publisher", Retention: "90 days"},
	{Name: "APE_METRICS", Type: "stream", Keys: []string{"ape.metrics.>"}, Class: Native, Writer: "each publisher", Retention: "90 days"},
	{Name: "ape-transcripts", Type: "Object Store", Keys: []string{"sha256/<hex>"}, Class: Native, Writer: byRunner, Retention: "90 days, or until the referencing close", Exists: true},
	{Name: "ape-gatelogs", Type: "Object Store", Keys: []string{"<project>/<M>/<run-id>/<check>.log.zst"}, Class: Native, Writer: byRunner, Retention: "90 days"},
}

// Subject is one subject pattern coordination publishes or serves.
type Subject struct {
	Pattern string
	Kind    string   // the event kind, or "service" for a request/reply root
	Names   []string // the events (or verbs) under it
}

// Subjects are v1.5's additions to ape's taxonomy (docs/reference/events.md):
// four event kinds under ape.evt and two request/reply roots.
var Subjects = []Subject{
	{Pattern: "ape.evt.<user>.<project>.project.<milestone>.<event>", Kind: "project", Names: []string{
		"person", "gate-green", "milestone-done", "run-end", "run-lost", "stop", "block",
		"plan-approved", "change-archived", "gate-red", "milestone-claimed", "milestone-released",
		"close-queued", "close-merged", "act-requested",
		"question-escalated", "milestone-paused", "milestone-resumed", "notice-acked",
	}},
	{Pattern: "ape.evt.<user>.<project>.change.<change-id>.<event>", Kind: "change", Names: []string{
		"pr-opened", "queued", "ejected", "merged", "rebased", "conflicted",
	}},
	{Pattern: "ape.evt.<user>.<ensemble>.contract.<name>.<event>", Kind: "contract", Names: []string{
		"drafted", "frozen", "fulfilled", "version-proposed", "version-accepted",
	}},
	{Pattern: "ape.evt.coord.<ensemble>.coord.<id>.<event>", Kind: "coord", Names: []string{
		"lease-expired", "slot-granted", "slot-reclaimed", "impact-notice", "hold-requested", "assessor-failed",
		"coordinator-replaced", "health-unconfirmed", "nats-paused", "nats-resumed", "heartbeat",
	}},
	{Pattern: "ape.coord.<ensemble>.<verb>", Kind: "service", Names: []string{"slot.acquire", "slot.release", "status", "hold.ack"}},
	{Pattern: "ape.sup.<verb>", Kind: "service", Names: []string{"ensure", "health.report", "status"}},
}

// Lease kinds and their timing (OQ13, OQ22).
const (
	LeaseTTL          = 30 * time.Second
	LeaseRenew        = 10 * time.Second
	ConductorLeaseTTL = 5 * time.Minute
	HealthDeadline    = 5 * time.Second
	RestartBudget     = 3
	RestartWindow     = 15 * time.Minute
	HeavyPerHost      = 3
)

// StewardKey is the ensemble's steward lease.
func StewardKey(ens string) string { return "steward." + ens }

// CoordKey is the ensemble's coordinator lease.
func CoordKey(ens string) string { return "coord." + ens }

// MilestoneKey is a milestone's lease, held by its orchestrator.
func MilestoneKey(project, m string) string { return "milestone." + project + "." + m }

// CloseKey is a project's close lease (D709): held by the one close that
// is taking main in, gating, enqueuing and waiting for its merge.
func CloseKey(project string) string { return "close." + project }

// ConductorKey is one person's conductor lease for an ensemble.
func ConductorKey(user, ens string) string { return "conductor." + user + "." + ens }

// SlotKey is a slot's lease: the n-th unit of a resource on a host.
func SlotKey(host, res string, n int) string { return fmt.Sprintf("slot.%s.%s.%d", host, res, n) }

// MessageID is an event's message id (OQ14).
func MessageID(project, checkout string, seq uint64) string {
	return fmt.Sprintf("%s/%s/%d", project, checkout, seq)
}

// ConductorConsumer is the durable consumer on APE_EVENTS through which a
// conductor receives its ensemble's events (R4): only the holder of
// ConductorKey(user, ens) binds it, and its acks are the lease's renewal
// signal (OQ22).
func ConductorConsumer(user, ens string) string { return "conductor-" + user + "-" + ens }

// ReservedTokens are the <user> tokens no member credential may carry
// (R1): the coordinator's and the supervisors' own, so a member cannot
// publish as them. A token starting with ReservedPrefix (aped's per-VM
// identities) is reserved too.
var ReservedTokens = []string{"coord", "sup"}

// ReservedPrefix marks aped's per-VM tokens, `vm-<id>`.
const ReservedPrefix = "vm-"

// Reserved reports whether a <user> token is one a member may not hold.
func Reserved(token string) bool {
	return slices.Contains(ReservedTokens, token) || strings.HasPrefix(token, ReservedPrefix)
}

// SupLeaderKey is the supervisors' leader lease (OQ9).
const SupLeaderKey = "sup.leader"

// EpochKey is ape-meta's one key: the cluster's epoch (§4.4, R-2).
const EpochKey = "epoch"

// Token is a lease token: the cluster's epoch and the lease key's KV
// revision (§3.1 fencing, §4.4). Only tokens of one epoch compare.
type Token struct {
	Epoch    uint64 `json:"epoch"`
	Revision uint64 `json:"revision"`
}

// String is the token's wire form, "<epoch>.<revision>".
func (t Token) String() string { return fmt.Sprintf("%d.%d", t.Epoch, t.Revision) }

// Newer reports whether t is newer than o: a higher epoch, or the same
// epoch and a higher revision.
func (t Token) Newer(o Token) bool {
	return t.Epoch > o.Epoch || t.Epoch == o.Epoch && t.Revision > o.Revision
}

// ---- the page --------------------------------------------------------------

// StoresTable renders Stores as the page's markdown table.
func StoresTable() string {
	var b strings.Builder
	b.WriteString("| Name | Type | Keys | Class | Single writer | Live retention |\n")
	b.WriteString("| ---- | ---- | ---- | ----- | ------------- | -------------- |\n")
	for _, s := range Stores {
		name := "`" + s.Name + "`"
		if s.Exists {
			name += " (exists)"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", name, s.Type, codeList(s.Keys), s.Class, s.Writer, s.Retention)
	}
	return b.String()
}

// SubjectsTable renders Subjects as the page's markdown table.
func SubjectsTable() string {
	var b strings.Builder
	b.WriteString("| Subject | Kind | Events or verbs |\n")
	b.WriteString("| ------- | ---- | --------------- |\n")
	for _, s := range Subjects {
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", s.Pattern, s.Kind, codeList(s.Names))
	}
	return b.String()
}

func codeList(xs []string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "`" + strings.ReplaceAll(x, "|", `\|`) + "`"
	}
	return strings.Join(out, ", ")
}
