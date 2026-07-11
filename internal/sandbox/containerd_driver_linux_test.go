//go:build linux

package sandbox

import "testing"

// TestNormalizeImageRef locks in the docker-convention normalization the driver
// needs so its exact-match GetImage lookup agrees with how nerdctl/containerd
// store and pull images (the live Tier-2 run surfaced a short-name miss).
func TestNormalizeImageRef(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"ape-tier2-probe:latest", "docker.io/library/ape-tier2-probe:latest"},
		{"alpine", "docker.io/library/alpine:latest"},
		{"alpine:3.20", "docker.io/library/alpine:3.20"},
		{"ghcr.io/exoport/ape-sandbox:v1", "ghcr.io/exoport/ape-sandbox:v1"},
		{"docker.io/library/busybox:latest", "docker.io/library/busybox:latest"},
	} {
		if got := normalizeImageRef(c.in); got != c.want {
			t.Errorf("normalizeImageRef(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
