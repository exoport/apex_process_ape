// Package ghapp is the GitHub App side of APEX v1.5's coordinated
// integration (PLAN-V15-GHAPP, D702/D703): the App JWT, installation tokens
// narrowed to one repository and a purpose's permissions, and the
// templated set-up acts (rulesets R1–R4, the queue workflow's PR, the two
// Actions secrets, a runner registration token). The key-holding service
// (serve.go) is its only user of the private keys; ape reaches it over
// NATS. net/http and the standard library only: no GitHub SDK.
package ghapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LoadKey reads an App's PEM private key (PKCS#1 or PKCS#8 RSA).
func LoadKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	b, _ := pem.Decode(pemBytes)
	if b == nil {
		return nil, errors.New("ghapp: no PEM block in the key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ghapp: parse key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("ghapp: the key is not RSA")
	}
	return rk, nil
}

// jwtLife is an App JWT's validity: nine minutes, GitHub allows ten.
const jwtLife = 9 * time.Minute

// maxBody bounds a GitHub response read.
const maxBody = 4 << 20

// AppJWT is the App's own JWT (RS256): issued a minute in the past for
// clock drift, valid jwtLife.
func AppJWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(jwtLife).Unix(),
		"iss": strconv.FormatInt(appID, 10),
	})
	if err != nil {
		return "", err
	}
	signing := head + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// Client calls GitHub's REST API with an App JWT or an installation token.
type Client struct {
	API  string
	HTTP *http.Client
}

// NewClient is a client with a bounded timeout.
func NewClient(api string) *Client {
	return &Client{API: strings.TrimRight(api, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// APIError is GitHub's refusal.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return "github: HTTP " + strconv.Itoa(e.Status) + ": " + e.Message }

func (c *Client) do(ctx context.Context, method, path, bearer string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.API+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &m)
		if m.Message == "" {
			m.Message = strings.TrimSpace(string(data))
		}
		return &APIError{Status: resp.StatusCode, Message: m.Message}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Token is an installation token.
type Token struct {
	Token       string            `json:"token"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Permissions map[string]string `json:"permissions"`
}

// Purposes and the permissions a pusher token carries for each (G9: never
// workflows, administration or secrets).
var Purposes = map[string]map[string]string{
	"push": {"contents": "write", "metadata": "read"},
	"pr":   {"contents": "write", "pull_requests": "write", "metadata": "read"},
	"read": {"contents": "read", "pull_requests": "read", "merge_queues": "read", "metadata": "read"},
	// watch is `ape ci watch`'s (D876, queue/watch): it reads main's ref,
	// the merge groups' refs and a compare, all contents: read, and nothing
	// else. Only a repository's watchers may ask for it, and they only for
	// it (Service.token).
	"watch": {"contents": "read", "metadata": "read"},
}

// WatchPurpose is the watchers' one purpose.
const WatchPurpose = "watch"

// OptionalPermissions are asked for, but a token is still minted without
// them when the App lacks them (FINDING-V15-1A-7 c) — GitHub refuses the
// whole token then. The reply names what is missing (Reply.Missing).
// merge_queues was asked for to read main's queue configuration; measured
// on the mirror (FINDING-V15-1B-PRE-2), GraphQL refuses that even with
// it, and project check reads the rulesets instead (Metadata: read).
var OptionalPermissions = []string{"merge_queues"}

// PermissionNames are the optional permissions as GitHub's App settings
// page names them.
var PermissionNames = map[string]string{"merge_queues": "Merge queues"}

// InstallationToken mints a token of installation inst narrowed to one
// repository (its name, without the owner) and perms.
func (c *Client) InstallationToken(ctx context.Context, appJWT string, inst int64, repoName string, perms map[string]string) (Token, error) {
	var t Token
	err := c.do(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(inst, 10)+"/access_tokens", appJWT,
		map[string]any{"repositories": []string{repoName}, "permissions": perms}, &t)
	return t, err
}
