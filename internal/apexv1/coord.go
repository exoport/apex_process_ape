package apexv1

// CodeCoordNATSUnreachable: a member project's ape found NATS unreachable
// when it had to start or integrate (v1.5, OQ5: claim, enqueue, elect).
// In-flight work continues; the refused command waits for the link.
const CodeCoordNATSUnreachable = "coord.nats-unreachable"

// CodeCoordMilestonePaused: the conductor's ape paused the milestone (v1.5,
// OQ24): a question to the conductor went unanswered past the pause age.
const CodeCoordMilestonePaused = "coord.milestone-paused"

// CodeNotLeaseHolder: a milestone's lease (milestone.<project>.<M>) is
// held by another member's checkout, or by none where it must be this
// one's (v1.5 stage 1a; proposal section 3.1).
const CodeNotLeaseHolder = "session.not-lease-holder"

// CodeQueueSettings: a project integrating through GitHub has a branch
// whose merge queue is missing or merges other than with a merge commit
// (v1.5 stage 1a, sections 5.2–5.3, OQ17).
const CodeQueueSettings = "coord.queue-settings"

// CodeCloseBusy: another member's close of this project holds the close
// lease (D709).
const CodeCloseBusy = "coord.close-busy"

// CodeCloseLost: right before its enqueue, a close found the close lease
// held by another checkout, or unreadable (FINDING-V15-1A-11).
const CodeCloseLost = "coord.close-lost"

// CodeCoordMembership: the coordination: block cannot be used — a field
// missing, an ensemble named as a member project (R8), or no credential
// at hot/ape-nats.creds.
const CodeCoordMembership = "coord.membership"

// CodeConductorCredential: `ape conductor attach` on a credential minted
// before V12-10, which grants the conductor's durable under its old name
// only: the server refused the new name (OBS-2D-2).
const CodeConductorCredential = "coord.conductor-credential"

// CodeCoordURLRewritten: in an App project, the checkout's own git config
// rewrites the coordinated repository's URL (url.*.insteadOf), so ape's
// call would not go where its token is for (FINDING-V15-1A-6).
const CodeCoordURLRewritten = "coord.url-rewritten"

// CodeNoticeUnknown and CodeNoticeAcked: `ape coord notice ack` of a
// notice the ensemble has none of, or one acknowledged already
// (PLAN-V15-ASSESSOR step 6).
const (
	CodeNoticeUnknown = "coord.notice-unknown"
	CodeNoticeAcked   = "coord.notice-acked"
)

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeCoordMilestonePaused, 1, ActionPerson, "the milestone is paused (v1.5): a question to the ensemble's conductor went unanswered past the pause age (OQ24), or a person paused it with `ape milestone pause` (the message names who and why); change new, start and archive of that milestone, its take-in and its close refuse, and other milestones go on — the conductor's answer resumes an OQ24 pause, and `ape milestone resume <M>` from the checkout holding its claim resumes either (milestone-resumed)"},
		CodeDoc{CodeNotLeaseHolder, 1, ActionPerson, "a project in an ensemble: `ape milestone claim <M>` found the milestone's lease held by another checkout, or an apex-loop run for M, `change new --milestone M` or `change start` of M's change found this checkout holding no lease on M (never claimed, released, or lapsed and taken by another member while no run renewed it) — a person claims M (`ape milestone claim <M>`), or agrees the handover with the holder the message names (v1.5 stage 1a, section 3.1)"},
		CodeDoc{CodeMilestoneBranchMissing, 1, ActionPerson, "a coordinated project (coordination.github): change new --milestone M or change start of M's change needs milestone/<M>, the branch M's changes are based on, and this checkout has none — a person claims M (`ape milestone claim M` creates it from main on the remote, or tracks the remote's) (v1.5 stage 1a, section 5.1)"},
		CodeDoc{CodeQueueSettings, 1, ActionPerson, "project check, a project integrating through GitHub (coordination.github): main or a milestone/<M> branch on the remote has no merge queue, or its queue merges by squash or rebase — main's must merge with a merge commit (the close PR's release point, OQ17), and a milestone's too (a take-in merges main, never squashed, section 5.2); a person sets the repository's rulesets (v1.5 stage 1a)"},
		CodeDoc{CodeCloseLost, 1, ActionRetry, "ape milestone close in a project integrating through GitHub: right before it enqueued its close PR, the close lease close.<project> (D709) was held by another checkout — it lapsed during this close (a NATS outage longer than its TTL) and another close took it — or could not be read; nothing was enqueued, since the PR would merge onto a main this close never gated against — close again once that close is done (FINDING-V15-1A-11)"},
		CodeDoc{CodeCloseBusy, 1, ActionRetry, "ape milestone close in a project integrating through GitHub: another checkout's close holds the project's close lease (close.<project>, D709), which serializes taking main in, gating on the merge, enqueuing and waiting for the merge — the message names the holder. A close with a --wait above 0 waits for it (at most 2 h) and is refused only past that; with --wait 0 it is refused at once — close again once it is done"},
		CodeDoc{CodeCoordMembership, 1, ActionPerson, "the project's coordination: block in _apex/config.yaml cannot be used (v1.5, OQ3, OQ4): it lacks ensemble, home or url, its ensemble is named as a member project (R8: both compared as subject tokens), the ensemble credential hot/ape-nats.creds is missing, or coordination.github names no <owner>/<name> repository or its token hot/github.token is missing (stage 1a) — a person fixes the block or places the credential. In a project whose GitHub access is the App service's (coordination.github.app), every git call to the coordinated repository refuses with it rather than fall back to the remote with git's own credentials, which may be a person's (FINDING-V15-1A-6)"},
		CodeDoc{CodeConductorCredential, 1, ActionPerson, "`ape conductor attach` (v1.5): the server refused the conductor's credential its durable conductor_<user>_<ensemble> — the credential was minted before ape 1.5 named the durable so (V12-10) and grants only the old conductor-<user>-<ensemble>; the attach stops at once, with nothing delivered and its lease let go. It ends the same way when, during an attach, the server closed its connection for refusing the credential (expired or revoked: nats.go closes a connection on the same auth error twice, V8D-CLOSED-1). The operator who holds the account's keys mints the person's credential again (`ape nats mint user --kind member …`), and it replaces hot/ape-nats.creds in each of the person's checkouts (OBS-2D-2)"},
		CodeDoc{CodeCoordURLRewritten, 1, ActionPerson, "a project whose GitHub access is the App service's (coordination.github.app): the checkout's own git config (.git/config) rewrites the coordinated repository's https URL with url.<base>.insteadOf, so ape's fetch or push would go elsewhere than its token is for, and refuses (FINDING-V15-1A-6) — the person's global and system config are never read for these calls; a person removes the rule from .git/config (`git config --unset url.<base>.insteadOf`)"},
		CodeDoc{CodeNoticeUnknown, 1, ActionPerson, "`ape coord notice ack <id>` (v1.5, PLAN-V15-ASSESSOR step 6): the ensemble's coordinator published no impact notice with that id — the ids are in `ape status --json`'s milestones[].notices and in the conductor's delivery"},
		CodeDoc{CodeNoticeAcked, 1, ActionPerson, "`ape coord notice ack <id>` (v1.5, PLAN-V15-ASSESSOR step 6): the notice was acknowledged already (the message says how, by whom and when); an ack is recorded once"},
		CodeDoc{CodeCoordNATSUnreachable, 1, ActionRetry, "a project in an ensemble (coordination: in _apex/config.yaml) needs NATS to start a milestone or a change and to integrate (claim, enqueue, elect), and NATS is unreachable (v1.5, OQ5): work already in flight continues; run the command again once NATS is back (`ape status` says since when it is down). change start, change archive, milestone take-in and milestone close wait for NATS in their run instead, at their start and at each lease fence inside it (D800+, V8l: exit 75 while a detached run waits, at most 2 h), and refuse with it only under --wait 0 or past that bound; they wait the same way for a milestone's pause state that could not be read, never taking it as \"not paused\". In a project whose GitHub access is the App service's, a git call to the coordinated repository whose token the service did not give refuses with it too, never falling back to git's own credentials (FINDING-V15-1A-6)"},
	)
}
