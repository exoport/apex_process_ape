//go:build linux

package sandbox

import (
	"os"
	"testing"
)

func TestParseMeminfoKB(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"       32784064 kB", 32784064 * 1024},
		{" 1 kB", 1024},
		{"not a number kB", 0},
		{"", 0},
	} {
		if got := parseMeminfoKB(tc.in); got != tc.want {
			t.Errorf("parseMeminfoKB(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseKataFactoryReadsBothSwitches(t *testing.T) {
	cfg := `
[factory]
enable_template = true
vm_cache_number = 2
`
	got := parseKataFactory(cfg)
	if !got.Templating || !got.VMCache {
		t.Errorf("parseKataFactory = %+v, want both enabled", got)
	}
}

func TestParseKataFactoryShippedDefaultsAreOff(t *testing.T) {
	// Kata ships both keys COMMENTED OUT. A line scan that ignored the comment
	// marker would report a factory that is not configured, which is the exact
	// failure this test exists to prevent.
	cfg := `
[factory]
#enable_template = true
# vm_cache_number = 2
`
	got := parseKataFactory(cfg)
	if got.Templating || got.VMCache {
		t.Errorf("parseKataFactory = %+v on a stock config, want both off", got)
	}
}

func TestParseKataFactoryZeroCacheIsOff(t *testing.T) {
	got := parseKataFactory("vm_cache_number = 0\nenable_template = false\n")
	if got.VMCache || got.Templating {
		t.Errorf("parseKataFactory = %+v, want both off", got)
	}
}

func TestParseKataFactoryStripsTrailingComment(t *testing.T) {
	got := parseKataFactory("enable_template = true # share guest RAM read-only\n")
	if !got.Templating {
		t.Error("a trailing comment must not defeat the value parse")
	}
}

// The fallback that exists because aped's units run ProcSubset=pid, so
// /proc/meminfo is absent by design and a capacity report read it as "no memory,
// nothing fits" (measured live on mmq4, 2026-08-05).
func TestSumNodeMeminfoKey(t *testing.T) {
	body := `Node 0 MemTotal:       31686340 kB
Node 0 MemFree:         9313788 kB
Node 0 MemUsed:        22372552 kB
`
	if got, want := sumNodeMeminfoKey(body, "MemTotal"), int64(31686340)*1024; got != want {
		t.Errorf("MemTotal = %d, want %d", got, want)
	}
	if got, want := sumNodeMeminfoKey(body, "MemFree"), int64(9313788)*1024; got != want {
		t.Errorf("MemFree = %d, want %d", got, want)
	}
	// The key is the LAST field before the colon; a prefix match on the whole
	// line would pick up "Node 0 MemUsed" for a "MemUsed" query and miss here.
	if got := sumNodeMeminfoKey(body, "MemAvailable"); got != 0 {
		t.Errorf("MemAvailable = %d, want 0 — sysfs does not publish it", got)
	}
	if got := sumNodeMeminfoKey("", "MemTotal"); got != 0 {
		t.Errorf("empty body = %d, want 0", got)
	}
}

// Whatever the sandboxing, a node that can run VMs must not report kvm=no: that
// is a false negative, not an honest unknown, and it is what the sysfs fallback
// exists to prevent.
func TestProbeKVMUsesSysfsWhenDevIsHidden(t *testing.T) {
	if _, err := os.Stat("/sys/class/misc/kvm"); err != nil {
		t.Skip("this host has no KVM device")
	}
	if !ProbeKVM() {
		t.Error("ProbeKVM() = false on a host whose kernel exposes /sys/class/misc/kvm")
	}
}
