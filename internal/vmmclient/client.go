// Package vmmclient is the client half of the ape.vmm contract (PLAN-18 D2/D3):
// a workspace.Backend implemented as NATS request/reply against an aped node, so
// the `ape` CLI (and a future controller) drive remote Kata workspaces with the
// same interface as a local driver.
//
// It depends only on the pure workspace contract + the nats.go client — never
// on nats-server or internal/aped — so linking it into `ape` keeps the CLI
// dependency-light (LOCKED 8). aped is the server; this is the client.
package vmmclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// DefaultTimeout bounds a vmm request/reply. Create provisions a VM, so the
// default is generous; callers can shorten it for read verbs.
const DefaultTimeout = 90 * time.Second

// Client is a workspace.Backend that speaks the ape.vmm.<node>.> contract over
// NATS.
type Client struct {
	nc      *nats.Conn
	base    string
	timeout time.Duration
}

// New builds a Client targeting ape.vmm.<node>.> on nc. A zero timeout uses
// DefaultTimeout.
func New(nc *nats.Conn, node string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{nc: nc, base: subjectRootVMM + "." + node, timeout: timeout}
}

const subjectRootVMM = "ape.vmm"

var _ workspace.Backend = (*Client)(nil)

// call marshals req, does the request/reply, maps a micro req.Error to the
// matching workspace sentinel, and unmarshals the reply into out (out may be
// nil for ack-only verbs).
func (c *Client) call(verb string, req, out any) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("vmm %s: marshal request: %w", verb, err)
	}
	msg, err := c.nc.Request(c.base+"."+verb, data, c.timeout)
	if err != nil {
		return fmt.Errorf("vmm %s: %w", verb, err)
	}
	if code := msg.Header.Get(micro.ErrorCodeHeader); code != "" {
		return workspace.ErrorForCode(code, msg.Header.Get(micro.ErrorHeader))
	}
	if out != nil {
		if err := json.Unmarshal(msg.Data, out); err != nil {
			return fmt.Errorf("vmm %s: decode reply: %w", verb, err)
		}
	}
	return nil
}

func (c *Client) Capabilities(context.Context) (workspace.Capabilities, error) {
	var r workspace.CapabilitiesReply
	err := c.call("capabilities", struct{}{}, &r)
	return r.Capabilities, err
}

func (c *Client) Create(_ context.Context, req workspace.CreateRequest) (workspace.Workspace, error) {
	req.V = workspace.WireVersion
	var r workspace.CreateReply
	err := c.call("create", req, &r)
	return r.Workspace, err
}

func (c *Client) Start(_ context.Context, id string) error {
	return c.call("start", workspace.IDRequest{V: workspace.WireVersion, ID: id}, nil)
}

func (c *Client) Stop(_ context.Context, id string) error {
	return c.call("stop", workspace.IDRequest{V: workspace.WireVersion, ID: id}, nil)
}

func (c *Client) Freeze(_ context.Context, id string) error {
	return c.call("freeze", workspace.IDRequest{V: workspace.WireVersion, ID: id}, nil)
}

func (c *Client) Unfreeze(_ context.Context, id string) error {
	return c.call("unfreeze", workspace.IDRequest{V: workspace.WireVersion, ID: id}, nil)
}

func (c *Client) Suspend(_ context.Context, id string) error {
	return c.call("suspend", workspace.IDRequest{V: workspace.WireVersion, ID: id}, nil)
}

func (c *Client) Resume(_ context.Context, id string) error {
	return c.call("resume", workspace.IDRequest{V: workspace.WireVersion, ID: id}, nil)
}

func (c *Client) Destroy(_ context.Context, id string, req workspace.DestroyRequest) error {
	return c.call("destroy", workspace.DestroyReq{V: workspace.WireVersion, ID: id, DestroyRequest: req}, nil)
}

func (c *Client) Exec(_ context.Context, id string, req workspace.ExecRequest) (workspace.ExitStatus, error) {
	var r workspace.ExecReply
	err := c.call("exec", workspace.ExecReq{V: workspace.WireVersion, ID: id, ExecRequest: req}, &r)
	return r.ExitStatus, err
}

func (c *Client) Snapshot(_ context.Context, id string, req workspace.SnapshotRequest) (workspace.SnapshotRef, error) {
	var r workspace.SnapshotReply
	err := c.call("snapshot", workspace.SnapshotReq{V: workspace.WireVersion, ID: id, SnapshotRequest: req}, &r)
	return r.SnapshotRef, err
}

func (c *Client) List(context.Context) ([]workspace.Workspace, error) {
	var r workspace.ListReply
	err := c.call("list", struct{}{}, &r)
	return r.Workspaces, err
}

func (c *Client) Inspect(_ context.Context, id string) (workspace.Status, error) {
	var r workspace.InspectReply
	err := c.call("inspect", workspace.IDRequest{V: workspace.WireVersion, ID: id}, &r)
	return r.Status, err
}

// AttachOpen opens an interactive session and returns the id + subject prefix
// the caller streams over. The full stdio streaming (session subjects → PTY) is
// wired under Tier-2; Attach itself is unsupported until then.
func (c *Client) AttachOpen(_ context.Context, id string, req workspace.AttachRequest) (workspace.AttachOpenReply, error) {
	var r workspace.AttachOpenReply
	err := c.call("attach.open", workspace.AttachOpenReq{V: workspace.WireVersion, ID: id, AttachRequest: req}, &r)
	return r, err
}

// Attach's bulk stdio streaming over NATS is a Tier-2 addition; use ssh/exec for
// interactive access in Phase 2.
func (c *Client) Attach(context.Context, string, workspace.AttachRequest, workspace.Stream) (workspace.ExitStatus, error) {
	return workspace.ExitStatus{}, workspace.ErrUnsupported
}

func (c *Client) Logs(context.Context, string, workspace.LogsRequest) (io.ReadCloser, error) {
	return nil, workspace.ErrUnsupported
}

func (c *Client) Events(context.Context) (<-chan workspace.Event, error) {
	return nil, workspace.ErrUnsupported
}
