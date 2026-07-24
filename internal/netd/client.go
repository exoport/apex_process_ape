package netd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"time"
)

// DefaultTimeout bounds one round-trip. Wiring a veth pair is a few milliseconds
// of netlink; anything near this bound means the helper is wedged, and a create
// must fail closed rather than hang.
const DefaultTimeout = 10 * time.Second

// Client is the executor's side of the netd protocol: one AF_UNIX round-trip per
// call, no persistent connection (the helper is stateless per request; its lease
// file is the only state).
type Client struct {
	// Socket is the helper's AF_UNIX path; "" → DefaultSocket.
	Socket string
	// Timeout bounds one round-trip; 0 → DefaultTimeout.
	Timeout time.Duration
	// Bridge/HostCIDR override the helper's defaults on every request (empty →
	// the helper's own configuration).
	Bridge   string
	HostCIDR string
}

func (c *Client) socket() string {
	if c.Socket == "" {
		return DefaultSocket
	}
	return c.Socket
}

func (c *Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

// EnsureNetns wires the workspace's egress link and returns the netns path the
// OCI spec should reference. reuse=true returns an existing link untouched.
func (c *Client) EnsureNetns(ctx context.Context, workspace string, proxyPort int, reuse bool) (string, error) {
	resp, err := c.Do(ctx, Request{
		Op: OpEnsure, Workspace: workspace, ProxyPort: proxyPort,
		Bridge: c.Bridge, HostCIDR: c.HostCIDR, Reuse: reuse,
	})
	if err != nil {
		return "", err
	}
	if resp.NetnsPath == "" {
		return "", fmt.Errorf("netd: helper returned no netns path for %q", workspace)
	}
	return resp.NetnsPath, nil
}

// DeleteNetns removes the workspace's egress link. A link that is already gone is
// not an error — teardown must be idempotent so Destroy can always complete.
func (c *Client) DeleteNetns(ctx context.Context, workspace string) error {
	_, err := c.Do(ctx, Request{Op: OpDelete, Workspace: workspace})
	return err
}

// Ping reports whether the helper is reachable (used by `ape doctor`).
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Do(ctx, Request{Op: OpPing})
	return err
}

// Do sends one request and returns the decoded response, mapping a helper-side
// error into a Go error.
func (c *Client) Do(ctx context.Context, req Request) (Response, error) {
	if err := req.Validate(); err != nil {
		return Response{}, err
	}
	frame, err := EncodeRequest(req)
	if err != nil {
		return Response{}, err
	}

	d := net.Dialer{Timeout: c.timeout()}
	conn, err := d.DialContext(ctx, "unix", c.socket())
	if err != nil {
		return Response{}, fmt.Errorf("netd: dial %s: %w (is aped-netd.service running?)", c.socket(), err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(c.timeout()))
	}

	if _, err := conn.Write(frame); err != nil {
		return Response{}, fmt.Errorf("netd: write request: %w", err)
	}
	line, err := bufio.NewReaderSize(io.LimitReader(conn, MaxFrame), MaxFrame).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Response{}, fmt.Errorf("netd: read response: %w", err)
	}
	resp, derr := DecodeResponse(line)
	if derr != nil {
		return Response{}, derr
	}
	if rerr := resp.Err(); rerr != nil {
		return resp, rerr
	}
	return resp, nil
}
