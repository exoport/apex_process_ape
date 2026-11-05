package cred

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	"github.com/exoport/apex_process_ape/internal/aped"
)

// A key directory is an enforcing cluster's trust root (stage 2a, D813): the
// operator, the system account SYS and the account APP the ensemble lives
// in, their seeds (0600), the server's resolver.conf, and minted.json, the
// names minted so far. `ape nats mint` writes it; nothing else needs nsc.
const (
	operatorSeedFile = "operator.nk"
	sysSeedFile      = "sys.nk"
	appSeedFile      = "app.nk"
	ResolverConf     = "resolver.conf"
	mintedFile       = "minted.json"
)

// Kinds are the credential kinds MintUserIn mints, each with its grant.
var Kinds = []string{"member", "conductor", "coordinator", "supervisor", "ghapp", "ci", "admin"}

// InitKeyDir creates an operator, SYS and APP (JetStream unlimited, as
// natstest.StartOperatorJetStream) in dir, and the resolver.conf a
// nats-server includes: operator, system_account, a MEMORY resolver with
// both accounts preloaded. It refuses a dir that holds a key already.
func InitKeyDir(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, operatorSeedFile)); err == nil {
		return fmt.Errorf("cred: %s already holds an operator: a key directory is made once", dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	okp, err := nkeys.CreateOperator()
	if err != nil {
		return err
	}
	opub, err := okp.PublicKey()
	if err != nil {
		return err
	}
	oc := jwt.NewOperatorClaims(opub)
	oc.Name = "ape-2a"
	ojwt, err := oc.Encode(okp)
	if err != nil {
		return err
	}
	accounts := map[string]string{}
	var sysPub string
	for _, a := range []struct {
		name, file string
		js         bool
	}{{"SYS", sysSeedFile, false}, {"APP", appSeedFile, true}} {
		akp, err := nkeys.CreateAccount()
		if err != nil {
			return err
		}
		apub, err := akp.PublicKey()
		if err != nil {
			return err
		}
		ac := jwt.NewAccountClaims(apub)
		ac.Name = a.name
		if a.js {
			ac.Limits.JetStreamLimits = jwt.JetStreamLimits{MemoryStorage: -1, DiskStorage: -1, Streams: -1, Consumer: -1}
		}
		ajwt, err := ac.Encode(okp)
		if err != nil {
			return err
		}
		accounts[apub] = ajwt
		if a.name == "SYS" {
			sysPub = apub
		}
		if err := writeSeed(filepath.Join(dir, a.file), akp); err != nil {
			return err
		}
	}
	if err := writeSeed(filepath.Join(dir, operatorSeedFile), okp); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "operator: %s\nsystem_account: %s\nresolver: MEMORY\nresolver_preload: {\n", ojwt, sysPub)
	for _, pub := range slices.Sorted(func(yield func(string) bool) {
		for k := range accounts {
			if !yield(k) {
				return
			}
		}
	}) {
		fmt.Fprintf(&b, "  %s: %s\n", pub, accounts[pub])
	}
	b.WriteString("}\n")
	if err := WritePrivate(filepath.Join(dir, ResolverConf), []byte(b.String())); err != nil {
		return err
	}
	return writeMinted(dir, nil)
}

func writeSeed(path string, kp nkeys.KeyPair) error {
	seed, err := kp.Seed()
	if err != nil {
		return err
	}
	return WritePrivate(path, seed)
}

// UserSpec is one credential to mint: its kind, its name (the JWT name and
// subject token) and the kind's scope.
type UserSpec struct {
	Kind, Name, Ensemble string
	Projects             []string
	Expires              time.Duration
}

// GrantFor is spec's kind's grant: exactly the measured cred grant.
func GrantFor(s UserSpec) (aped.Grant, error) {
	need := func(what string, ok bool) error {
		if !ok {
			return fmt.Errorf("cred: a %s credential needs %s", s.Kind, what)
		}
		return nil
	}
	switch s.Kind {
	case "member":
		if err := need("one project and the ensemble", len(s.Projects) == 1 && s.Ensemble != ""); err != nil {
			return aped.Grant{}, err
		}
		return MemberGrant(s.Name, s.Projects[0], s.Ensemble), nil
	case "conductor":
		if err := need("the ensemble and its projects", s.Ensemble != "" && len(s.Projects) > 0); err != nil {
			return aped.Grant{}, err
		}
		return ConductorGrant(s.Name, s.Ensemble, s.Projects...), nil
	case "coordinator":
		if err := need("the ensemble", s.Ensemble != ""); err != nil {
			return aped.Grant{}, err
		}
		return CoordinatorGrant(s.Name, s.Ensemble), nil
	case "supervisor":
		return SupervisorGrant(s.Name), nil
	case "ghapp":
		return GHAppGrant(s.Name), nil
	case "ci":
		if err := need("its projects", len(s.Projects) > 0); err != nil {
			return aped.Grant{}, err
		}
		return CIGrant(s.Name, s.Projects...), nil
	case "admin":
		return aped.Grant{}, nil // the operator's own: unrestricted, for `ape nats declare` and reads
	}
	return aped.Grant{}, fmt.Errorf("cred: kind %q is not one of %s", s.Kind, strings.Join(Kinds, ", "))
}

// MintUserIn mints spec in dir's APP account, refusing a reserved token
// (all kinds but admin) or a token another name in minted.json has
// (V12-5), and records the name. It returns the .creds bytes.
func MintUserIn(dir string, spec UserSpec) ([]byte, error) {
	g, err := GrantFor(spec)
	if err != nil {
		return nil, err
	}
	seed, err := os.ReadFile(filepath.Join(dir, appSeedFile))
	if err != nil {
		return nil, fmt.Errorf("cred: %s is not a key directory (`ape nats mint init`): %w", dir, err)
	}
	acct, err := aped.AccountFromSeed(seed)
	if err != nil {
		return nil, err
	}
	minted, err := readMinted(dir)
	if err != nil {
		return nil, err
	}
	var creds []byte
	switch spec.Kind {
	case "member":
		creds, err = Mint(acct, spec.Name, spec.Projects[0], spec.Ensemble, spec.Expires, minted...)
	case "admin":
		creds, _, err = acct.MintUser(spec.Name, g, spec.Expires)
	default:
		if err := checkName(spec.Name, minted); err != nil {
			return nil, err
		}
		creds, _, err = acct.MintUser(spec.Name, g, spec.Expires)
	}
	if err != nil {
		return nil, err
	}
	if !slices.Contains(minted, spec.Name) {
		minted = append(minted, spec.Name)
	}
	return creds, writeMinted(dir, minted)
}

func readMinted(dir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, mintedFile))
	if err != nil {
		return nil, err
	}
	var m struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("cred: %s: %w", mintedFile, err)
	}
	return m.Names, nil
}

func writeMinted(dir string, names []string) error {
	data, err := json.MarshalIndent(map[string][]string{"names": names}, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivate(filepath.Join(dir, mintedFile), append(data, '\n'))
}

// WritePrivate writes a secret 0600 by temp-and-rename: a key directory's
// seeds and resolver.conf, and the .creds `ape nats mint user` writes.
func WritePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ape-"+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
