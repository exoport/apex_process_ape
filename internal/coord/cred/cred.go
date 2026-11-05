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
	"time"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// The streams ape's coordination reads, by their JetStream names.
const (
	eventsStream = "APE_EVENTS"
	leasesStream = "KV_ape-leases"
	metaStream   = "KV_ape-meta"
	slotsStream  = "KV_ape-slots"
)

// leaseKV is the leases' key subject prefix.
const leaseKV = "$KV.ape-leases."

// readStreams is the JetStream API that reads streams, for each of
// streams: bind one (INFO), get a message (MSG.GET, and DIRECT.GET, which a
// KV get uses), and the consumers a read makes — an ordered consumer, a KV
// watch, the conductor's durable: create, info, next, delete, and the
// flow control a push watch answers. Never a stream's CREATE, UPDATE,
// DELETE, PURGE or MSG.DELETE.
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

// ConductorGrant is a conductor's permissions (`ape conductor attach`):
// its own conductor lease, its durable consumer of the ensemble's events
// and the acks of what it delivers, and the pause, resume and escalation
// events it publishes for its projects' milestones, as its user.
func ConductorGrant(user, ensemble string, projects ...string) aped.Grant {
	u, e := natsconn.SubjectToken(user), natsconn.SubjectToken(ensemble)
	pub := []string{
		leaseKV + layout.ConductorKey(u, e),
		"$JS.ACK." + eventsStream + "." + layout.ConductorConsumer(u, e) + ".>",
	}
	for _, p := range projects {
		pub = append(pub, "ape.evt."+u+"."+natsconn.SubjectToken(p)+".project.>")
	}
	return aped.Grant{
		PubAllow: append(pub, readStreams(eventsStream, leasesStream, metaStream)...),
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
			ghapp.TokenSubject + "." + u,
			ghapp.SetupSubject + "." + u,
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

// Mint issues a member credential for user's project of ensemble,
// refusing a name whose subject token is reserved (`coord`, `sup`,
// `vm-*`): a member must not be able to publish as the coordinator, a
// supervisor or a VM.
func Mint(account aped.Account, user, project, ensemble string, expires time.Duration) ([]byte, error) {
	tok := natsconn.SubjectToken(user)
	if tok == "" {
		return nil, errors.New("cred: empty user name")
	}
	if layout.Reserved(tok) {
		return nil, fmt.Errorf("cred: %q's subject token %q is reserved (coord, sup and vm-* are ape's own); name the member otherwise", user, tok)
	}
	creds, _, err := account.MintUser(user, MemberGrant(user, project, ensemble), expires)
	return creds, err
}
