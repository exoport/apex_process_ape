//go:build linux

package sandbox

import (
	"strings"
	"testing"
)

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

// TestNormalizeImageRefKeepsThePinnedDigest guards the digest-pinned DefaultImage.
//
// It carries a tag AND a digest, and ParseDockerRef reduces that to the digest alone —
// which is what containerd stores and pulls. The failure this catches is quiet: a
// malformed pin does not error, it makes ParseDockerRef fail, normalizeImageRef fall
// back to the raw ref, the driver's exact-match GetImage lookup miss, and the driver
// then attempt a pull that cannot succeed. Derived from the constant so it follows the
// pin rather than having to be edited alongside it.
func TestNormalizeImageRefKeepsThePinnedDigest(t *testing.T) {
	at := strings.Index(DefaultImage, "@")
	if at < 0 {
		t.Skip("DefaultImage is not digest-pinned")
	}
	want := DefaultImage[:strings.LastIndex(DefaultImage[:at], ":")] + DefaultImage[at:]
	if got := normalizeImageRef(DefaultImage); got != want {
		t.Errorf("normalizeImageRef(%q) = %q, want %q (tag dropped, digest kept)", DefaultImage, got, want)
	}
}
