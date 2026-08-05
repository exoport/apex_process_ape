package workspace

// The request/response types below double as the `ape.vmm` NATS wire contract
// (docs/reference/events.md). Their JSON field names are snake_case — the
// stable, documented, additive-only contract — which is why each carries a
// tagliatelle nolint (the repo's lint default is json:camel; see the analog in
// internal/service/request.go). Add fields, never rename or repurpose them.

// CreateRequest is the fully-resolved request to provision one workspace. It
// is deliberately thin: the composed home, egress proxy, ports, and env are
// resolved by the caller (client-side today; server-side in `aped`), not sent
// on the wire. Image "" means the backend's pinned default.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type CreateRequest struct {
	V       int    `json:"v,omitempty"`
	Name    string `json:"name"`
	Image   string `json:"image,omitempty"`
	Runtime string `json:"runtime,omitempty"` // kata-qemu | kata-clh | firecracker
	Mount   string `json:"mount,omitempty"`   // host-fs | volume | ephemeral
	// MountSource is the canonical host path to mount when Mount is "host-fs".
	// It is the one caller-context path on the wire; aped canonicalizes it and
	// re-checks it against the policy mount-root allow-list before binding it
	// (never trusting the raw path). Empty for volume/ephemeral mounts.
	MountSource string   `json:"mount_source,omitempty"`
	Profile     string   `json:"profile,omitempty"`
	Devices     []Device `json:"devices,omitempty"`
	From        string   `json:"from,omitempty"` // Kata factory template (Kata tier only)
	// Egress is the workspace's REQUESTED network egress (PLAN-21). It is a
	// request, never a grant: aped intersects it with the node's egress policy, so
	// a project can narrow what policy permits but never widen it. Nil/empty means
	// no egress — the workspace stays networkless.
	Egress *EgressRequest `json:"egress,omitempty"`
	// Repos are the project repositories to mount, each at /workspace/<name>, with
	// exactly one flagged Main (it sets the workspace's working directory). Empty
	// degenerates to a single repo derived from MountSource — the pre-PLAN-20 shape.
	Repos []RepoMount `json:"repos,omitempty"`
	// Mounts are ADDITIVE user mounts (PLAN-20): extra host paths a project asks
	// for beyond the system mounts. They can only ever add: aped re-checks every
	// source against its mount-root allow-list and refuses any entry targeting a
	// reserved destination, so a committed file can never redirect the framework,
	// the composed home, or a repo.
	Mounts []MountSpec `json:"mounts,omitempty"`
	// Caches names the durable tool caches to mount (PLAN-22 D4): "go", "asdf", …
	// The NAMES are the request; the host source, guest path, and the environment
	// that points each toolchain at it are all resolved server-side from a closed
	// table, so a caller can never redirect GOPATH or ASDF_DATA_DIR itself.
	Caches []string `json:"caches,omitempty"`
	// FrameworkRef selects which materialized APEX framework ref to mount read-only
	// at /opt/apex-framework. It names a ref the NODE already has; aped resolves it
	// under its own framework root and errors if absent — it never fetches, and the
	// request can never point the mount somewhere else.
	FrameworkRef string `json:"framework_ref,omitempty"`
	// IdleStop is this workspace's idle-stop preference (PLAN-24 D7): a Go
	// duration, or "off" to exempt it from the node's idle reaper. Empty takes the
	// node's default.
	//
	// It can only narrow or disable. A node whose reaper is off ignores it entirely,
	// and no value here can make the node stop a workspace it would otherwise leave
	// alone — so this is a request like every other field, not a grant.
	IdleStop string `json:"idle_stop,omitempty"`
}

// MountSpec is one host→guest bind: a canonical host source, a guest destination,
// and its read-only flag. It is the single mount shape shared by the wire, the
// resolved spec, and the OCI bind layer.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type MountSpec struct {
	// Source is a canonical, absolute host path. Relative paths are resolved
	// client-side against the project root before they reach the wire — aped never
	// sees, nor trusts, a relative path.
	Source string `json:"source"`
	// Dest is the guest mount point (absolute).
	Dest string `json:"dest"`
	// ReadOnly is the guest-side bind option. User-declared mounts default to true
	// (opt in to write); the framework is always true; project repos default false.
	ReadOnly bool `json:"readonly,omitempty"`
}

// RepoMount is one project repository in a possibly-multi-repo workspace. Each is
// mounted at /workspace/<Name> — always, even for a single repo — and exactly one
// is Main, which sets the default working directory and the target for framework
// setup and boundary commits.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type RepoMount struct {
	Source   string `json:"source"` // canonical host path to the repo
	Name     string `json:"name"`   // mount name → /workspace/<name>
	Main     bool   `json:"main,omitempty"`
	ReadOnly bool   `json:"readonly,omitempty"`
}

// EgressRequest is the requested allowlist for a workspace's public egress. It
// carries the same two shapes as a profile's network policy: HTTPS domains
// reached through the CONNECT proxy, and fixed host:port pairs for non-HTTP
// endpoints. Domains may be exact ("github.com") or single leading-wildcard
// ("*.githubusercontent.com").
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type EgressRequest struct {
	AuthorizedDomains []string `json:"authorized_domains,omitempty"`
	DirectAllow       []string `json:"direct_allow,omitempty"`
}

// Domains returns the requested domains (nil-safe).
func (e *EgressRequest) Domains() []string {
	if e == nil {
		return nil
	}
	return e.AuthorizedDomains
}

// Device is one passthrough device request. Exactly one of PCI/USB is set.
type Device struct {
	// PCI passes a whole IOMMU group (BDF → vfio-pci): GPUs and PCI controllers.
	PCI string `json:"pci,omitempty"`
	// USB passes a single device by "vendor:product" via QEMU usb-host — NOT
	// whole-controller VFIO (that would leak the system keyboard/mouse). Only
	// the backend synthesises the usb-host string, from a per-caller allowlist;
	// the caller never sends raw QEMU args (PLAN-18 D5).
	USB string `json:"usb,omitempty"`
}

// Workspace is the durable record of a provisioned workspace — the source of
// truth for List. Live state is reported separately by Inspect (Status).
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type Workspace struct {
	ID        string   `json:"id"` // logical id (== name on the shell tier)
	Name      string   `json:"name"`
	Image     string   `json:"image,omitempty"`
	Runtime   string   `json:"runtime,omitempty"` // kata-qemu | kata-clh | firecracker
	Mount     string   `json:"mount,omitempty"`
	Profile   string   `json:"profile,omitempty"`
	Devices   []Device `json:"devices,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	// LastUsedAt is the last exec/attach/start on this workspace (PLAN-22 D5b), so an
	// operator can see which workspaces are actually in use before reclaiming any.
	LastUsedAt string `json:"last_used_at,omitempty"`
	// ApeVersion is the `ape` the node delivered into this workspace (PLAN-23). Worth
	// recording because it is the NODE's ape, not necessarily the one the operator ran
	// `ape sandbox up` with — a laptop driving a remote node gets the node's. Empty for
	// workspaces created before delivery existed.
	ApeVersion string `json:"ape_version,omitempty"`
	// IdleStop is the idle-stop setting this workspace was created with (PLAN-24
	// D7): a duration, "off", or empty for the node's default. Reported so an
	// operator can see WHY a workspace is or is not being auto-stopped without
	// reading the project's descriptor.
	IdleStop string `json:"idle_stop,omitempty"`
}

// UsageTotals is one bucket of Claude usage: cost and the token split behind it
// (PLAN-24 D3). It mirrors the shape internal/cost produces, restated here
// because this package is the pure wire contract — vmmclient links it and must
// not pull the price table in with it.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type UsageTotals struct {
	CostUSD             float64 `json:"cost_usd"`
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	NumTurns            int     `json:"num_turns"`
}

// Cost is one workspace's all-time Claude usage rollup, read from the
// transcripts its composed home already holds on the host.
//
// UnpricedModels/EstimatedModels are not decoration: a total computed from a
// price table with a gap in it is a LOWER BOUND, and a consumer that cannot tell
// the two apart will report a confident wrong number. They travel with the
// total for exactly that reason.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type Cost struct {
	Name string `json:"name"`
	// Sessions is how many Claude sessions ran in this workspace.
	Sessions int         `json:"sessions"`
	Totals   UsageTotals `json:"totals"`
	// PerModel breaks the total down by normalized model id.
	PerModel map[string]UsageTotals `json:"per_model,omitempty"`
	// FirstTurnAt/LastTurnAt bound the work, RFC3339. Empty when no turns were
	// found — which is what an unused workspace looks like.
	FirstTurnAt string `json:"first_turn_at,omitempty"`
	LastTurnAt  string `json:"last_turn_at,omitempty"`
	// UnpricedModels contributed $0 because no rate was found — NOT because they
	// were free. EstimatedModels were priced at their family rate rather than an
	// exact published one.
	UnpricedModels  []string `json:"unpriced_models,omitempty"`
	EstimatedModels []string `json:"estimated_models,omitempty"`
	// Error explains why THIS workspace could not be read, while the rest of the
	// report still stands. Empty on a clean read.
	Error string `json:"error,omitempty"`
}

// State is a workspace's lifecycle state, reported by Inspect.
type State string

const (
	StateCreated State = "created"
	StateRunning State = "running"
	StateFrozen  State = "frozen" // cgroup-frozen (RAM resident)
	StateStopped State = "stopped"
	StateExited  State = "exited"
)

// Status is a workspace's live state (Inspect result).
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type Status struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    State  `json:"state"`
	ExitCode *int   `json:"exit_code,omitempty"` // set once terminal
}

// ExitStatus is the result of Exec/Attach.
type ExitStatus struct {
	Code int `json:"code"`
}

// DestroyRequest carries teardown options.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type DestroyRequest struct {
	Force        bool `json:"force,omitempty"`
	RemoveVolume bool `json:"remove_volume,omitempty"`
}

// ExecRequest is a one-shot command to run inside a workspace.
type ExecRequest struct {
	Cmd []string `json:"cmd"`
	TTY bool     `json:"tty,omitempty"`
	Env []string `json:"env,omitempty"`
}

// AttachRequest opens an interactive session. Shell "" uses the backend default.
type AttachRequest struct {
	Shell string `json:"shell,omitempty"`
	TTY   bool   `json:"tty,omitempty"`
	// Cmd, when set, opens a streamed one-shot command instead of the login shell
	// — the streaming counterpart of the request/reply exec verb (its stdio rides
	// the session subjects). Additive to the wire contract.
	Cmd []string `json:"cmd,omitempty"`
}

// ForwardRequest opens a byte pipe to a TCP port inside a workspace (PLAN-24
// D2).
//
// It carries a PORT and nothing else. The command that runs in the guest is
// built by the node from its own delivered `ape`, so this cannot become
// arbitrary in-guest execution however it is crafted — which is the point of it
// being a separate verb rather than an exec the caller fills in.
//
// Nothing about it opens the workspace to the network. The guest end dials
// 127.0.0.1 INSIDE the guest, where loopback is the one thing the workspace's
// ruleset accepts; the bytes ride the same authenticated NATS session transport
// as exec and attach. There is no listener on the workspace and no change to
// either firewall.
type ForwardRequest struct {
	// Port is the guest-local TCP port to connect to (1..65535).
	Port int `json:"port"`
}

// LogsRequest selects how much output to return.
type LogsRequest struct {
	Follow bool `json:"follow,omitempty"`
	Tail   int  `json:"tail,omitempty"`
}

// SnapshotRequest names a snapshot to take (VMM save/restore tier only).
type SnapshotRequest struct {
	Name string `json:"name,omitempty"`
}

// SnapshotRef identifies a taken snapshot.
type SnapshotRef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// EventType classifies a lifecycle Event.
type EventType string

const (
	EventTaskExit   EventType = "task-exit"
	EventTaskFrozen EventType = "task-frozen" // containerd TaskPaused
	EventTaskOOM    EventType = "task-oom"
)

// Event is one workspace lifecycle event from Events.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type Event struct {
	Type        EventType `json:"type"`
	WorkspaceID string    `json:"workspace_id"`
	Time        string    `json:"time,omitempty"` // RFC3339
	ExitCode    *int      `json:"exit_code,omitempty"`
}

// Capabilities reports what a backend/node can provision.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type Capabilities struct {
	KVM      bool          `json:"kvm"`
	Runtimes []RuntimeInfo `json:"runtimes"`
	HostFS   bool          `json:"host_fs"` // false on Firecracker nodes
	GPUs     []GPU         `json:"gpus,omitempty"`
	USB      []USBDevice   `json:"usb,omitempty"` // passable USB devices for usb-host
	IOMMU    IOMMUState    `json:"iommu"`
	Mem      MemInfo       `json:"mem"`
	Factory  FactoryState  `json:"factory"`
	// Capacity answers "does another workspace fit on this node?" for a HUMAN
	// (PLAN-24 D4). It is deliberately not scheduler input: placement across nodes
	// is a fleet concern, and a node whose workspaces are pinned to the disk holding
	// their repos cannot be scheduled against anyway.
	Capacity CapacityInfo `json:"capacity"`
}

// CapacityInfo is the node's headroom, reported as the numbers an operator
// actually reasons with: how many cores it has, how many workspaces it already
// carries, how big a workspace is assumed to be, and how many more the free
// memory holds.
//
// Fits is an ESTIMATE and says so: a workspace is a real VM whose guest memory
// is set by the runtime's configuration, not by this daemon, so the divisor is an
// assumption (WorkspaceMemBytes) rather than a measurement. It is reported
// alongside the raw memory numbers precisely so a reader can disagree with it.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type CapacityInfo struct {
	// Cores is the number of logical CPUs visible to the node.
	Cores int `json:"cores"`
	// Workspaces is how many workspaces this node has provisioned (running or not).
	Workspaces int `json:"workspaces"`
	// Running is how many of them currently hold a live task (and therefore RAM).
	Running int `json:"running"`
	// WorkspaceMemBytes is the per-workspace guest memory Fits divides by.
	WorkspaceMemBytes int64 `json:"workspace_mem_bytes"`
	// Fits is how many additional workspaces the node's AVAILABLE memory holds at
	// WorkspaceMemBytes each. Zero means the next one does not fit.
	Fits int `json:"fits"`
}

// RuntimeInfo describes a containerd runtime handler the node offers.
type RuntimeInfo struct {
	Name    string `json:"name"` // e.g. io.containerd.kata-clh.v2
	VMM     string `json:"vmm"`  // clh | qemu | firecracker
	Default bool   `json:"default,omitempty"`
}

// GPU describes a passthrough-capable GPU and its IOMMU group.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type GPU struct {
	BDF           string   `json:"bdf"`
	VendorID      string   `json:"vendor_id"`
	DeviceID      string   `json:"device_id"`
	Model         string   `json:"model"`
	Driver        string   `json:"driver"`
	IOMMUGroup    int      `json:"iommu_group"`
	GroupIsolated bool     `json:"group_isolated"`
	GroupMembers  []string `json:"group_members,omitempty"`
}

// USBDevice describes a passable USB device (forwarded via usb-host, not VFIO).
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type USBDevice struct {
	VendorID    string `json:"vendor_id"`
	ProductID   string `json:"product_id"`
	Description string `json:"description,omitempty"`
}

// IOMMUState reports host IOMMU/VFIO readiness.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type IOMMUState struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode,omitempty"`
	VfioReady bool   `json:"vfio_ready"`
}

// MemInfo reports host memory available to workspaces.
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type MemInfo struct {
	TotalBytes     int64 `json:"total_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}

// FactoryState reports Kata fast-create options. Templating shares guest RAM
// read-only (a KSM-class side channel — surfaced, never a per-run flag).
//
//nolint:tagliatelle // snake_case is the documented vmm NATS wire contract
type FactoryState struct {
	Templating bool `json:"templating"`
	VMCache    bool `json:"vm_cache"`
}
