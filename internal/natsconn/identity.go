package natsconn

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// Identity is the user identity decoded from a NATS .creds file's user
// JWT. It is decoded offline — base64 + JSON, stdlib only, no server round
// trip and no JWT library (PLAN-13 D1 / PLAN-17 D1). The JWT signature is
// not verified here; the NATS server is the authority that enforces the
// credential. This decode only mirrors what the credential already asserts
// so ape can stamp it into subjects and payloads.
type Identity struct {
	Name         string // JWT "name" claim (may be empty)
	Subject      string // user public key (JWT "sub")
	SubjectToken string // slugged name used in subjects; falls back to Subject
}

// ErrNoCreds is returned when no credentials file is configured.
var ErrNoCreds = errors.New("natsconn: no credentials file configured")

// credsJWTBegin marks the start of the user JWT block in a .creds file. The
// end marker's dash count varies across nats tooling, so extractUserJWT
// matches its stable core text rather than a second constant.
const credsJWTBegin = "-----BEGIN NATS USER JWT-----" //nolint:gosec // G101 false positive: a PEM block marker, not a credential

// Identity decodes the identity for this config's creds file. Returns
// ErrNoCreds when the config carries no creds file. This is the
// `natsconn.Identity()` the plans reference; the package-level
// DecodeIdentity is the testable core.
func (c Config) Identity() (Identity, error) {
	if c.CredsFile == "" {
		return Identity{}, ErrNoCreds
	}
	return DecodeIdentity(c.CredsFile)
}

// DecodeIdentity reads a .creds file and decodes the embedded user JWT's
// claims into an Identity.
func DecodeIdentity(credsFile string) (Identity, error) {
	data, err := os.ReadFile(credsFile)
	if err != nil {
		return Identity{}, fmt.Errorf("natsconn: read creds: %w", err)
	}
	token, err := extractUserJWT(string(data))
	if err != nil {
		return Identity{}, err
	}
	claims, err := decodeJWTClaims(token)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{Name: claims.Name, Subject: claims.Subject}
	id.SubjectToken = SubjectToken(id.Name)
	if id.SubjectToken == "" {
		// Empty/whitespace-only name: fall back to the public key so the
		// token is always non-empty and deterministic.
		id.SubjectToken = SubjectToken(id.Subject)
	}
	return id, nil
}

// jwtClaims is the minimal shape ape needs from a NATS user JWT's claims
// segment. NATS JWT v2 puts these at the top level of the payload.
type jwtClaims struct {
	Name    string `json:"name"`
	Subject string `json:"sub"`
}

// extractUserJWT pulls the JWT token out of the standard
// `-----BEGIN NATS USER JWT-----` block of a .creds file. The block body
// may be split across lines; they are concatenated.
func extractUserJWT(creds string) (string, error) {
	start := strings.Index(creds, credsJWTBegin)
	if start < 0 {
		return "", errors.New("natsconn: no NATS USER JWT block in creds file")
	}
	rest := creds[start+len(credsJWTBegin):]
	// The end marker's dash count varies slightly across nats tooling; match
	// on the stable core text to stay robust.
	end := strings.Index(rest, "END NATS USER JWT")
	if end >= 0 {
		// Trim back to the line the end marker starts on.
		if dash := strings.LastIndex(rest[:end], "-----"); dash >= 0 {
			end = dash
		}
		rest = rest[:end]
	}
	var b strings.Builder
	for line := range strings.SplitSeq(rest, "\n") {
		b.WriteString(strings.TrimSpace(line))
	}
	token := b.String()
	if token == "" {
		return "", errors.New("natsconn: empty NATS USER JWT block")
	}
	return token, nil
}

// decodeJWTClaims base64url-decodes the claims (middle) segment of a JWT
// and JSON-unmarshals it. Stdlib only.
func decodeJWTClaims(token string) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return jwtClaims{}, errors.New("natsconn: malformed JWT (want header.payload.signature)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some encoders pad; fall back to the padded variant.
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return jwtClaims{}, fmt.Errorf("natsconn: decode JWT claims: %w", err)
		}
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return jwtClaims{}, fmt.Errorf("natsconn: unmarshal JWT claims: %w", err)
	}
	return claims, nil
}

// SubjectToken slugs a string for use as a NATS subject token: lowercased,
// with the subject-syntax metacharacters (`.`, `*`, `>`) and any
// whitespace collapsed to `-`. This is the single source of truth for the
// <user> token; operators scope publish permissions to `ape.*.<token>.>`
// by deriving the token the same way (PLAN-17 D1).
func SubjectToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r == '.' || r == '*' || r == '>':
			b.WriteRune('-')
		case unicode.IsSpace(r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
