//go:build linux

package netd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/exoport/apex_process_ape/internal/sandbox"
)

// socketMode is the helper socket's permission: root only. The single legitimate
// peer is aped's root executor, so — unlike the priv socket, which the
// de-privileged front must reach — there is no group to widen this to.
const socketMode = 0o600

// socketDirMode matches tmpfiles.d's /run/aped (0710 root:ape): the front only
// traverses it, and the helper's own socket inside is root-only.
const socketDirMode = 0o710

// Serve binds the helper socket and serves requests until ctx is cancelled. It is
// the body of `aped netd`.
func Serve(ctx context.Context, cfg ServerConfig) error {
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	sock := cfg.Socket
	if sock == "" {
		sock = DefaultSocket
	}
	leases, err := OpenLeases(cfg.LeaseFile)
	if err != nil {
		return err
	}
	allowed := map[uint32]bool{}
	for _, uid := range cfg.AllowedUIDs {
		allowed[uid] = true
	}
	if len(allowed) == 0 {
		allowed[0] = true
	}

	if err := os.MkdirAll(filepath.Dir(sock), socketDirMode); err != nil {
		return fmt.Errorf("netd: mkdir socket dir: %w", err)
	}
	// A stale socket file from an unclean stop would make bind fail with EADDRINUSE;
	// removing it is safe because only one helper instance may own the path (the
	// unit is not templated).
	if err := os.Remove(sock); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("netd: remove stale socket: %w", err)
	}
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "unix", sock)
	if err != nil {
		return fmt.Errorf("netd: listen %s: %w", sock, err)
	}
	defer func() { _ = l.Close() }()
	if err := os.Chmod(sock, socketMode); err != nil {
		return fmt.Errorf("netd: chmod socket: %w", err)
	}

	s := &server{cfg: cfg, leases: leases, allowed: allowed, stderr: stderr}
	fmt.Fprintf(stderr, "▶ aped netd — %s (bridge %s %s, proxy ports %d-%d, %d lease(s))\n",
		sock, s.bridge(), s.hostCIDR(), s.portLow(), s.portHigh(), len(leases.All()))
	warnIfNetnsNotShared(stderr)

	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("netd: accept: %w", err)
		}
		go s.handle(ctx, conn)
	}
}

// warnIfNetnsNotShared checks, at startup, that /run/netns is a SHARED mount this
// helper's namespace has in common with the host — the single prerequisite that
// makes its work visible to containerd.
//
// Why it earns a dedicated check: `ip netns add` bind-mounts the namespace over a
// placeholder file, so that bind must be visible to containerd. Two ways it is not:
// /run/netns is not a shared mount at all (aped-netbr.service establishes it), or
// this unit was given a private mount namespace, which systemd makes a SLAVE of the
// host — propagation then runs host→unit only. In both cases everything here SUCCEEDS — the netns is real,
// the veth is wired, the reply carries a path — and the failure surfaces much later
// as the Kata shim reporting "failed to set into network namespace N while creating
// netlink socket: invalid argument", because containerd opened the placeholder file
// instead of a namespace. One log line at startup is worth more than a debugging
// session at create time.
//
// It only warns: the check is advisory (the operator may be mid-setup), and a
// helper that refused to start would be harder to recover from than one that says
// what is wrong.
func warnIfNetnsNotShared(stderr io.Writer) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return // cannot tell; stay quiet rather than cry wolf
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		// mountinfo: <id> <parent> <maj:min> <root> <mountpoint> <opts> <optional...> - ...
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[4] != sandbox.NetnsRunDir {
			continue
		}
		switch {
		case strings.Contains(line, " master:"):
			// The mount is a SLAVE of the host's peer group: propagation is host→here
			// only, so a netns bind made here cannot reach containerd. This is what
			// systemd gives any unit with a private mount namespace, even with
			// MountFlags=shared — so the fix is to remove the namespacing options, not
			// to remount anything.
			fmt.Fprintf(stderr, "! netd: %s is SLAVE to the host mount namespace (%s) — netns binds made here "+
				"cannot reach containerd, and the Kata shim will fail with \"failed to set into network "+
				"namespace\". This unit must run in the HOST mount namespace: remove ProtectHome=/ProtectProc=/"+
				"ProtectSystem=/PrivateTmp= and friends from aped-netd.service.\n",
				sandbox.NetnsRunDir, strings.TrimSpace(masterTag(line)))
		case strings.Contains(line, " shared:"):
			return // correctly set up: shared with the host, no master
		default:
			fmt.Fprintf(stderr, "! netd: %s is a mount but NOT shared — netns binds will not reach containerd; "+
				"run `mount --make-shared %s` (aped-netbr.service does this)\n", sandbox.NetnsRunDir, sandbox.NetnsRunDir)
		}
		return
	}
	fmt.Fprintf(stderr, "! netd: %s is not a mountpoint in this namespace — netns binds will stay private to this "+
		"process and the Kata shim will fail with \"failed to set into network namespace\". Start "+
		"aped-netbr.service (it bind-mounts %s onto itself and marks it shared) and restart this unit.\n",
		sandbox.NetnsRunDir, sandbox.NetnsRunDir)
}

// masterTag extracts the "master:N" field from a mountinfo line, for the diagnostic.
func masterTag(line string) string {
	for f := range strings.FieldsSeq(line) {
		if strings.HasPrefix(f, "master:") {
			return f
		}
	}
	return "master:?"
}

// server holds the helper's small mutable state: the lease set, serialized by a
// mutex because two concurrent creates must never be handed the same address.
type server struct {
	cfg     ServerConfig
	leases  *Leases
	allowed map[uint32]bool
	stderr  io.Writer
	mu      sync.Mutex
}

func (s *server) bridge() string {
	if s.cfg.Bridge != "" {
		return s.cfg.Bridge
	}
	return sandbox.DefaultEgressBridge
}

func (s *server) hostCIDR() string {
	if s.cfg.HostCIDR != "" {
		return s.cfg.HostCIDR
	}
	return sandbox.DefaultEgressHostCIDR
}

func (s *server) portLow() int {
	if s.cfg.PortLow > 0 {
		return s.cfg.PortLow
	}
	return sandbox.DefaultEgressPortLow
}

func (s *server) portHigh() int {
	if s.cfg.PortHigh > 0 {
		return s.cfg.PortHigh
	}
	return sandbox.DefaultEgressPortHigh
}

func (s *server) bin(configured, def string) string {
	if strings.TrimSpace(configured) != "" {
		return configured
	}
	return def
}

// handle serves one connection: peer-uid gate → one request → one response.
func (s *server) handle(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	uid, err := peerUID(conn)
	if err != nil {
		return // cannot attest the peer → drop silently
	}
	// Read the request before answering, so even a rejected peer's frame is drained
	// (an unread stream socket can reset and clobber the reply).
	line, rerr := bufio.NewReaderSize(io.LimitReader(conn, MaxFrame), MaxFrame).ReadBytes('\n')
	if !s.allowed[uid] {
		fmt.Fprintf(s.stderr, "✗ netd: rejected peer uid %d\n", uid)
		s.reply(conn, Response{Error: "peer not authorized"})
		return
	}
	if rerr != nil && len(line) == 0 {
		return
	}
	req, err := DecodeRequest(line)
	if err != nil {
		s.reply(conn, Response{Error: err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		s.reply(conn, Response{Error: err.Error()})
		return
	}

	s.mu.Lock()
	resp := s.dispatch(ctx, req)
	s.mu.Unlock()
	s.reply(conn, resp)
}

func (s *server) reply(conn net.Conn, resp Response) {
	frame, err := EncodeResponse(resp)
	if err != nil {
		frame = []byte(`{"error":"encode failure"}` + "\n")
	}
	_, _ = conn.Write(frame)
}

// dispatch runs one validated request.
func (s *server) dispatch(ctx context.Context, req Request) Response {
	switch req.Op {
	case OpPing:
		return Response{}
	case OpEnsure:
		return s.ensure(ctx, req)
	case OpDelete:
		return s.delete(ctx, req)
	default:
		return Response{Error: fmt.Sprintf("unknown op %q", req.Op)}
	}
}

// ensure wires (or re-wires) the link and returns its netns path.
func (s *server) ensure(ctx context.Context, req Request) Response {
	bridge, hostCIDR := s.bridge(), s.hostCIDR()
	if req.Bridge != "" {
		bridge = req.Bridge
	}
	if req.HostCIDR != "" {
		hostCIDR = req.HostCIDR
	}

	ip, err := sandbox.AllocateGuestIP(hostCIDR, s.leases.All(), req.Workspace)
	if err != nil {
		return Response{Error: err.Error()}
	}
	link, err := sandbox.PlanEgressLink(sandbox.EgressLinkOptions{
		Workspace: req.Workspace, Bridge: bridge, HostCIDR: hostCIDR,
		GuestIP: ip, ProxyPort: req.ProxyPort,
		PortLow: s.portLow(), PortHigh: s.portHigh(),
	})
	if err != nil {
		return Response{Error: err.Error()}
	}

	// Reuse: a stopped workspace's container spec already names this path, so if
	// the netns is still there we must not rebuild it (that would detach the
	// running/stopped container from its network).
	if req.Reuse {
		if _, statErr := os.Stat(link.NetnsPath); statErr == nil {
			if perr := s.leases.Put(req.Workspace, ip); perr != nil {
				return Response{Error: perr.Error()}
			}
			return Response{NetnsPath: link.NetnsPath, GuestIP: ip, ProxyURL: link.ProxyURL()}
		}
	}

	// Fresh wire-up: clear any stale remnants first so a half-torn-down link from a
	// crashed create cannot make this one fail.
	s.teardown(ctx, link)
	for _, argv := range link.SetupCommands() {
		if err := s.run(ctx, argv, ""); err != nil {
			s.teardown(ctx, link) // never leave a partial link behind
			return Response{Error: err.Error()}
		}
	}
	nftArgv, ruleset := link.NftCommand()
	if err := s.run(ctx, nftArgv, ruleset); err != nil {
		// The in-netns ruleset is defence in depth (Kata's tcfilter mode bypasses
		// it); the load-bearing wall is the host nft chain + bridge port isolation,
		// which are already in place. Log and continue rather than failing the
		// create over a belt-and-braces layer.
		fmt.Fprintf(s.stderr, "! netd: in-netns ruleset for %s not applied: %v\n", req.Workspace, err)
	}
	if err := s.leases.Put(req.Workspace, ip); err != nil {
		return Response{Error: err.Error()}
	}
	fmt.Fprintf(s.stderr, "✓ netd: %s → %s (%s, proxy %s)\n", req.Workspace, link.NetnsPath, ip, link.ProxyURL())
	return Response{NetnsPath: link.NetnsPath, GuestIP: ip, ProxyURL: link.ProxyURL()}
}

// delete removes the link and its lease. Missing pieces are not an error.
func (s *server) delete(ctx context.Context, req Request) Response {
	ip, _ := s.leases.Get(req.Workspace)
	if ip == "" {
		// No lease: still attempt teardown with a placeholder address so a link left
		// behind by a crashed helper is cleaned up.
		ip = firstUsableAddr(s.hostCIDR())
	}
	link, err := sandbox.PlanEgressLink(sandbox.EgressLinkOptions{
		Workspace: req.Workspace, Bridge: s.bridge(), HostCIDR: s.hostCIDR(),
		GuestIP: ip, ProxyPort: s.portLow(),
		PortLow: s.portLow(), PortHigh: s.portHigh(),
	})
	if err != nil {
		return Response{Error: err.Error()}
	}
	s.teardown(ctx, link)
	if err := s.leases.Remove(req.Workspace); err != nil {
		return Response{Error: err.Error()}
	}
	fmt.Fprintf(s.stderr, "✓ netd: removed %s\n", req.Workspace)
	return Response{}
}

// teardown runs the removal commands, tolerating "already gone".
func (s *server) teardown(ctx context.Context, link sandbox.EgressLink) {
	for _, argv := range link.TeardownCommands() {
		if err := s.run(ctx, argv, ""); err != nil && !benignTeardownErr(err) {
			fmt.Fprintf(s.stderr, "! netd: teardown %v: %v\n", argv, err)
		}
	}
}

// run executes one argv (no shell), optionally feeding stdin, and folds stderr
// into the error so a failure is diagnosable from the journal.
func (s *server) run(ctx context.Context, argv []string, stdin string) error {
	if len(argv) == 0 {
		return errors.New("netd: empty command")
	}
	name := argv[0]
	switch name {
	case "ip":
		name = s.bin(s.cfg.IPBin, "ip")
	case "bridge":
		name = s.bin(s.cfg.BridgeBin, "bridge")
	case "nft":
		name = s.bin(s.cfg.NftBin, "nft")
	}
	// `ip netns exec … nft …` runs nft THROUGH ip, so the nft binary override has
	// to be applied to the nested argument too.
	args := append([]string(nil), argv[1:]...)
	for i, a := range args {
		if a == "nft" {
			args[i] = s.bin(s.cfg.NftBin, "nft")
		}
	}

	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("netd: %s %s: %s", name, strings.Join(args, " "), msg)
	}
	return nil
}

// benignTeardownErr matches the "it was already gone" shapes iproute2 reports, so
// an idempotent teardown does not spam the journal.
func benignTeardownErr(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"cannot find device", "no such file or directory", "does not exist", "cannot remove namespace file"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// firstUsableAddr returns the first host address in the CIDR's subnet, used only
// as a placeholder when tearing down a workspace with no recorded lease (the
// address is irrelevant to teardown, but PlanEgressLink validates one).
func firstUsableAddr(hostCIDR string) string {
	ip, err := sandbox.AllocateGuestIP(hostCIDR, nil, "")
	if err != nil {
		return ""
	}
	return ip
}

// peerUID reads the connecting process's SO_PEERCRED — kernel-attested, so the
// helper's uid gate cannot be spoofed by the caller (same idiom as the executor's
// priv socket).
func peerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("netd: not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("netd: syscall conn: %w", err)
	}
	var ucred *unix.Ucred
	var cerr error
	if ctlErr := raw.Control(func(fd uintptr) {
		ucred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); ctlErr != nil {
		return 0, fmt.Errorf("netd: peercred control: %w", ctlErr)
	}
	if cerr != nil {
		return 0, fmt.Errorf("netd: getsockopt SO_PEERCRED: %w", cerr)
	}
	return ucred.Uid, nil
}
