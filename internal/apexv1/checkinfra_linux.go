package apexv1

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// endTmpHolders ends and names the processes outside the check (inCheck
// false) that hold tmp — in their command line, working directory or
// environment (TMPDIR=tmp) — this user's alone (T1).
func endTmpHolders(tmp string, inCheck func(pid int) bool) []string {
	self := os.Getpid()
	uid := os.Getuid()
	var held []int
	var names []string
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	envNeedle := []byte("TMPDIR=" + tmp + "\x00")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self || inCheck(pid) {
			continue
		}
		dir := "/proc/" + e.Name()
		st, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if s, ok := st.Sys().(*syscall.Stat_t); !ok || int(s.Uid) != uid {
			continue
		}
		argv := pidArgv(pid)
		holds := strings.Contains(strings.Join(argv, " "), tmp)
		if !holds {
			if cwd, err := os.Readlink(dir + "/cwd"); err == nil && (cwd == tmp || strings.HasPrefix(cwd, tmp+"/")) {
				holds = true
			}
		}
		if !holds {
			if env, err := os.ReadFile(dir + "/environ"); err == nil && bytes.Contains(env, envNeedle) {
				holds = true
			}
		}
		if holds {
			held = append(held, pid)
			names = append(names, strconv.Itoa(pid)+" "+clip(strings.Join(argv, " "), 120)+" (outside its group and scope: it held the check's TMPDIR)")
		}
	}
	if len(held) == 0 {
		return nil
	}
	for _, pid := range held {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(leftoverGrace)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range held {
			if processAlive(pid) {
				alive = true
			}
		}
		if !alive {
			return names
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, pid := range held {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return names
}

// listenerPID is the pid of this user's process listening on TCP port, 0
// when none can be seen (another user's, or a container's port forwarder
// this user cannot read).
func listenerPID(port int) int {
	inodes := map[string]bool{}
	want := strings.ToUpper(strconv.FormatInt(int64(port), 16))
	for len(want) < 4 {
		want = "0" + want
	}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		file, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(file)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			// sl local_address rem_address st … inode
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			if _, p, ok := strings.Cut(fields[1], ":"); ok && p == want {
				inodes[fields[9]] = true
			}
		}
		_ = file.Close()
	}
	if len(inodes) == 0 {
		return 0
	}
	fds, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	for _, fd := range fds {
		link, err := os.Readlink(fd)
		if err != nil || !strings.HasPrefix(link, "socket:[") {
			continue
		}
		if inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] {
			pid, err := strconv.Atoi(strings.Split(fd, "/")[2])
			if err == nil {
				return pid
			}
		}
	}
	return 0
}

// procCgroupMemoryPeak is the memory.peak of pid's cgroup.
func procCgroupMemoryPeak(pid int) int64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return scopeMemoryPeak(cgroupRoot + rest)
		}
	}
	return 0
}
