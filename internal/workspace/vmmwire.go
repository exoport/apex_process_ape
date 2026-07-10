package workspace

// The request/reply envelopes below are the ape.vmm NATS request/reply bodies
// (docs/reference/events.md). One endpoint per Backend verb; the id-only verbs
// share IDRequest, and the verbs that carry options embed the matching option
// struct so a body is {v, id, …options}. Every reply stamps "v". Field names
// are snake_case — the stable, additive-only wire contract. A rejection is a
// micro req.Error (a Code* string), never one of these reply shapes.

// IDRequest is the body of the id-only verbs: start, stop, freeze, unfreeze,
// suspend, resume, inspect.
type IDRequest struct {
	V  int    `json:"v,omitempty"`
	ID string `json:"id"`
}

// CreateReply wraps the durable Workspace record returned by create.
type CreateReply struct {
	V int `json:"v"`
	Workspace
}

// OKReply is the ack for the verbs that return no body (start/stop/freeze/
// unfreeze/suspend/resume/destroy).
type OKReply struct {
	V  int  `json:"v"`
	OK bool `json:"ok"`
}

// DestroyReq is the body of destroy: an id plus teardown options.
type DestroyReq struct {
	V  int    `json:"v,omitempty"`
	ID string `json:"id"`
	DestroyRequest
}

// ExecReq is the body of exec: an id plus the one-shot command.
type ExecReq struct {
	V  int    `json:"v,omitempty"`
	ID string `json:"id"`
	ExecRequest
}

// ExecReply carries the exec exit status.
type ExecReply struct {
	V int `json:"v"`
	ExitStatus
}

// AttachOpenReq is the body of attach.open: an id plus the attach options.
type AttachOpenReq struct {
	V  int    `json:"v,omitempty"`
	ID string `json:"id"`
	AttachRequest
}

// AttachOpenReply returns the interactive-session id and the subject prefix the
// client then streams over: ape.vmm.<node>.exec.<sid>.{stdin,stdout,stderr,
// resize,control,exit} (PLAN-18 D2). Bulk stdio rides those session subjects
// with credit-based flow control, never request/reply.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type AttachOpenReply struct {
	V             int    `json:"v"`
	SessionID     string `json:"session_id"`
	SubjectPrefix string `json:"subject_prefix"`
}

// SnapshotReq is the body of snapshot: an id plus the snapshot name.
type SnapshotReq struct {
	V  int    `json:"v,omitempty"`
	ID string `json:"id"`
	SnapshotRequest
}

// SnapshotReply wraps the taken snapshot ref.
type SnapshotReply struct {
	V int `json:"v"`
	SnapshotRef
}

// ListReply is the list response.
type ListReply struct {
	V          int         `json:"v"`
	Workspaces []Workspace `json:"workspaces"`
}

// InspectReply wraps a workspace's live status.
type InspectReply struct {
	V int `json:"v"`
	Status
}

// CapabilitiesReply wraps the node's capabilities.
type CapabilitiesReply struct {
	V int `json:"v"`
	Capabilities
}
