package apexv1

import (
	"bufio"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The sampler behind Observed (observe.go): every observeFast for a check's
// first observeFastFor, then every observeSlow, and once more when its
// shell has exited, before what it left running is ended. A sample reads
// the check's processes (its scope, else its process group), their command
// lines, their TCP sockets (/proc/<pid>/fd against /proc/net/tcp{,6}), and
// costs a few /proc reads.
var (
	observeFast    = 250 * time.Millisecond
	observeFastFor = 30 * time.Second
	observeSlow    = 2 * time.Second
)

// observer samples one running check.
type observer struct {
	g    *checkGroup
	stop chan struct{}
	done chan struct{}

	mu       sync.Mutex
	samples  int
	argv     map[int][]string
	listens  map[string]listenSeen // "<pid> <port>"
	conns    map[string]connSeen   // "<local port> <remote ip> <remote port>"
	ownPorts map[int]bool          // ports a process of the check listened on
}

type listenSeen struct {
	pid, port int
}

type connSeen struct {
	localPort, remotePort int
	remote                net.IP
}

// startObserver starts sampling g, which has started.
func startObserver(g *checkGroup) *observer {
	o := &observer{
		g: g, stop: make(chan struct{}), done: make(chan struct{}),
		argv: map[int][]string{}, listens: map[string]listenSeen{}, conns: map[string]connSeen{}, ownPorts: map[int]bool{},
	}
	go o.run()
	return o
}

func (o *observer) run() {
	defer close(o.done)
	start := time.Now()
	t := time.NewTimer(observeFast)
	defer t.Stop()
	for {
		select {
		case <-o.stop:
			return
		case <-t.C:
		}
		o.sample()
		next := observeFast
		if time.Since(start) > observeFastFor {
			next = observeSlow
		}
		t.Reset(next)
	}
}

// finish stops the sampling, samples once more and returns what was seen.
func (o *observer) finish() *Observed {
	if o == nil {
		return nil
	}
	close(o.stop)
	<-o.done
	o.sample()
	return o.result()
}

// pids are the check's processes now.
func (o *observer) pids() []int {
	if o.g.unit != "" {
		return scopePIDs(o.g.dir)
	}
	members := groupMembers(o.g.pid)
	out := make([]int, 0, len(members))
	for _, m := range members {
		out = append(out, m.pid)
	}
	return out
}

func (o *observer) sample() {
	pids := o.pids()
	socks := tcpSockets()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.samples++
	for _, pid := range pids {
		if _, ok := o.argv[pid]; !ok {
			if argv := pidArgv(pid); len(argv) > 0 && argv[0] != "" {
				o.argv[pid] = argv
			}
		}
		for _, inode := range socketInodes(pid) {
			s, ok := socks[inode]
			if !ok {
				continue
			}
			switch s.state {
			case tcpListen:
				o.listens[strconv.Itoa(pid)+" "+strconv.Itoa(s.localPort)] = listenSeen{pid: pid, port: s.localPort}
				o.ownPorts[s.localPort] = true
			case tcpEstablished:
				key := strconv.Itoa(s.localPort) + " " + s.remote.String() + " " + strconv.Itoa(s.remotePort)
				o.conns[key] = connSeen{localPort: s.localPort, remote: s.remote, remotePort: s.remotePort}
			}
		}
	}
}

// result is what the samples saw.
func (o *observer) result() *Observed {
	o.mu.Lock()
	defer o.mu.Unlock()
	res := &Observed{Samples: o.samples}
	pids := make([]int, 0, len(o.argv))
	for pid := range o.argv {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	for _, pid := range pids {
		argv := o.argv[pid]
		if r := testRunner(argv); r != "" && (res.Runner == "" || res.Runner == "go" && r != "go") {
			res.Runner = r
		}
		if isBrowser(argv) {
			res.Browser = true
		}
	}
	keys := make([]string, 0, len(o.listens))
	for k := range o.listens {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		l := o.listens[k]
		argv := o.argv[l.pid]
		if testRunner(argv) != "" {
			res.InProcessListeners++
			continue
		}
		cmd := strings.Join(argv, " ")
		if cmd == "" {
			cmd = "?"
		}
		if r := []rune(cmd); len(r) > holderWidth {
			cmd = string(r[:holderWidth]) + "…"
		}
		res.Services = append(res.Services, strconv.Itoa(l.pid)+" "+cmd+" :"+strconv.Itoa(l.port))
	}
	outside, remote := map[string]bool{}, map[string]bool{}
	for _, c := range o.conns {
		switch {
		case o.ownPorts[c.localPort], o.ownPorts[c.remotePort] && c.remote.IsLoopback():
			// The accepted side of a check's own listener, or a
			// connection to one.
		case c.remote.IsLoopback():
			outside[net.JoinHostPort(c.remote.String(), strconv.Itoa(c.remotePort))] = true
		case !c.remote.IsUnspecified():
			remote[net.JoinHostPort(c.remote.String(), strconv.Itoa(c.remotePort))] = true
		}
	}
	if len(outside) > 0 {
		res.Outside = sortedKeys(outside)
	}
	if len(remote) > 0 {
		res.Remote = sortedKeys(remote)
	}
	return res
}

// socketInodes are the socket inodes pid holds open.
func socketInodes(pid int) []string {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil || !strings.HasPrefix(link, "socket:[") {
			continue
		}
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]"))
	}
	return out
}

// The /proc/net/tcp states read.
const (
	tcpEstablished = "01"
	tcpListen      = "0A"
)

type tcpSocket struct {
	state                 string
	localPort, remotePort int
	remote                net.IP
}

// tcpSockets are this network namespace's TCP sockets by inode.
func tcpSockets() map[string]tcpSocket {
	out := map[string]tcpSocket{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		file, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(file)
		for sc.Scan() {
			if s, inode, ok := parseTCPLine(sc.Text()); ok {
				out[inode] = s
			}
		}
		_ = file.Close()
	}
	return out
}

// parseTCPLine reads one /proc/net/tcp{,6} line: "sl local rem st … inode".
func parseTCPLine(line string) (tcpSocket, string, bool) {
	f := strings.Fields(line)
	if len(f) < 10 || (f[3] != tcpListen && f[3] != tcpEstablished) {
		return tcpSocket{}, "", false
	}
	_, lport, ok1 := parseHexAddr(f[1])
	rip, rport, ok2 := parseHexAddr(f[2])
	if !ok1 || !ok2 {
		return tcpSocket{}, "", false
	}
	return tcpSocket{state: f[3], localPort: lport, remotePort: rport, remote: rip}, f[9], true
}

// parseHexAddr reads "0100007F:1F90": an address /proc writes as 32-bit
// words in host (little-endian) order, then a port in hex.
func parseHexAddr(s string) (net.IP, int, bool) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return nil, 0, false
	}
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != net.IPv4len && len(raw) != net.IPv6len) {
		return nil, 0, false
	}
	ip := make(net.IP, len(raw))
	for w := 0; w < len(raw); w += 4 {
		ip[w], ip[w+1], ip[w+2], ip[w+3] = raw[w+3], raw[w+2], raw[w+1], raw[w]
	}
	return ip, int(port), true
}
