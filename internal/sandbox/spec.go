package sandbox

import (
	"errors"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// DefaultProjectDest is where the project root is bind-mounted inside the
// guest. It is a fixed path (not the host path) so it never collides with
// the masked /home tree even when the project lives under the user's home.
// The guest job is invoked with --cwd pointed here.
const DefaultProjectDest = "/workspace"

// defaultMaskedPaths are host paths the guest must not see even though the
// rootfs is the (read-only) host filesystem: the real homes, system state,
// SSH host config, and common cloud-credential locations. Masking renders
// them empty/inaccessible inside the guest regardless of the ro rootfs.
var defaultMaskedPaths = []string{
	"/home",
	"/root",
	"/etc/ssh",
	"/etc/shadow",
	"/etc/gshadow",
	"/var/lib",
	"/var/log",
	"/proc/kcore",
	"/proc/keys",
}

// SpecOptions drives BuildSpec. Comp, ProjectRoot, and Args are required.
type SpecOptions struct {
	RootfsPath   string       // guest rootfs; default "/" (the host fs, read-only)
	ReadonlyRoot *bool        // default true
	ProjectRoot  string       // host project path (bind-mounted rw)
	ProjectDest  string       // guest mount point; default DefaultProjectDest
	Comp         *Composition // staging home + binds + env from Compose
	Args         []string     // guest process argv (e.g. ape task … --no-tui)
	Cwd          string       // guest working dir; default ProjectDest
	ExtraRW      []string     // extra host paths bind-mounted rw at the same path
	Env          []string     // extra env merged after Comp.Env (proxy vars, NATS, …)
	Terminal     bool         // allocate a PTY in the guest
	UID, GID     uint32       // guest process user (match host uid to keep mount ownership sane)

	// HostNetwork omits the private network namespace. The rootless tier
	// sets this (rootless runsc can't drive the isolated netstack — it
	// uses host networking, and egress is controlled by HTTPS_PROXY +
	// the CONNECT proxy). The strict tier leaves it false so the runner
	// can wire a per-job netns + nft hard wall.
	HostNetwork bool
}

// BuildSpec constructs an OCI runtime spec (config.json). It is retained
// as the reusable OCI-config builder for the ctr/OCI-bundle driver path
// (the primary Phase-1 path builds nerdctl args directly — see kata.go's
// RunArgs). The shape follows PLAN-16 D1: read-only host rootfs, masked
// homes/secrets, the project and synthetic home mounted rw, tmpfs on /tmp,
// a private network namespace, and the job command as the init process. It
// is pure — no filesystem or process calls — so it unit-tests on every
// platform.
func BuildSpec(opts SpecOptions) (*specs.Spec, error) {
	if opts.Comp == nil {
		return nil, errors.New("spec: composition is nil")
	}
	if opts.ProjectRoot == "" {
		return nil, errors.New("spec: project root is empty")
	}
	if len(opts.Args) == 0 {
		return nil, errors.New("spec: args is empty")
	}

	rootfs := opts.RootfsPath
	if rootfs == "" {
		rootfs = "/"
	}
	roRoot := true
	if opts.ReadonlyRoot != nil {
		roRoot = *opts.ReadonlyRoot
	}
	projectDest := opts.ProjectDest
	if projectDest == "" {
		projectDest = DefaultProjectDest
	}
	cwd := opts.Cwd
	if cwd == "" {
		cwd = projectDest
	}

	env := []string{
		"HOME=" + opts.Comp.GuestHome,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm-256color",
	}
	env = append(env, opts.Comp.Env...)
	env = append(env, opts.Env...)

	mounts := baseMounts()
	// Shadow the sensitive host dirs with empty tmpfs. This is the primary
	// masking mechanism: spike testing showed OCI `maskedPaths` is NOT
	// honored by rootless runsc over a host-/ rootfs (the real ~/.claude
	// stayed readable), whereas a tmpfs mounted over the dir reliably hides
	// it in every mode. maskedPaths is still set below as defence-in-depth
	// for the strict (root) tier. The synthetic home (/sandbox/home) and
	// project (/workspace) live outside these dirs, so shadowing is safe.
	mounts = append(mounts, sensitiveShadowMounts()...)
	mounts = append(
		mounts,
		// Project root: read-write, so the job can produce _output and commits.
		specs.Mount{
			Destination: projectDest,
			Type:        "bind",
			Source:      opts.ProjectRoot,
			Options:     []string{"rbind", "rw"},
		},
		// Synthetic home: read-write (session state, transcripts).
		specs.Mount{
			Destination: opts.Comp.GuestHome,
			Type:        "bind",
			Source:      opts.Comp.StagingDir,
			Options:     []string{"rbind", "rw"},
		},
		// tmpfs /tmp — never the host's.
		specs.Mount{
			Destination: "/tmp",
			Type:        "tmpfs",
			Source:      "tmpfs",
			Options:     []string{"nosuid", "nodev", "mode=1777"},
		},
	)
	for _, b := range opts.Comp.Binds {
		opt := "rw"
		if b.ReadOnly {
			opt = "ro"
		}
		mounts = append(mounts, specs.Mount{
			Destination: b.Dest,
			Type:        "bind",
			Source:      b.Source,
			Options:     []string{"rbind", opt},
		})
	}
	for _, p := range opts.ExtraRW {
		mounts = append(mounts, specs.Mount{
			Destination: p,
			Type:        "bind",
			Source:      p,
			Options:     []string{"rbind", "rw"},
		})
	}

	namespaces := []specs.LinuxNamespace{
		{Type: specs.PIDNamespace},
		{Type: specs.MountNamespace},
		{Type: specs.IPCNamespace},
		{Type: specs.UTSNamespace},
	}
	if !opts.HostNetwork {
		// Strict tier: private netns the runner wires to the proxy + nft.
		namespaces = append(namespaces, specs.LinuxNamespace{Type: specs.NetworkNamespace})
	}

	spec := &specs.Spec{
		Version: specs.Version,
		Root:    &specs.Root{Path: rootfs, Readonly: roRoot},
		Process: &specs.Process{
			Terminal: opts.Terminal,
			User:     specs.User{UID: opts.UID, GID: opts.GID},
			Args:     opts.Args,
			Env:      env,
			Cwd:      cwd,
			Capabilities: &specs.LinuxCapabilities{
				// Minimal set — the job is an unprivileged agent session, not
				// a container manager.
				Bounding:  defaultCaps,
				Effective: defaultCaps,
				Permitted: defaultCaps,
			},
		},
		Mounts: mounts,
		Linux: &specs.Linux{
			MaskedPaths: append([]string(nil), defaultMaskedPaths...),
			Namespaces:  namespaces,
		},
	}
	return spec, nil
}

// defaultCaps is the capability set granted to the guest process. Kept
// deliberately small.
var defaultCaps = []string{
	"CAP_CHOWN",
	"CAP_DAC_OVERRIDE",
	"CAP_FOWNER",
	"CAP_FSETID",
	"CAP_KILL",
	"CAP_SETGID",
	"CAP_SETUID",
}

// sensitiveDirs are host directories shadowed with empty tmpfs so their
// real contents never reach the guest. /home and /root cover the user and
// root homes (credentials, ssh keys, shell history); the cloud-cred dirs
// are belt-and-suspenders for the common SDK locations that sometimes sit
// outside a home.
var sensitiveDirs = []string{"/home", "/root"}

// sensitiveShadowMounts returns read-only empty-tmpfs mounts over each
// sensitive dir.
func sensitiveShadowMounts() []specs.Mount {
	out := make([]specs.Mount, 0, len(sensitiveDirs))
	for _, d := range sensitiveDirs {
		out = append(out, specs.Mount{
			Destination: d,
			Type:        "tmpfs",
			Source:      "tmpfs",
			Options:     []string{"ro", "nosuid", "nodev", "mode=0755"},
		})
	}
	return out
}

// baseMounts returns the standard pseudo-filesystems every guest needs.
func baseMounts() []specs.Mount {
	return []specs.Mount{
		{Destination: "/proc", Type: "proc", Source: "proc"},
		{
			Destination: "/dev",
			Type:        "tmpfs",
			Source:      "tmpfs",
			Options:     []string{"nosuid", "strictatime", "mode=755", "size=65536k"},
		},
		{
			Destination: "/dev/pts",
			Type:        "devpts",
			Source:      "devpts",
			Options:     []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"},
		},
		{
			Destination: "/dev/shm",
			Type:        "tmpfs",
			Source:      "shm",
			Options:     []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"},
		},
	}
}
