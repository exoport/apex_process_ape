package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ErrUnsupported is returned when a Kata workspace operation is attempted
// on a platform that can't provide it (non-Linux — Kata/KVM is Linux-only).
// The CLI turns this into a clear usage error.
var ErrUnsupported = errors.New("sandbox: Kata workspaces require Linux with containerd + Kata")

// ContainerPrefix namespaces the containerd container name ape derives from
// a workspace name, so `nerdctl ps` and teardown never collide with the
// user's own containers.
const ContainerPrefix = "ape-ws-"

// DefaultImage is the official ape-sandbox image reference used when a
// profile leaves `image:` empty. It is pinned (never :latest) and tracks
// ape + framework releases (PLAN-16 D6). The concrete tag is produced by
// the image pipeline (Step 5); this default is the wiring point.
const DefaultImage = "ghcr.io/exoport/ape-sandbox:v0"

// DefaultShell is the login shell `ape sandbox attach` opens inside a
// workspace when the caller doesn't pick one.
const DefaultShell = "/bin/bash"

// ContainerName derives the containerd container name for a workspace.
func ContainerName(workspace string) string { return ContainerPrefix + workspace }

// runtimeHandler maps a VMM to its containerd runtime handler. kata-deploy
// registers one handler per VMM (io.containerd.kata-<vmm>.v2); clh is the
// default, qemu is the device tier.
func runtimeHandler(vmm VMM) string {
	switch vmm {
	case VMMQemu:
		return "io.containerd.kata-qemu.v2"
	case VMMCloudHypervisor:
		return "io.containerd.kata-clh.v2"
	default:
		return "io.containerd.kata-clh.v2"
	}
}

// WorkspaceSpec is the fully-resolved description of one workspace to
// provision — everything the nerdctl command builder needs, produced from
// a Profile + a Composition + the resolved project/staging paths. It is
// backend-neutral input to the pure RunArgs builder; the Linux runner
// hands the args to nerdctl.
type WorkspaceSpec struct {
	Name        string       // logical workspace name (`ape sandbox up <name>`)
	Image       string       // resolved OCI image ref (never empty here)
	VMM         VMM          // clh | qemu → runtime handler
	Mount       MountMode    // host-fs | volume | ephemeral
	ProjectRoot string       // host project path (host-fs mode)
	ProjectDest string       // guest mount point; default DefaultProjectDest
	Volume      string       // named volume (volume mode)
	Comp        *Composition // staging home + binds + env from Compose
	HTTPSProxy  string       // HTTPS_PROXY value wired into the guest; "" → none
	SSHPort     int          // host loopback port forwarded to guest :22; 0 → none
	Env         []string     // extra KEY=VALUE env beyond Comp.Env / the proxy
	Command     []string     // container command override; empty → the image default
}

// Container returns the containerd container name for the workspace.
func (s WorkspaceSpec) Container() string { return ContainerName(s.Name) }

// RunArgs builds the nerdctl argument vector (everything after the binary
// name) that provisions the workspace: a detached, long-lived Kata
// container with the composed home + project mounted, egress proxy env,
// and an optional forwarded sshd port. It is pure — no exec, no
// filesystem — so it unit-tests on every platform.
func (s WorkspaceSpec) RunArgs() ([]string, error) {
	if strings.TrimSpace(s.Name) == "" {
		return nil, errors.New("workspace: name is empty")
	}
	if strings.TrimSpace(s.Image) == "" {
		return nil, errors.New("workspace: image is empty (resolve it before building run args)")
	}
	if s.Comp == nil {
		return nil, errors.New("workspace: composition is nil")
	}
	dest := s.ProjectDest
	if dest == "" {
		dest = DefaultProjectDest
	}

	args := []string{
		"run", "-d",
		"--name", s.Container(),
		"--runtime", runtimeHandler(s.VMM),
		"--label", "ape.managed=true",
		"--label", "ape.workspace=" + s.Name,
	}

	// Project mount depends on the mode.
	switch s.Mount {
	case MountHostFS:
		if strings.TrimSpace(s.ProjectRoot) == "" {
			return nil, errors.New("workspace: mount host-fs requires a project root")
		}
		args = append(args, "-v", s.ProjectRoot+":"+dest)
	case MountVolume:
		if strings.TrimSpace(s.Volume) == "" {
			return nil, errors.New("workspace: mount volume requires a volume name")
		}
		args = append(args, "-v", s.Volume+":"+dest)
	case MountEphemeral:
		// Nothing from the host: the workspace clones the repo in-guest and
		// discards it on teardown. No project bind.
	default:
		return nil, fmt.Errorf("workspace: unknown mount mode %q", s.Mount)
	}

	// Composed ~/.claude staging home mounted as the guest $HOME.
	args = append(args, "-v", s.Comp.StagingDir+":"+s.Comp.GuestHome)
	for _, b := range s.Comp.Binds {
		args = append(args, "-v", bindVolumeArg(b))
	}

	// Env: HOME, then egress proxy vars, then the composition env
	// (credentials/git token), then any extra. Deterministic order for tests.
	args = append(args, "-e", "HOME="+s.Comp.GuestHome)
	for _, e := range ProxyEnv(s.HTTPSProxy) {
		args = append(args, "-e", e)
	}
	for _, e := range s.Comp.Env {
		args = append(args, "-e", e)
	}
	for _, e := range s.Env {
		args = append(args, "-e", e)
	}

	// sshd access over host loopback (Phase 1; overlay is Phase 3).
	if s.SSHPort != 0 {
		args = append(args, "-p", "127.0.0.1:"+strconv.Itoa(s.SSHPort)+":22")
	}

	args = append(args, s.Image)
	args = append(args, s.Command...)
	return args, nil
}

// bindVolumeArg renders a BindMount as a nerdctl -v value: "src:dst" or
// "src:dst:ro".
func bindVolumeArg(b BindMount) string {
	v := b.Source + ":" + b.Dest
	if b.ReadOnly {
		v += ":ro"
	}
	return v
}

// ProxyEnv returns the proxy env vars to inject into the guest for a given
// CONNECT-proxy URL, or nil when no proxy is configured. NO_PROXY keeps
// loopback traffic (sshd, the in-guest MCP bridge) off the proxy.
func ProxyEnv(proxyURL string) []string {
	if strings.TrimSpace(proxyURL) == "" {
		return nil
	}
	return []string{
		"HTTPS_PROXY=" + proxyURL,
		"HTTP_PROXY=" + proxyURL,
		"NO_PROXY=localhost,127.0.0.1",
	}
}

// ExecArgs builds `nerdctl exec [-it] <container> <cmd...>` for running a
// one-shot command inside a workspace.
func ExecArgs(container string, tty bool, cmd []string) []string {
	args := []string{"exec"}
	if tty {
		args = append(args, "-it")
	}
	args = append(args, container)
	return append(args, cmd...)
}

// AttachArgs builds an interactive-shell exec into the workspace. attach is
// modelled as a login shell (not `nerdctl attach`, which binds PID 1's
// stdio — useless when PID 1 is the workspace's sshd/supervisor).
func AttachArgs(container, shell string) []string {
	if shell == "" {
		shell = DefaultShell
	}
	return ExecArgs(container, true, []string{shell, "-l"})
}

// PauseArgs / ResumeArgs / DownArgs build the lifecycle commands. pause
// suspends the microVM; unpause resumes it; rm -f tears it down.
func PauseArgs(container string) []string  { return []string{"pause", container} }
func ResumeArgs(container string) []string { return []string{"unpause", container} }
func DownArgs(container string) []string   { return []string{"rm", "-f", container} }

// SSHArgs builds the `ssh` argument vector to reach a workspace over the
// forwarded host-loopback port (PLAN-16 D7). The per-workspace known_hosts
// keeps host-key state isolated; accept-new avoids a TOFU prompt on first
// connect without silently trusting a changed key later.
func SSHArgs(user string, port int, knownHostsFile string, extra []string) []string {
	if user == "" {
		user = "ape"
	}
	args := []string{
		"-p", strconv.Itoa(port),
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if knownHostsFile != "" {
		args = append(args, "-o", "UserKnownHostsFile="+knownHostsFile)
	}
	args = append(args, extra...)
	return append(args, user+"@127.0.0.1")
}

// ResolveImage returns the profile's image or the pinned official default.
func ResolveImage(p *Profile) string {
	if p != nil && strings.TrimSpace(p.Image) != "" {
		return p.Image
	}
	return DefaultImage
}

// ---- On-disk workspace registry -------------------------------------------

// Workspace is one row of the on-disk registry: the durable record of a
// provisioned workspace (name → container id, profile, mount) the runner
// needs to attach/pause/down it later without re-reading the profile.
//
//nolint:tagliatelle // stable on-disk JSON field names
type Workspace struct {
	Name        string `json:"name"`
	Container   string `json:"container"`
	Profile     string `json:"profile"`
	Backend     string `json:"backend"`
	VMM         string `json:"vmm"`
	Image       string `json:"image"`
	Mount       string `json:"mount"`
	ProjectRoot string `json:"project_root,omitempty"`
	Volume      string `json:"volume,omitempty"`
	StagingDir  string `json:"staging_dir"`
	SSHPort     int    `json:"ssh_port,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`

	// Egress-proxy supervisor record (PLAN-16 D4). Set only when `up`
	// started a managed CONNECT proxy for the workspace (a profile
	// declaring network.authorized_domains, no explicit --proxy); zero for
	// open-egress or an externally-supplied --proxy. `down` uses ProxyPID
	// to stop the daemon.
	ProxyPID      int    `json:"proxy_pid,omitempty"`
	ProxyAddr     string `json:"proxy_addr,omitempty"`
	ProxyAuditLog string `json:"proxy_audit_log,omitempty"`
}

// Registry is the on-disk index of provisioned workspaces, stored as a
// single JSON file. It is the source of truth for `ape sandbox ls` and for
// resolving a workspace name to its container/mount at attach/down time.
type Registry struct {
	path string
}

// OpenRegistry returns a Registry backed by <baseDir>/workspaces.json. The
// file is created lazily on first Put; a missing file reads as empty.
func OpenRegistry(baseDir string) *Registry {
	return &Registry{path: filepath.Join(baseDir, "workspaces.json")}
}

// Path returns the backing file path (for diagnostics).
func (r *Registry) Path() string { return r.path }

// load reads the registry map; a missing file is an empty registry.
func (r *Registry) load() (map[string]Workspace, error) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Workspace{}, nil
		}
		return nil, fmt.Errorf("sandbox: read registry %s: %w", r.path, err)
	}
	m := map[string]Workspace{}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("sandbox: parse registry %s: %w", r.path, err)
	}
	return m, nil
}

// save writes the registry map atomically (temp file + rename) so a
// crashed write never truncates the index.
func (r *Registry) save(m map[string]Workspace) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("sandbox: mkdir registry dir: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("sandbox: marshal registry: %w", err)
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("sandbox: write registry temp: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("sandbox: replace registry: %w", err)
	}
	return nil
}

// List returns every registered workspace, sorted by name for stable
// output.
func (r *Registry) List() ([]Workspace, error) {
	m, err := r.load()
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(m))
	for name := range m {
		out = append(out, m[name])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns the workspace with the given name; ok is false when absent.
func (r *Registry) Get(name string) (Workspace, bool, error) {
	m, err := r.load()
	if err != nil {
		return Workspace{}, false, err
	}
	w, ok := m[name]
	return w, ok, nil
}

// Put inserts or replaces a workspace record.
func (r *Registry) Put(w Workspace) error {
	if strings.TrimSpace(w.Name) == "" {
		return errors.New("sandbox: registry Put with empty name")
	}
	m, err := r.load()
	if err != nil {
		return err
	}
	m[w.Name] = w
	return r.save(m)
}

// Remove deletes a workspace record. Removing an absent name is a no-op.
func (r *Registry) Remove(name string) error {
	m, err := r.load()
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		return nil
	}
	delete(m, name)
	return r.save(m)
}

// DefaultStateDir is where ape keeps sandbox state (the workspace registry
// and per-workspace composed homes): <user-config-dir>/ape/sandbox.
func DefaultStateDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve config dir: %w", err)
	}
	return filepath.Join(dir, "ape", "sandbox"), nil
}

// StagingDirFor returns the per-workspace composed-home staging path under
// a state dir. Each workspace gets its own home so credentials/skills
// never bleed across workspaces.
func StagingDirFor(stateDir, name string) string {
	return filepath.Join(stateDir, "homes", name)
}

// ---- Kata runner ----------------------------------------------------------

// DefaultNerdctl is the driver binary ape shells out to. containerd is a
// host prerequisite (not a Go dependency) — see PLAN-16's backend decision.
const DefaultNerdctl = "nerdctl"

// Runner executes Kata workspace lifecycle operations by shelling out to
// nerdctl (provision/exec/attach/pause/resume/down). The struct is defined
// here (cross-platform); the method bodies live in kata_linux.go, with a
// portable ErrUnsupported stub in kata_other.go so the Windows CI leg
// compiles. Stdin/Stdout/Stderr are wired through for the interactive
// exec/attach paths; nil falls back to the process's own streams.
type Runner struct {
	Nerdctl string    // driver binary; default DefaultNerdctl
	Stdin   io.Reader // guest stdin for interactive exec/attach
	Stdout  io.Writer
	Stderr  io.Writer
}

// bin returns the configured driver binary or the default.
func (r *Runner) bin() string {
	if strings.TrimSpace(r.Nerdctl) != "" {
		return r.Nerdctl
	}
	return DefaultNerdctl
}
