package apexv1

// CodeCoordNATSUnreachable: a member project's ape found NATS unreachable
// when it had to start or integrate (v1.5, OQ5: claim, enqueue, elect).
// In-flight work continues; the refused command waits for the link.
const CodeCoordNATSUnreachable = "coord.nats-unreachable"

// CodeCoordMembership: the coordination: block cannot be used — a field
// missing, an ensemble named as a member project (R8), or no credential
// at hot/ape-nats.creds.
const CodeCoordMembership = "coord.membership"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeCoordMembership, 1, ActionPerson, "the project's coordination: block in _apex/config.yaml cannot be used (v1.5, OQ3, OQ4): it lacks ensemble, home or url, its ensemble is named as a member project (R8: both compared as subject tokens), or the ensemble credential hot/ape-nats.creds is missing — a person fixes the block or places the credential"},
		CodeDoc{CodeCoordNATSUnreachable, 1, ActionRetry, "a project in an ensemble (coordination: in _apex/config.yaml) needs NATS to start a milestone or a change and to integrate (claim, enqueue, elect), and NATS is unreachable (v1.5, OQ5): work already in flight continues; run the command again once NATS is back (`ape status` says since when it is down)"},
	)
}
