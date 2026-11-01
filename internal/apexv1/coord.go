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

// CodeCoordMembership: the coordination: block cannot be used — a field
// missing, an ensemble named as a member project (R8), or no credential
// at hot/ape-nats.creds.
const CodeCoordMembership = "coord.membership"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeCoordMilestonePaused, 1, ActionPerson, "the milestone is paused (v1.5, OQ24): a question to the ensemble's conductor went unanswered past the pause age, so change new and change start of that milestone refuse; other milestones go on — the conductor's answer resumes it (milestone-resumed)"},
		CodeDoc{CodeNotLeaseHolder, 1, ActionPerson, "a project in an ensemble: `ape milestone claim <M>` found the milestone's lease held by another checkout, or an apex-loop run for M, `change new --milestone M` or `change start` of M's change found this checkout holding no lease on M (never claimed, released, or lapsed and taken by another member while no run renewed it) — a person claims M (`ape milestone claim <M>`), or agrees the handover with the holder the message names (v1.5 stage 1a, section 3.1)"},
		CodeDoc{CodeCoordMembership, 1, ActionPerson, "the project's coordination: block in _apex/config.yaml cannot be used (v1.5, OQ3, OQ4): it lacks ensemble, home or url, its ensemble is named as a member project (R8: both compared as subject tokens), the ensemble credential hot/ape-nats.creds is missing, or coordination.github names no <owner>/<name> repository or its token hot/github.token is missing (stage 1a) — a person fixes the block or places the credential"},
		CodeDoc{CodeCoordNATSUnreachable, 1, ActionRetry, "a project in an ensemble (coordination: in _apex/config.yaml) needs NATS to start a milestone or a change and to integrate (claim, enqueue, elect), and NATS is unreachable (v1.5, OQ5): work already in flight continues; run the command again once NATS is back (`ape status` says since when it is down)"},
	)
}
