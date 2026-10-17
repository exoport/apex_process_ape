// Package proctree reads the process tree: a process's parent, its chain
// of ancestors, and whether a pid is alive. ape's nesting rule (D62) uses
// it to find the ape-launched session a command runs under even when the
// environment that says so was scrubbed: a model can rewrite its
// environment, but not the parent of the shell its tool runs in.
package proctree

// MaxDepth bounds an ancestor walk: a process tree is never this deep, and
// a cycle (a pid reused while the walk ran) must still end.
const MaxDepth = 64

// ParentFunc returns pid's parent pid, false when it cannot be read.
type ParentFunc func(pid int) (int, bool)

// Ancestors lists pid's ancestors, nearest first, through parent: pid's
// parent, its parent, and so on, up to init (pid 1, not listed), a parent
// that cannot be read, or MaxDepth.
func Ancestors(pid int, parent ParentFunc) []int {
	var out []int
	seen := map[int]bool{pid: true}
	for range MaxDepth {
		pp, ok := parent(pid)
		if !ok || pp <= 1 || seen[pp] {
			break
		}
		out = append(out, pp)
		seen[pp] = true
		pid = pp
	}
	return out
}
