//go:build linux

package sandbox

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/exoport/apex_process_ape/internal/workspace"
)

// The Linux probes behind PLAN-24 D4. Each is best-effort and fails to the
// conservative answer: a node whose /proc/meminfo cannot be read reports no
// available memory (and therefore no room), never unbounded room.

// kvmDevice is the KVM character device. Its presence is what makes a node able
// to run a hardware-isolated workspace at all.
const kvmDevice = "/dev/kvm"

// kataConfigPaths are the standard Kata configuration locations, in the order
// kata-runtime itself searches them. They are read ONLY to report whether the
// fast-create factory options are configured — never to change behaviour.
var kataConfigPaths = []string{
	"/etc/kata-containers/configuration.toml",
	"/usr/share/defaults/kata-containers/configuration.toml",
	"/opt/kata/share/defaults/kata-containers/configuration.toml",
}

// ProbeCores reports the logical CPUs available to this process. It is
// deliberately the process's view rather than the machine's: a daemon under
// CPUAffinity= can only place work on the CPUs it was given, so the machine
// count would overstate what a workspace can actually use.
func ProbeCores() int { return runtime.NumCPU() }

// ProbeKVM reports whether this NODE can run hardware-isolated workspaces.
//
// The subtlety that makes this two probes rather than one: aped's units run with
// PrivateDevices=yes, so /dev/kvm is *not in this process's /dev at all* — and it
// does not need to be. aped never touches KVM; containerd and the Kata shim do,
// from their own units. Probing openability here therefore answers "may this
// daemon open KVM", which is not the question and is reliably "no" on a
// correctly hardened node — a false negative on a box that demonstrably boots
// VMs (measured live on mmq4, 2026-08-05).
//
// So the device open stays as the precise answer where it is available (an
// unhardened dev box, a test), and sysfs is the fallback: /sys is read-only under
// ProtectKernelTunables rather than hidden, and /sys/class/misc/kvm exists
// exactly when the kernel has the KVM device. That is a property of the node,
// which is what a capacity report is about.
func ProbeKVM() bool {
	if f, err := os.OpenFile(kvmDevice, os.O_RDWR, 0); err == nil {
		_ = f.Close()
		return true
	}
	for _, p := range []string{"/sys/class/misc/kvm", "/sys/module/kvm"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// ProbeMem reads total + available memory from /proc/meminfo.
//
// MemAvailable (not MemFree) is the right number: it is the kernel's own
// estimate of what a new workload can claim without swapping, which already
// accounts for reclaimable page cache. MemFree on a busy host reads as almost
// nothing and would report a box with plenty of headroom as full.
//
// A missing MemAvailable (kernels before 3.14 — not a case this stack supports,
// but the parse must still be total) leaves AvailableBytes zero, which reads as
// "no room" rather than as a fabricated number.
func ProbeMem() workspace.MemInfo {
	if info := procMeminfo(); info.TotalBytes > 0 {
		return info
	}
	// /proc/meminfo is not readable here, and that is not a broken host: aped's
	// units run with ProcSubset=pid, which mounts /proc showing only process
	// directories, so every non-pid file — meminfo, stat, loadavg — is absent by
	// design (measured live on mmq4, 2026-08-05).
	//
	// sysfs survives that (ProtectKernelTunables makes /sys read-only, not
	// hidden), so fall back to the per-NUMA-node meminfo. It costs precision, and
	// the cost falls in the SAFE direction: sysfs offers MemFree, not
	// MemAvailable, so reclaimable page cache is not counted as headroom and the
	// node reports LESS room than it has. Under-reporting delays a workspace
	// someone could have started; over-reporting starts one the box cannot hold.
	return sysfsMeminfo()
}

// procMeminfo reads /proc/meminfo. A zero TotalBytes means unreadable.
func procMeminfo() workspace.MemInfo {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return workspace.MemInfo{}
	}
	defer f.Close()

	var info workspace.MemInfo
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch key {
		case "MemTotal":
			info.TotalBytes = parseMeminfoKB(value)
		case "MemAvailable":
			info.AvailableBytes = parseMeminfoKB(value)
		}
	}
	return info
}

// sysfsMeminfo sums MemTotal/MemFree across every NUMA node. Summed rather than
// read from node0, because a two-socket box would otherwise report half its
// memory.
func sysfsMeminfo() workspace.MemInfo {
	nodes, err := filepath.Glob("/sys/devices/system/node/node*/meminfo")
	if err != nil || len(nodes) == 0 {
		return workspace.MemInfo{}
	}
	var info workspace.MemInfo
	for _, path := range nodes {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		info.TotalBytes += sumNodeMeminfoKey(string(data), "MemTotal")
		info.AvailableBytes += sumNodeMeminfoKey(string(data), "MemFree")
	}
	return info
}

// sumNodeMeminfoKey pulls one value out of a node meminfo body, whose lines look
// like "Node 0 MemTotal:  31686340 kB" — so the key is the last field before the
// colon, not the whole prefix.
func sumNodeMeminfoKey(body, want string) int64 {
	for line := range strings.SplitSeq(body, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(key)
		if len(fields) > 0 && fields[len(fields)-1] == want {
			return parseMeminfoKB(value)
		}
	}
	return 0
}

// parseMeminfoKB parses a "  12345 kB" meminfo value into bytes. Anything
// unparseable yields 0.
func parseMeminfoKB(value string) int64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}
	kb, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return kb * 1024
}

// ProbeFactory reports whether Kata's fast-create options are CONFIGURED on this
// node — VM templating (a pre-booted guest whose memory is shared read-only by
// every clone) and the VM cache.
//
// Reported, never enabled: templating shares guest RAM read-only across
// workspaces, which is a KSM-class side channel between tenants. That trade-off
// is an operator's to make in the Kata configuration, so this only says whether
// they have made it. A node with no readable Kata configuration reports neither,
// which is what an unconfigured node has.
func ProbeFactory() workspace.FactoryState {
	for _, p := range kataConfigPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return parseKataFactory(string(data))
	}
	// Kata also ships per-hypervisor configs beside the generic one; try the
	// handlers this tier drives before giving up.
	for _, dir := range []string{"/etc/kata-containers", "/opt/kata/share/defaults/kata-containers"} {
		for _, vmm := range []string{"qemu", "clh"} {
			data, err := os.ReadFile(filepath.Join(dir, "configuration-"+vmm+".toml"))
			if err != nil {
				continue
			}
			return parseKataFactory(string(data))
		}
	}
	return workspace.FactoryState{}
}

// parseKataFactory extracts the two factory switches from a Kata
// configuration.toml. It is a line scan rather than a TOML parse on purpose:
// this reads one boolean and one integer out of a file the daemon does not own,
// and taking a TOML dependency to do it would be a poor trade. Commented-out
// lines (which is how Kata ships both keys) are skipped, so the shipped default
// reads as "off" — which is what it is.
func parseKataFactory(cfg string) workspace.FactoryState {
	var st workspace.FactoryState
	for line := range strings.SplitSeq(cfg, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
		switch strings.TrimSpace(key) {
		case "enable_template":
			st.Templating = value == "true"
		case "vm_cache_number":
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				st.VMCache = true
			}
		}
	}
	return st
}
