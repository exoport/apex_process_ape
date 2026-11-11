package natstest

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Cluster is an embedded JetStream cluster of loopback servers (v1.5
// stage 0): the Tier-1 stand-in for a team's three-node cluster, so
// leases, R3 buckets and the rebuild run in `go test` with no binary.
type Cluster struct {
	t       *testing.T
	Servers []*server.Server
	opts    []*server.Options
}

// ReadyWait bounds how long a node may take to accept connections: a
// node restarted on its store replays it first, which took more than 10 s
// under a loaded ci-local (V8-WIN-4), and a fresh node more than 30 s
// once (V8-FLAKE-4).
var ReadyWait = 60 * time.Second

// clusterAttempts bounds RunCluster's tries on fresh ports.
const clusterAttempts = 5

// readyPoll is how often startNode looks at the node's fatal lines while
// it waits.
const readyPoll = 250 * time.Millisecond

// peerPoll is how often waitPeers counts the meta leader's peers.
const peerPoll = 50 * time.Millisecond

// reservePorts picks the cluster's ports, n client ports then n route
// ports; a test replaces it.
var reservePorts = freePorts

// errBind: a node could not listen on its port. freePorts releases its
// ports before the nodes bind them, so a parallel test can take one in
// between (V8-FLAKE-4); RunCluster then tries fresh ports.
var errBind = errors.New("natstest: a port was taken before the node bound it")

// RunCluster starts n JetStream servers routed to each other, waits until
// the cluster has a meta leader, and shuts them down with the test. A
// node whose port was taken meanwhile (errBind) makes it start the whole
// cluster again on fresh ports, at most clusterAttempts times.
func RunCluster(t *testing.T, n int) *Cluster {
	t.Helper()
	for attempt := 1; ; attempt++ {
		c, err := runCluster(t, n)
		if err == nil {
			return c
		}
		if !errors.Is(err, errBind) || attempt == clusterAttempts {
			t.Fatalf("%v", err)
		}
		t.Logf("%v; starting the cluster again on fresh ports (attempt %d of %d)", err, attempt+1, clusterAttempts)
	}
}

func runCluster(t *testing.T, n int) (*Cluster, error) {
	t.Helper()
	ports := reservePorts(t, 2*n)
	clients, ports := ports[:n], ports[n:]
	routes := make([]*url.URL, n)
	for i, p := range ports {
		routes[i] = &url.URL{Scheme: "nats", Host: fmt.Sprintf("127.0.0.1:%d", p)}
	}
	c := &Cluster{t: t}
	// Every store is made before the first node starts and Shutdown is
	// registered after them, so the nodes are stopped before any store is
	// removed, a setup that fails partway included (V8-WIN-4).
	dirs := make([]string, n)
	for i := range dirs {
		dirs[i] = storeDir(t)
	}
	t.Cleanup(c.Shutdown)
	for i := range n {
		o := &server.Options{
			ServerName: fmt.Sprintf("n%d", i+1),
			Host:       "127.0.0.1",
			Port:       clients[i],
			JetStream:  true,
			StoreDir:   dirs[i],
			NoLog:      true,
			NoSigs:     true,
			Cluster:    server.ClusterOpts{Name: "ape-test", Host: "127.0.0.1", Port: ports[i]},
			Routes:     routes,
		}
		c.opts = append(c.opts, o)
		s, err := c.startNode(o)
		if err != nil {
			c.Shutdown()
			return nil, err
		}
		// Restart comes back on the same ports, as a real node does, so
		// a client's URLs still reach it after an outage.
		c.Servers = append(c.Servers, s)
	}
	c.WaitMetaLeader(ReadyWait)
	c.waitPeers(n, ReadyWait)
	return c, nil
}

// waitPeers waits until the meta leader counts n peers online with
// JetStream, the set an R3 placement draws from: a meta leader can be
// elected before every peer has reported, and an R3 create then failed
// with 10005 "no suitable peers for placement" (Windows, V8-WIN-3 and
// CI-5GREEN-2 on 0e50409).
func (c *Cluster) waitPeers(n int, d time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(d)
	for {
		most := 0
		for _, s := range c.Servers {
			if s != nil && s.Running() {
				most = max(most, len(s.JetStreamClusterPeers()))
			}
		}
		if most >= n {
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("natstest: the meta leader counts %d of %d peers after %v", most, n, d)
		}
		time.Sleep(peerPoll)
	}
}

func (c *Cluster) start(o *server.Options) *server.Server {
	c.t.Helper()
	s, err := c.startNode(o)
	if err != nil {
		c.t.Fatalf("%v", err)
	}
	return s
}

// startNode starts one node and waits until it accepts connections, at
// most ReadyWait. The node logs into a fatalLog, so a port it could not
// bind (errBind) ends the wait at once, with the server's own line,
// rather than after ReadyWait as "not ready".
func (c *Cluster) startNode(o *server.Options) (*server.Server, error) {
	s, err := server.NewServer(o)
	if err != nil {
		return nil, fmt.Errorf("natstest: new cluster server %s: %w", o.ServerName, err)
	}
	fl := &fatalLog{}
	s.SetLogger(fl, false, false)
	go s.Start()
	deadline := time.Now().Add(ReadyWait)
	for !s.ReadyForConnections(readyPoll) {
		if line := fl.first(); line != "" {
			stop(s)
			if strings.Contains(line, "Error listening on") {
				return nil, fmt.Errorf("%w: cluster server %s: %s", errBind, o.ServerName, line)
			}
			return nil, fmt.Errorf("natstest: cluster server %s: %s", o.ServerName, line)
		}
		if time.Now().After(deadline) {
			stop(s)
			return nil, fmt.Errorf("natstest: cluster server %s not ready after %v", o.ServerName, ReadyWait)
		}
	}
	return s, nil
}

// fatalLog keeps a server's fatal lines (the rest is dropped, as NoLog
// did): a server that cannot listen says so only there.
type fatalLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *fatalLog) first() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return ""
	}
	return l.lines[0]
}

func (l *fatalLog) Fatalf(format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, v...))
}

func (*fatalLog) Noticef(string, ...any) {}
func (*fatalLog) Warnf(string, ...any)   {}
func (*fatalLog) Errorf(string, ...any)  {}
func (*fatalLog) Debugf(string, ...any)  {}
func (*fatalLog) Tracef(string, ...any)  {}

// URLs are the servers' client URLs, comma-joined as nats.Connect takes them.
func (c *Cluster) URLs() string {
	var us []string
	for _, s := range c.Servers {
		if s != nil && s.Running() {
			us = append(us, s.ClientURL())
		}
	}
	return strings.Join(us, ",")
}

// Stop shuts server i down, keeping its store, so Restart brings it back
// with its data.
func (c *Cluster) Stop(i int) {
	stop(c.Servers[i])
}

// Restart starts server i again on its old options and store.
func (c *Cluster) Restart(i int) {
	c.t.Helper()
	o := c.opts[i].Clone()
	c.Servers[i] = c.start(o)
}

// Shutdown stops every server and waits until each has closed its store:
// on Windows an open file cannot be removed, so the stores' removal would
// otherwise fail on a file still in use.
func (c *Cluster) Shutdown() {
	for _, s := range c.Servers {
		if s != nil {
			s.Shutdown()
		}
	}
	for _, s := range c.Servers {
		if s != nil {
			s.WaitForShutdown()
		}
	}
}

// WaitMetaLeader waits until the cluster's JetStream answers an account
// request, which needs a meta leader.
func (c *Cluster) WaitMetaLeader(d time.Duration) {
	c.t.Helper()
	nc, err := nats.Connect(c.URLs())
	if err != nil {
		c.t.Fatalf("natstest: connect to cluster: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		c.t.Fatalf("natstest: jetstream: %v", err)
	}
	deadline := time.Now().Add(d)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := js.AccountInfo(ctx)
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("natstest: no JetStream meta leader after %v: %v", d, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// freePorts picks n loopback ports below the ephemeral range, each one
// listened on once to see it is free (V15-RED-4). A port the kernel hands
// out (":0") is an ephemeral one, and while its node is down for an
// outage drill any outgoing connection can be given it, so Restart failed
// to bind (36457 on a ci-local). Ports below the range are only ever
// taken by a process that names them. Ports are released before the
// nodes bind them, so another test can still take one in between: that
// is errBind, and RunCluster tries again (V8-FLAKE-4).
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	lo, hi := portFloor, ephemeralLow()
	if hi-lo < 1000 {
		t.Fatalf("natstest: the ephemeral range starts at %d, leaving no ports below it above %d", hi, lo)
	}
	ports := make([]int, 0, n)
	var lc net.ListenConfig
	for tries := 0; len(ports) < n; tries++ {
		if tries == 100*n {
			t.Fatalf("natstest: no free port in %d-%d after %d tries", lo, hi-1, tries)
		}
		p := lo + rand.IntN(hi-lo) //nolint:gosec // a port, not a secret
		if !claimPort(p) {
			continue
		}
		l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			continue // taken, or excluded (Windows reserves ranges for Hyper-V)
		}
		_ = l.Close()
		ports = append(ports, p)
	}
	return ports
}

// portFloor is the lowest port freePorts picks: above the registered
// services a developer machine runs.
const portFloor = 20000

// ephemeralLow is where the kernel's ephemeral ports start: Linux's
// ip_local_port_range, else 32768, below Windows' and macOS' 49152.
func ephemeralLow() int {
	const below = 32768
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return below
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return below
	}
	low, err := strconv.Atoi(f[0])
	if err != nil {
		return below
	}
	return min(low, below)
}

// claimed are the ports this process has handed out, never handed out
// twice: a stopped node's ports are free while it is down, and a parallel
// test must not pick them then.
var (
	claimedMu sync.Mutex
	claimed   = map[int]bool{}
)

func claimPort(p int) bool {
	claimedMu.Lock()
	defer claimedMu.Unlock()
	if claimed[p] {
		return false
	}
	claimed[p] = true
	return true
}
