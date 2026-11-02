package natstest

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
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

// RunCluster starts n JetStream servers routed to each other, waits until
// the cluster has a meta leader, and shuts them down with the test.
func RunCluster(t *testing.T, n int) *Cluster {
	t.Helper()
	ports := freePorts(t, n)
	routes := make([]*url.URL, n)
	for i, p := range ports {
		routes[i] = &url.URL{Scheme: "nats", Host: fmt.Sprintf("127.0.0.1:%d", p)}
	}
	c := &Cluster{t: t}
	for i := range n {
		o := &server.Options{
			ServerName: fmt.Sprintf("n%d", i+1),
			Host:       "127.0.0.1",
			Port:       -1,
			JetStream:  true,
			StoreDir:   t.TempDir(),
			NoLog:      true,
			NoSigs:     true,
			Cluster:    server.ClusterOpts{Name: "ape-test", Host: "127.0.0.1", Port: ports[i]},
			Routes:     routes,
		}
		c.opts = append(c.opts, o)
		c.Servers = append(c.Servers, c.start(o))
		// Restart comes back on the same client port, as a real node
		// does, so a client's URLs still reach it after an outage.
		if a, ok := c.Servers[i].Addr().(*net.TCPAddr); ok {
			o.Port = a.Port
		}
	}
	t.Cleanup(c.Shutdown)
	c.WaitMetaLeader(30 * time.Second)
	return c
}

func (c *Cluster) start(o *server.Options) *server.Server {
	c.t.Helper()
	s, err := server.NewServer(o)
	if err != nil {
		c.t.Fatalf("natstest: new cluster server %s: %v", o.ServerName, err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		c.t.Fatalf("natstest: cluster server %s not ready", o.ServerName)
	}
	return s
}

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
	c.Servers[i].Shutdown()
	c.Servers[i].WaitForShutdown()
}

// Restart starts server i again on its old options and store.
func (c *Cluster) Restart(i int) {
	c.t.Helper()
	o := c.opts[i].Clone()
	c.Servers[i] = c.start(o)
}

// Shutdown stops every server.
func (c *Cluster) Shutdown() {
	for _, s := range c.Servers {
		if s != nil {
			s.Shutdown()
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
