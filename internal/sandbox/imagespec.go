package sandbox

import (
	"fmt"
	"strconv"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// ContainerdSpecOptions drives applyImageConfig — the pure, mount-free core of
// the containerd driver's OCI-spec construction (PLAN-18 D3).
type ContainerdSpecOptions struct {
	// Config is the image's OCI config, read from the containerd content store.
	Config ocispec.ImageConfig
	// Args overrides the image entrypoint+cmd when non-empty.
	Args []string
	// Env is appended after the image env.
	Env []string
	// Cwd overrides the image WorkingDir when non-empty.
	Cwd string
	// Terminal allocates a PTY for the guest process.
	Terminal bool
	// Networkless keeps the workspace off any CNI network (Phase-2 default): the
	// process runs in a private network namespace with only loopback. Overlay
	// connectivity is Phase 3.
	Networkless bool
}

// applyImageConfig sets spec.Process from an OCI image config + overrides while
// doing NO rootfs mount and NO /etc/passwd|/etc/group resolution — the whole
// point of the containerd driver (PLAN-18 D3 / Risks "barrier 3"). containerd's
// oci.WithImageConfig resolves the process user by RO-bind-mounting the image
// rootfs to read /etc/group (oci.WithAdditionalGIDs); the hardened executor
// forbids that mount (@mount denied, no CAP_SYS_ADMIN), so `ape sandbox up`
// dies there under the nerdctl shellDriver. Reading the config from the content
// store and setting Process.{Args,Env,Cwd,User} directly — resolving only a
// NUMERIC uid[:gid], never a name — sidesteps the mount entirely.
//
// It mutates spec in place (the driver applies it as an oci.SpecOpts on top of
// containerd's default spec) and adds ZERO mounts. spec.Process is created when
// nil so the pure builder can be exercised against a bare spec in tests.
func applyImageConfig(spec *specs.Spec, opts ContainerdSpecOptions) error {
	if spec.Process == nil {
		spec.Process = &specs.Process{}
	}
	cfg := opts.Config

	if len(opts.Args) > 0 {
		spec.Process.Args = append([]string(nil), opts.Args...)
	} else {
		args := make([]string, 0, len(cfg.Entrypoint)+len(cfg.Cmd))
		args = append(args, cfg.Entrypoint...)
		args = append(args, cfg.Cmd...)
		spec.Process.Args = args
	}

	cwd := opts.Cwd
	if cwd == "" {
		cwd = cfg.WorkingDir
	}
	if cwd == "" {
		cwd = "/"
	}
	spec.Process.Cwd = cwd

	env := make([]string, 0, len(cfg.Env)+len(opts.Env))
	env = append(env, cfg.Env...)
	env = append(env, opts.Env...)
	spec.Process.Env = env

	uid, gid, err := parseNumericUser(cfg.User)
	if err != nil {
		return err
	}
	spec.Process.User = specs.User{UID: uid, GID: gid}
	// Explicitly no additional GIDs: resolving them is exactly the /etc/group
	// rootfs read (barrier 3) this path exists to avoid.
	spec.Process.User.AdditionalGids = nil
	spec.Process.Terminal = opts.Terminal

	applyNetworkless(spec, opts.Networkless)
	return nil
}

// applyNetworkless makes spec's network posture match networkless: when true the
// process gets a private (path-less) network namespace — loopback only, no CNI —
// which is nerdctl's `--network none` equivalent and the Phase-2 default. When
// false any network namespace is dropped so a CNI/overlay layer can attach
// (Phase 3). It is idempotent over containerd's default spec (which already
// carries a private netns).
func applyNetworkless(spec *specs.Spec, networkless bool) {
	if spec.Linux == nil {
		spec.Linux = &specs.Linux{}
	}
	filtered := spec.Linux.Namespaces[:0:0]
	hasNet := false
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			hasNet = true
			if !networkless {
				continue // drop it → host/CNI networking
			}
		}
		filtered = append(filtered, ns)
	}
	if networkless && !hasNet {
		filtered = append(filtered, specs.LinuxNamespace{Type: specs.NetworkNamespace})
	}
	spec.Linux.Namespaces = filtered
}

// parseNumericUser parses an image-config User of the form "", "uid", or
// "uid:gid" where uid/gid are NUMERIC. A named user/group (e.g. "node" or
// "1000:app") is rejected: resolving it needs /etc/passwd+/etc/group from the
// image rootfs — the mount the containerd path avoids. "" → 0:0; a bare "uid"
// defaults gid to 0 (the runc numeric convention).
func parseNumericUser(user string) (uid, gid uint32, err error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return 0, 0, nil
	}
	uPart, gPart, hasGroup := strings.Cut(user, ":")
	u, err := strconv.ParseUint(uPart, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("containerd driver: image USER %q is not numeric "+
			"(name resolution needs the rootfs mount this path avoids; set a numeric uid[:gid])", user)
	}
	uid = uint32(u)
	if hasGroup {
		g, err := strconv.ParseUint(gPart, 10, 32)
		if err != nil {
			return 0, 0, fmt.Errorf("containerd driver: image USER group %q is not numeric", gPart)
		}
		gid = uint32(g)
	}
	return uid, gid, nil
}
