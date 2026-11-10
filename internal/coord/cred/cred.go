// Package cred is APEX v1.5's credentials (stage 0, step 3; V12, V12b):
// one allow-list per credential kind — a member's, a conductor's, the
// coordinator's and the supervisor's — and the minter's refusal of
// reserved tokens (R1). Stage 0 mints locally with aped's account; the
// auth-callout service is a later stage.
//
// No kind may purge, delete or reconfigure a stream (C1, D757/D758): NATS
// authorizes a JetStream API call by its API subject, not by the subjects
// of the messages it touches, so "$JS.API.>" let any member purge any
// lease and delete the event stream. Each kind gets the API calls its code
// makes, named stream by stream, measured by running that code under the
// list on an enforcing server (grants_test.go). Declaring the buckets and
// streams, and a rebuild, are `ape nats declare`'s, on the operator's own
// credential.
package cred

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// The streams ape's coordination reads, by their JetStream names.
const (
	eventsStream = "APE_EVENTS"
	leasesStream = "KV_ape-leases"
	metaStream   = "KV_ape-meta"
	slotsStream  = "KV_ape-slots"
)

// The App service's request subjects, ghapp.TokenSubject and
// ghapp.SetupSubject (.<user>): named here rather than imported, so
// ghapp's own tests can measure GHAppGrant (cred_test checks they agree).
const (
	appTokenSubject = "ape.sup.ghapp.pusher" //nolint:gosec // G101 false positive: a NATS subject, not a credential
	appSetupSubject = "ape.sup.ghapp.warden"
)

// leaseKV is the leases' key subject prefix.
const leaseKV = "$KV.ape-leases."

// readStreams is the JetStream API that reads streams, for each of
// streams: bind one (INFO), get a message (MSG.GET, and DIRECT.GET, which a
// KV get uses), and the consumers a read makes — an ordered consumer, a KV
// watch: create, info, next, delete, and the flow control a push watch
// answers. Never a stream's CREATE, UPDATE, DELETE, PURGE or MSG.DELETE.
// The coordinator's and the supervisor's: a person's credential reads by
// gets alone (getStreams, V12-3).
func readStreams(streams ...string) []string {
	out := make([]string, 0, 1+9*len(streams))
	out = append(out, "$JS.API.INFO")
	for _, s := range streams {
		out = append(out,
			"$JS.API.STREAM.INFO."+s,
			"$JS.API.STREAM.MSG.GET."+s,
			"$JS.API.DIRECT.GET."+s,
			"$JS.API.DIRECT.GET."+s+".>",
			"$JS.API.CONSUMER.CREATE."+s+".>",
			"$JS.API.CONSUMER.INFO."+s+".*",
			"$JS.API.CONSUMER.MSG.NEXT."+s+".*",
			"$JS.API.CONSUMER.DELETE."+s+".*",
			"$JS.FC."+s+".>",
		)
	}
	return out
}

// getStreams is the JetStream API that reads streams with no consumer, for
// each of streams: bind one (INFO) and get a message (MSG.GET, and
// DIRECT.GET, which a KV get uses). A member's and a conductor's reads are
// all gets (V12-3): consumer names carry no owner token NATS can scope, so
// a consumer verb on a stream reaches every person's consumer on it.
func getStreams(streams ...string) []string {
	out := make([]string, 0, 1+4*len(streams))
	out = append(out, "$JS.API.INFO")
	for _, s := range streams {
		out = append(out,
			"$JS.API.STREAM.INFO."+s,
			"$JS.API.STREAM.MSG.GET."+s,
			"$JS.API.DIRECT.GET."+s,
			"$JS.API.DIRECT.GET."+s+".>",
		)
	}
	return out
}

// durable is the consumer API for one consumer, by its exact name: create
// or update it (with one filter subject the name is followed by it), its
// info, and its pulls. Never its delete.
func durable(stream, name string) []string {
	pre := "$JS.API.CONSUMER."
	return []string{
		pre + "CREATE." + stream + "." + name,
		pre + "CREATE." + stream + "." + name + ".>",
		pre + "INFO." + stream + "." + name,
		pre + "MSG.NEXT." + stream + "." + name,
	}
}

// ConductorGrant is a conductor's permissions (`ape conductor attach`):
// its own conductor lease, its durable consumer of the ensemble's events
// and the acks of what it delivers, and the pause, resume and escalation
// events it publishes for its projects' milestones, as its user. Its
// durable is the only consumer it may touch, so another person can neither
// delete nor re-create it (V12-3).
func ConductorGrant(user, ensemble string, projects ...string) aped.Grant {
	u, e := natsconn.SubjectToken(user), natsconn.SubjectToken(ensemble)
	pub := []string{
		leaseKV + layout.ConductorKey(u, e),
		"$JS.ACK." + eventsStream + "." + layout.ConductorConsumer(u, e) + ".>",
	}
	for _, p := range projects {
		pub = append(pub, "ape.evt."+u+"."+natsconn.SubjectToken(p)+".project.>")
	}
	pub = append(pub, durable(eventsStream, layout.ConductorConsumer(u, e))...)
	// The durable's name before V12-10, read once to move from it and
	// deleted after the move (V12-10-2). Two pairs could share that name:
	// the conductor moves from and deletes it only when its filters are
	// its own, and neither pair may create, pull from or ack it.
	legacy := layout.LegacyConductorConsumer(u, e)
	pub = append(pub, "$JS.API.CONSUMER.INFO."+eventsStream+"."+legacy, "$JS.API.CONSUMER.DELETE."+eventsStream+"."+legacy)
	return aped.Grant{
		PubAllow: append(pub, getStreams(eventsStream, leasesStream, metaStream)...),
		SubAllow: []string{inbox(u)},
	}
}

// inbox is the subscribe grant on a user's own replies
// (natsconn.InboxPrefix, which every ape connection with a credential
// uses): never _INBOX.>, where another's App token or a warden's secrets
// would arrive (V12-1).
func inbox(tok string) string { return natsconn.InboxPrefix(tok) + ".>" }

// MemberGrant is a project credential's permissions (hot/ape-nats.creds):
// publish its own events and metrics, write its project's milestone and
// close leases (by compare-and-set; the server cannot require one), read
// the coordination streams, and the requests the verbs run in a member
// project make — `ape supervisor ensure|status`, `ape health report`, the
// App service's token (and `ape ghapp setup`'s) for its own user; never
// another project's lease, another user's events, or the coordinator's.
// `ape conductor attach` runs on this credential, so it carries the
// conductor's grant for the project too.
func MemberGrant(user, project, ensemble string) aped.Grant {
	u, p := natsconn.SubjectToken(user), natsconn.SubjectToken(project)
	c := ConductorGrant(user, ensemble, project)
	return aped.Grant{
		PubAllow: append([]string{
			"ape.evt." + u + "." + p + ".>",
			"ape.metrics." + u + "." + p + ".>",
			leaseKV + "milestone." + p + ".>",
			leaseKV + layout.CloseKey(p),
			"ape.sup.ensure",
			"ape.sup.status",
			"ape.sup.health.report",
			"ape.coord." + natsconn.SubjectToken(ensemble) + ".status",
			appTokenSubject + "." + u,
			appSetupSubject + "." + u,
		}, c.PubAllow...),
		// Its project's events (the event watch), and its own replies.
		SubAllow: []string{"ape.evt.*." + p + ".project.>", inbox(u)},
	}
}

// CoordinatorGrant is the permissions of an ensemble coordinator whose
// credential is named name (its inbox token) (V12b, C2):
// its coord.<ens> lease, the slots it grants, its events and the
// assessor's records under the coordinator's reserved owner token, its
// metrics, and its service's replies (ape.coord.<ens>.*).
func CoordinatorGrant(name, ensemble string) aped.Grant {
	e := natsconn.SubjectToken(ensemble)
	// The assessor's records' scope, coordinator.AssessorScope.
	assessor := natsconn.SubjectToken(ensemble + "-assessor")
	return aped.Grant{
		PubAllow: append([]string{
			"ape.evt.coord." + e + ".coord.>",
			"ape.evt.coord." + assessor + ".coord.>",
			"ape.metrics.coord." + e + ".>",
			leaseKV + layout.CoordKey(e),
			"$KV.ape-slots.slot.>",
		}, readStreams(eventsStream, leasesStream, metaStream, slotsStream)...),
		SubAllow:       []string{"ape.coord." + e + ".>", "$SRV.>", inbox(natsconn.SubjectToken(name))},
		AllowResponses: true,
	}
}

// SupervisorGrant is the permissions of a supervisor whose credential is
// named name (its inbox token) (V12b): the sup.leader
// lease, the coordinators' leases it clears when one is unhealthy, the
// coord-kind events it publishes for any ensemble (heartbeats, a
// coordinator replaced), its health requests to the coordinators, and its
// service's replies (ape.sup.*).
func SupervisorGrant(name string) aped.Grant {
	return aped.Grant{
		PubAllow: append([]string{
			leaseKV + layout.SupLeaderKey,
			leaseKV + "coord.*",
			"ape.evt.coord.*.coord.>",
			"ape.coord.*.status",
		}, readStreams(eventsStream, leasesStream, metaStream)...),
		// Its service's own verbs: never ape.sup.ghapp.>, the App
		// service's, whose requests carry secrets.
		SubAllow:       []string{"ape.sup.ensure", "ape.sup.health.report", "ape.sup.status", "$SRV.>", inbox(natsconn.SubjectToken(name))},
		AllowResponses: true,
	}
}

// GHAppGrant is the App service's permissions (`ape ghapp serve`, V12g,
// R4): it answers the token and set-up requests of every user
// (ape.sup.ghapp.pusher.* and .warden.*, a queue group) and replies to
// each once (allow_responses). It publishes nothing else, reads no stream
// and holds no lease. Its only standing publish is its own inbox, so that
// the list is not empty (an empty allow list is no restriction).
func GHAppGrant(name string) aped.Grant {
	in := inbox(natsconn.SubjectToken(name))
	return aped.Grant{
		PubAllow:       []string{in},
		SubAllow:       []string{appTokenSubject + ".*", appSetupSubject + ".*", in},
		AllowResponses: true,
	}
}

// CIGrant is the permissions of the CI publisher (`ape ci event`, in a
// workflow, V12g): the merged / ejected / close-merged project events of
// projects, under its own user token (the credential's name), and the
// stream's acks on its inbox. Nothing else: no lease, no read, no request.
func CIGrant(name string, projects ...string) aped.Grant {
	u := natsconn.SubjectToken(name)
	pub := make([]string, 0, len(projects))
	for _, p := range projects {
		pub = append(pub, "ape.evt."+u+"."+natsconn.SubjectToken(p)+".project.>")
	}
	if len(pub) == 0 {
		pub = append(pub, inbox(u)) // never an empty list: that is no restriction
	}
	return aped.Grant{PubAllow: pub, SubAllow: []string{inbox(u)}}
}

// WatchGrant is `ape ci watch`'s (D876, queue/watch): CIGrant's project
// events, plus asking the coordination host's App service for its read-only
// watch token (ape.sup.ghapp.pusher.<own user>). It runs on that host and
// never on a CI runner; a ci credential, which may sit in a GitHub secret,
// never gets the token subject.
func WatchGrant(name string, projects ...string) aped.Grant {
	g := CIGrant(name, projects...)
	g.PubAllow = append(g.PubAllow, appTokenSubject+"."+natsconn.SubjectToken(name))
	return g
}

// Mint issues a member credential for user's project of ensemble,
// refusing a name whose subject token is reserved (`coord`, `sup`,
// `vm-*`): a member must not be able to publish as the coordinator, a
// supervisor or a VM. It also refuses a name whose token is one of
// inUse's (the names already minted in the account; V12-5): two names that
// slug to one token share its events, its inbox and its pauses.
func Mint(account aped.Account, user, project, ensemble string, expires time.Duration, inUse ...string) ([]byte, error) {
	if err := checkName(user, inUse); err != nil {
		return nil, err
	}
	creds, _, err := account.MintUser(user, MemberGrant(user, project, ensemble), expires)
	return creds, err
}

// checkName is a credential name's checks: a subject token, not reserved,
// and no other name in inUse with the same token.
func checkName(user string, inUse []string) error {
	tok := natsconn.SubjectToken(user)
	if tok == "" {
		return errors.New("cred: empty user name")
	}
	if strings.Contains(tok, layout.NameSep) {
		return fmt.Errorf("cred: %q's subject token %q holds %q, the separator of the conductor's durable conductor_<user>_<ensemble> (V12-10); name the member otherwise", user, tok, layout.NameSep)
	}
	if layout.Reserved(tok) {
		return fmt.Errorf("cred: %q's subject token %q is reserved (coord, sup and vm-* are ape's own); name the member otherwise", user, tok)
	}
	for _, other := range inUse {
		if natsconn.SubjectToken(other) == tok && other != user {
			return fmt.Errorf("cred: %q's subject token %q is %q's already: two names on one token share its events, inbox and pauses; name the member otherwise", user, tok, other)
		}
	}
	return nil
}
