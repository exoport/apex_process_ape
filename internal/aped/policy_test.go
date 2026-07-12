package aped

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/workspace"
)

// goosWindows is the runtime.GOOS value for Windows, named once so the
// Linux-only test skips below don't trip goconst on the repeated literal.
const goosWindows = "windows"

func TestLoadPolicyRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte("images:\n  - img:1\nlimits:\n  max_vcpus: 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(good); err != nil {
		t.Fatalf("LoadPolicy(good): %v", err)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("imagez:\n  - typo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(bad); err == nil {
		t.Fatal("LoadPolicy should reject unknown keys (a typo must not silently widen policy)")
	}
}

func TestPolicyCheckImage(t *testing.T) {
	p := &Policy{Images: []string{"ghcr.io/exoport/ape-sandbox@sha256:abc"}}
	if err := p.CheckCreate(ResolvedCreate{Image: "ghcr.io/exoport/ape-sandbox@sha256:abc"}, 0); err != nil {
		t.Errorf("allowed image rejected: %v", err)
	}
	err := p.CheckCreate(ResolvedCreate{Image: "docker.io/library/evil:latest"}, 0)
	if !errors.Is(err, workspace.ErrPolicyDenied) {
		t.Errorf("disallowed image: got %v, want ErrPolicyDenied", err)
	}
	// Empty allow-list denies everything (default-deny).
	empty := &Policy{}
	if !errors.Is(empty.CheckCreate(ResolvedCreate{Image: "anything"}, 0), workspace.ErrPolicyDenied) {
		t.Error("empty image allow-list should deny all creates")
	}
}

func TestPolicyCheckLimits(t *testing.T) {
	p := &Policy{Images: []string{"img"}, Limits: Limits{MaxVCPUs: 4, MaxMemMB: 2048, MaxWorkspaces: 2}}
	base := ResolvedCreate{Image: "img"}

	if err := p.CheckCreate(base, 0); err != nil {
		t.Errorf("within limits rejected: %v", err)
	}
	if !errors.Is(p.CheckCreate(ResolvedCreate{Image: "img", VCPUs: 8}, 0), workspace.ErrPolicyDenied) {
		t.Error("vCPU ceiling not enforced")
	}
	if !errors.Is(p.CheckCreate(ResolvedCreate{Image: "img", MemMB: 4096}, 0), workspace.ErrPolicyDenied) {
		t.Error("mem ceiling not enforced")
	}
	if !errors.Is(p.CheckCreate(base, 2), workspace.ErrPolicyDenied) {
		t.Error("workspace count ceiling not enforced")
	}
	// Zero ceilings mean unlimited.
	unl := &Policy{Images: []string{"img"}}
	if err := unl.CheckCreate(ResolvedCreate{Image: "img", VCPUs: 999, MemMB: 999999}, 999); err != nil {
		t.Errorf("zero ceilings should be unlimited: %v", err)
	}
}

func TestPolicyCheckDevicesDefaultDeny(t *testing.T) {
	p := &Policy{Images: []string{"img"}} // no devices allowed
	if !errors.Is(p.CheckCreate(ResolvedCreate{Image: "img", Devices: []workspace.Device{{PCI: "0000:01:00.0"}}}, 0), workspace.ErrPolicyDenied) {
		t.Error("PCI device should be denied by an empty allow-list")
	}

	allow := &Policy{Images: []string{"img"}, Devices: DevicePolicy{PCI: []string{"0000:01:00.0"}, USB: []string{"303a:1001"}}}
	if err := allow.CheckCreate(ResolvedCreate{Image: "img", Devices: []workspace.Device{{PCI: "0000:01:00.0"}}}, 0); err != nil {
		t.Errorf("allow-listed PCI device rejected: %v", err)
	}
	if err := allow.CheckCreate(ResolvedCreate{Image: "img", Devices: []workspace.Device{{USB: "303a:1001"}}}, 0); err != nil {
		t.Errorf("allow-listed USB device rejected: %v", err)
	}
	// Shape errors → VALIDATION, not DENIED.
	if !errors.Is(allow.CheckCreate(ResolvedCreate{Image: "img", Devices: []workspace.Device{{}}}, 0), workspace.ErrValidation) {
		t.Error("a device with neither pci nor usb should be a validation error")
	}
	if !errors.Is(allow.CheckCreate(ResolvedCreate{Image: "img", Devices: []workspace.Device{{PCI: "x", USB: "y"}}}, 0), workspace.ErrValidation) {
		t.Error("a device with both pci and usb should be a validation error")
	}
}

func TestPolicyCheckMountUnderRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "proj")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Policy{Images: []string{"img"}, MountRoots: []string{root}}

	if err := p.CheckCreate(ResolvedCreate{Image: "img", MountPath: sub}, 0); err != nil {
		t.Errorf("mount under an allowed root rejected: %v", err)
	}
	// A sibling outside the root is denied.
	outside := t.TempDir()
	if !errors.Is(p.CheckCreate(ResolvedCreate{Image: "img", MountPath: outside}, 0), workspace.ErrPolicyDenied) {
		t.Error("mount outside the allowed roots should be denied")
	}
	// Volume/ephemeral (no mount path) is unaffected.
	if err := p.CheckCreate(ResolvedCreate{Image: "img", MountPath: ""}, 0); err != nil {
		t.Errorf("no-mount create rejected: %v", err)
	}
}

// TestPolicyMountProtectHomeHint proves a host-fs mount aped cannot see (a source
// under a ProtectHome-masked root, or a permission failure — the mask's shape)
// fails with actionable guidance, not a raw lstat error. An unmasked path with a
// benign error keeps its plain error.
func TestPolicyMountProtectHomeHint(t *testing.T) {
	if runtime.GOOS == goosWindows {
		// ProtectHome is a systemd/Linux concept and the hint logic resolves
		// Linux absolute mount roots (/home, /root) via filepath.Abs, which
		// mangles POSIX paths on Windows. aped only runs on Linux.
		t.Skip("Linux-only: systemd ProtectHome path masking")
	}
	if h := protectHomeHint("/home/dev/proj", os.ErrNotExist); !strings.Contains(h, "ProtectHome") {
		t.Errorf("mount under /home got no ProtectHome hint: %q", h)
	}
	if h := protectHomeHint("/root/proj", os.ErrNotExist); !strings.Contains(h, "ProtectHome") {
		t.Errorf("mount under /root got no ProtectHome hint: %q", h)
	}
	if h := protectHomeHint("/srv/x", fs.ErrPermission); !strings.Contains(h, "ProtectHome") {
		t.Errorf("permission error got no ProtectHome hint: %q", h)
	}
	if h := protectHomeHint("/srv/workspaces/p", os.ErrNotExist); h != "" {
		t.Errorf("unmasked path with a benign error should get no hint, got %q", h)
	}
}

// TestPolicyMountSymlinkEscape proves policy authorizes the symlink-resolved
// target (the CVE lesson): a symlink that lives under an allowed root but points
// outside it is denied.
func TestPolicyMountSymlinkEscape(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	p := &Policy{Images: []string{"img"}, MountRoots: []string{root}}
	if !errors.Is(p.CheckCreate(ResolvedCreate{Image: "img", MountPath: link}, 0), workspace.ErrPolicyDenied) {
		t.Error("a symlink under the root pointing outside must be denied (resolved-path check)")
	}
}
