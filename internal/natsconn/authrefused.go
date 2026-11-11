package natsconn

import (
	"errors"
	"strings"

	"github.com/nats-io/nats.go"
)

// authRefusals are the server's own texts for a refused credential, as
// nats.go's checkAuthError reads them.
var authRefusals = []string{
	"authorization violation",
	"user authentication expired",
	"user authentication revoked",
	"account authentication expired",
}

// AuthRefused is an error that says the server refused the credential
// itself — rejected, expired or revoked — not an outage (V8LM2-1). It
// reads the refusal as it arrives (FINDING-FD6-1): nats.go's sentinels,
// and the server's text, which is what a connect or reconnect refused for
// an expired or revoked credential carries (an unexported natsProtoErr
// whose Is matches neither ErrAuthExpired nor ErrAuthRevoked: its "user
// authentication expired" is not their "authentication expired").
func AuthRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, nats.ErrAuthorization) || errors.Is(err, nats.ErrAuthExpired) || errors.Is(err, nats.ErrAuthRevoked) ||
		errors.Is(err, nats.ErrAccountAuthExpired) {
		return true
	}
	e := strings.ToLower(err.Error())
	for _, r := range authRefusals {
		if strings.Contains(e, r) {
			return true
		}
	}
	return false
}
