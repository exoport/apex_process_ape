//go:build !linux

package sandbox

// NewContainerdDriver is unavailable off Linux — containerd + Kata + KVM are
// Linux-only. The stub keeps the Windows/macOS cross-compile green; aped runs
// only on Linux, where containerd_driver_linux.go provides the real driver.
func NewContainerdDriver(_ ContainerdConfig) (ProvisioningBackend, error) {
	return nil, ErrUnsupported
}
