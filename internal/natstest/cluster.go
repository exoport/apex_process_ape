package natstest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
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

// reservePorts picks the route ports; a test replaces it.
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
	ports := reservePorts(t, n)
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
			Port:       -1,
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
		c.Servers = append(c.Servers, s)
		// Restart comes back on the same client port, as a real node
		// does, so a client's URLs still reach it after an outage.
		if a, ok := c.Servers[i].Addr().(*net.TCPAddr); ok {
			o.Port = a.Port
		}
	}
	c.WaitMetaLeader(ReadyWait)
	return c, nil
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

// freePorts reserves n loopback ports for the cluster routes.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	ls := make([]net.Listener, 0, n)
	ports := make([]int, 0, n)
	var lc net.ListenConfig
	for range n {
		l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("natstest: reserve port: %v", err)
		}
		ls = append(ls, l)
		addr, _ := l.Addr().(*net.TCPAddr)
		ports = append(ports, addr.Port)
	}
	for _, l := range ls {
		_ = l.Close()
	}
	return ports
}
