package sandbox

import (
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// baseSpec mimics the shape containerd's oci.WithDefaultSpec hands us: a Process
// stub, a pre-existing mount, and a default private network namespace. The
// pre-existing mount lets the tests assert applyImageConfig adds NONE (proving
// it never temp-mounts the rootfs — barrier 3).
func baseSpec() *specs.Spec {
	return &specs.Spec{
		Process: &specs.Process{},
		Mounts:  []specs.Mount{{Destination: "/proc", Type: "proc", Source: "proc"}},
		Linux: &specs.Linux{
			Namespaces: []specs.LinuxNamespace{
				{Type: specs.PIDNamespace},
				{Type: specs.NetworkNamespace},
			},
		},
	}
}

func TestApplyImageConfigProjectsProcessNoMount(t *testing.T) {
	spec := baseSpec()
	mountsBefore := len(spec.Mounts)
	err := applyImageConfig(spec, ContainerdSpecOptions{
		Config: ocispec.ImageConfig{
			User:       "1000:2000",
			Env:        []string{"PATH=/usr/bin", "FOO=bar"},
			Entrypoint: []string{"/entry"},
			Cmd:        []string{"--flag", "x"},
			WorkingDir: "/work",
		},
		Env:         []string{"APE_NATS_URL=nats://x"},
		Networkless: true,
	})
	if err != nil {
		t.Fatalf("applyImageConfig: %v", err)
	}

	// Barrier 3: NOT ONE mount added (WithImageConfig would have temp-mounted the rootfs).
	if len(spec.Mounts) != mountsBefore {
		t.Fatalf("applyImageConfig added mounts (barrier-3 regression): %+v", spec.Mounts)
	}

	if got := spec.Process.User; got.UID != 1000 || got.GID != 2000 {
		t.Errorf("user = %+v, want uid=1000 gid=2000", got)
	}
	if len(spec.Process.User.AdditionalGids) != 0 {
		t.Errorf("additional gids = %v, want none (no /etc/group read)", spec.Process.User.AdditionalGids)
	}
	wantArgs := []string{"/entry", "--flag", "x"}
	if !equalStrings(spec.Process.Args, wantArgs) {
		t.Errorf("args = %v, want %v", spec.Process.Args, wantArgs)
	}
	wantEnv := []string{"PATH=/usr/bin", "FOO=bar", "APE_NATS_URL=nats://x"}
	if !equalStrings(spec.Process.Env, wantEnv) {
		t.Errorf("env = %v, want %v", spec.Process.Env, wantEnv)
	}
	if spec.Process.Cwd != "/work" {
		t.Errorf("cwd = %q, want /work", spec.Process.Cwd)
	}
}

func TestApplyImageConfigArgsAndCwdOverride(t *testing.T) {
	spec := baseSpec()
	err := applyImageConfig(spec, ContainerdSpecOptions{
		Config: ocispec.ImageConfig{Entrypoint: []string{"/entry"}, Cmd: []string{"default"}, WorkingDir: "/img"},
		Args:   []string{"sleep", "infinity"},
		Cwd:    "/override",
	})
	if err != nil {
		t.Fatalf("applyImageConfig: %v", err)
	}
	if !equalStrings(spec.Process.Args, []string{"sleep", "infinity"}) {
		t.Errorf("args override ignored: %v", spec.Process.Args)
	}
	if spec.Process.Cwd != "/override" {
		t.Errorf("cwd override ignored: %q", spec.Process.Cwd)
	}
}

func TestApplyImageConfigDefaults(t *testing.T) {
	spec := baseSpec()
	if err := applyImageConfig(spec, ContainerdSpecOptions{Config: ocispec.ImageConfig{}}); err != nil {
		t.Fatalf("applyImageConfig: %v", err)
	}
	if spec.Process.User.UID != 0 || spec.Process.User.GID != 0 {
		t.Errorf("empty user → %+v, want 0:0", spec.Process.User)
	}
	if spec.Process.Cwd != "/" {
		t.Errorf("empty workdir → %q, want /", spec.Process.Cwd)
	}
}

func TestApplyImageConfigRejectsNamedUser(t *testing.T) {
	spec := baseSpec()
	err := applyImageConfig(spec, ContainerdSpecOptions{Config: ocispec.ImageConfig{User: "node"}})
	if err == nil {
		t.Fatal("named USER accepted, want rejection (would need the rootfs mount)")
	}
}

func TestParseNumericUser(t *testing.T) {
	cases := []struct {
		in       string
		uid, gid uint32
		wantErr  bool
	}{
		{"", 0, 0, false},
		{"0", 0, 0, false},
		{"1000", 1000, 0, false}, // bare uid → gid 0 (runc numeric convention)
		{"1000:2000", 1000, 2000, false},
		{"node", 0, 0, true},
		{"1000:app", 0, 0, true},
	}
	for _, c := range cases {
		uid, gid, err := parseNumericUser(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseNumericUser(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && (uid != c.uid || gid != c.gid) {
			t.Errorf("parseNumericUser(%q) = %d:%d, want %d:%d", c.in, uid, gid, c.uid, c.gid)
		}
	}
}

func TestApplyNetworklessNamespace(t *testing.T) {
	// networkless=true keeps the private netns.
	spec := baseSpec()
	applyNetworkless(spec, true)
	if !hasNetNS(spec) {
		t.Error("networkless=true dropped the private network namespace")
	}
	// networkless=false drops it (CNI/overlay attaches — Phase 3).
	spec = baseSpec()
	applyNetworkless(spec, false)
	if hasNetNS(spec) {
		t.Error("networkless=false kept a network namespace")
	}
	// Idempotent: adding when absent.
	spec = &specs.Spec{Linux: &specs.Linux{}}
	applyNetworkless(spec, true)
	if !hasNetNS(spec) {
		t.Error("networkless=true did not add a network namespace to a bare spec")
	}
}

func hasNetNS(spec *specs.Spec) bool {
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
