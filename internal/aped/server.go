package aped

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nkeys"
)

// Account names as they appear in the account JWTs stored in the resolver.
const (
	accountHostOps   = "HOST_OPS"  // management: host `ape` + the aped service identity
	accountTelemetry = "TELEMETRY" // per-VM users + aped's ingestion subscriber
)

// ServerConfig configures the embedded management NATS server aped-front runs.
type ServerConfig struct {
	// Host/Port is the management listener. Locally this is 127.0.0.1 (host
	// `ape`, guest-unreachable); guest telemetry reaches a separate bridge-IP
	// listener (a Tier-2 deployment concern — account isolation, not the port,
	// is the load-bearing guest→host barrier). Port -1 picks a random free port
	// (tests); production sets a fixed port.
	Host string
	Port int
	// StoreDir persists the operator + account nkey seeds so identities (and
	// therefore already-minted per-VM creds) survive an aped restart (D7). ""
	// keeps everything in memory — for tests.
	StoreDir string
	// Name is the server name reported in monitoring; default "aped".
	Name string
}

// Server is aped's embedded nats-server plus the two accounts whose keys mint
// user credentials into it. HostOps signs management identities; Telemetry
// signs the per-VM telemetry creds (MintVMCreds).
type Server struct {
	ns        *server.Server
	operator  nkeys.KeyPair
	hostOps   Account
	telemetry Account
	url       string
}

// StartServer brings up the embedded server in operator/JWT mode with the two
// isolated accounts and an in-memory account resolver (required to hot-mint
// per-VM users without a reload — D2). It blocks until the server accepts
// connections. Call Shutdown to stop it.
func StartServer(cfg ServerConfig) (*Server, error) {
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Name == "" {
		cfg.Name = "aped"
	}

	operator, err := loadOrCreateOperatorKey(cfg.StoreDir)
	if err != nil {
		return nil, err
	}
	opub, err := operator.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("aped: operator public key: %w", err)
	}
	hostOps, err := loadOrCreateAccount(cfg.StoreDir, "host_ops")
	if err != nil {
		return nil, err
	}
	telemetry, err := loadOrCreateAccount(cfg.StoreDir, "telemetry")
	if err != nil {
		return nil, err
	}

	res := &server.MemAccResolver{}
	for _, a := range []struct {
		acct Account
		name string
	}{{hostOps, accountHostOps}, {telemetry, accountTelemetry}} {
		ajwt, err := a.acct.Encode(a.name, operator)
		if err != nil {
			return nil, err
		}
		if err := res.Store(a.acct.Public(), ajwt); err != nil {
			return nil, fmt.Errorf("aped: store %s account jwt: %w", a.name, err)
		}
	}

	opts := &server.Options{
		ServerName:      cfg.Name,
		Host:            cfg.Host,
		Port:            cfg.Port,
		NoLog:           true, // aped owns diagnostics; keep the embedded server off stdout
		NoSigs:          true, // aped owns signal handling
		TrustedKeys:     []string{opub},
		AccountResolver: res,
	}
	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("aped: new server: %w", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		ns.Shutdown()
		return nil, errors.New("aped: embedded server not ready within 5s")
	}

	return &Server{
		ns:        ns,
		operator:  operator,
		hostOps:   hostOps,
		telemetry: telemetry,
		url:       ns.ClientURL(),
	}, nil
}

// ClientURL is the management server's client URL.
func (s *Server) ClientURL() string { return s.url }

// HostOps returns the HOST_OPS account (mints management identities).
func (s *Server) HostOps() Account { return s.hostOps }

// Telemetry returns the TELEMETRY account (mints per-VM telemetry creds).
func (s *Server) Telemetry() Account { return s.telemetry }

// Shutdown stops the embedded server.
func (s *Server) Shutdown() {
	if s.ns != nil {
		s.ns.Shutdown()
	}
}

// loadOrCreateOperatorKey loads the persisted operator seed or generates one.
// With no StoreDir the key is ephemeral (a fresh operator per process — fine
// for tests, never for a restarting daemon).
func loadOrCreateOperatorKey(dir string) (nkeys.KeyPair, error) {
	if dir == "" {
		kp, err := nkeys.CreateOperator()
		if err != nil {
			return nil, fmt.Errorf("aped: create operator key: %w", err)
		}
		return kp, nil
	}
	path := filepath.Join(dir, "keys", "operator.seed")
	if seed, err := os.ReadFile(path); err == nil {
		kp, err := nkeys.FromSeed(seed)
		if err != nil {
			return nil, fmt.Errorf("aped: load operator seed %s: %w", path, err)
		}
		return kp, nil
	}
	kp, err := nkeys.CreateOperator()
	if err != nil {
		return nil, fmt.Errorf("aped: create operator key: %w", err)
	}
	seed, err := kp.Seed()
	if err != nil {
		return nil, fmt.Errorf("aped: operator seed: %w", err)
	}
	if err := writeSecret(path, seed); err != nil {
		return nil, err
	}
	return kp, nil
}

// loadOrCreateAccount loads a persisted account seed under <dir>/keys/<name>.seed
// or generates and persists one.
func loadOrCreateAccount(dir, name string) (Account, error) {
	if dir == "" {
		return NewAccount()
	}
	path := filepath.Join(dir, "keys", name+".seed")
	if seed, err := os.ReadFile(path); err == nil {
		return AccountFromSeed(seed)
	}
	a, err := NewAccount()
	if err != nil {
		return Account{}, err
	}
	seed, err := a.Seed()
	if err != nil {
		return Account{}, fmt.Errorf("aped: %s account seed: %w", name, err)
	}
	if err := writeSecret(path, seed); err != nil {
		return Account{}, err
	}
	return a, nil
}

// writeSecret writes a 0600 file (creating its 0700 parent), for nkey seeds
// and .creds. These embed private keys — never world-readable, never logged.
func writeSecret(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("aped: mkdir secret dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("aped: write secret %s: %w", path, err)
	}
	return nil
}
