// Package cred is APEX v1.5's member credential (stage 0, step 3): what a
// project credential may publish and which lease keys it may write
// (proposal §4.6), and the minter's refusal of reserved tokens (R1).
// Stage 0 mints locally with aped's account; the auth-callout service is a
// later stage.
package cred

import (
	"errors"
	"fmt"
	"time"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// MemberGrant is a project credential's permissions: publish its own
// events, write its own conductor leases and the shared lease kinds by
// compare-and-set, and use JetStream; never another user's conductor key.
func MemberGrant(user, project string) aped.Grant {
	tok := natsconn.SubjectToken(user)
	kv := "$KV.ape-leases."
	return aped.Grant{
		PubAllow: []string{
			"ape.*." + tok + "." + natsconn.SubjectToken(project) + ".>",
			kv + "conductor." + tok + ".>",
			kv + "milestone.>",
			kv + "steward.>",
			"$JS.API.>",
		},
		SubAllow: []string{"ape.>", "_INBOX.>"},
	}
}

// Mint issues a member credential for user's project, refusing a name whose
// subject token is reserved (`coord`, `sup`, `vm-*`): a member must not be
// able to publish as the coordinator, a supervisor or a VM.
func Mint(account aped.Account, user, project string, expires time.Duration) ([]byte, error) {
	tok := natsconn.SubjectToken(user)
	if tok == "" {
		return nil, errors.New("cred: empty user name")
	}
	if layout.Reserved(tok) {
		return nil, fmt.Errorf("cred: %q's subject token %q is reserved (coord, sup and vm-* are ape's own); name the member otherwise", user, tok)
	}
	creds, _, err := account.MintUser(user, MemberGrant(user, project), expires)
	return creds, err
}
