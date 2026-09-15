package repl

import (
	"bytes"
	"errors"
	"io"
	"slices"
)

// scanCoupling streams r looking for two byte strings, and reports how many
// times each occurs and whether any pair of them lies within window bytes.
//
// It exists for the live Claude Code gate, which asks whether an environment
// variable ape depends on still sits beside the code it is supposed to guard
// (bg_shell_reap_switch). The input is a ~220 MB binary, so it is read in
// chunks of the given size; each chunk keeps a tail of the previous one, long
// enough that a match spanning the boundary — or a pair separated by up to
// window bytes across it — is still found. Offsets are de-duplicated, so the
// re-scanned overlap does not inflate the counts.
func scanCoupling(r io.Reader, anchor, needle []byte, window, chunk int) (anchors, needles int, together bool, err error) {
	if len(anchor) == 0 || len(needle) == 0 {
		return 0, 0, false, errors.New("repl: scanCoupling needs a non-empty anchor and needle")
	}
	overlap := window + len(anchor) + len(needle)
	buf := make([]byte, chunk+overlap)

	var (
		base     int64 // file offset of buf[0]
		filled   int
		anchorAt []int64
		needleAt []int64
	)
	offsets := func(hay, sep []byte, from int64) []int64 {
		var out []int64
		for i := 0; ; {
			j := bytes.Index(hay[i:], sep)
			if j < 0 {
				return out
			}
			out = append(out, from+int64(i+j))
			i += j + 1
		}
	}
	for {
		n, readErr := io.ReadFull(r, buf[filled:])
		filled += n
		if filled > 0 {
			anchorAt = append(anchorAt, offsets(buf[:filled], anchor, base)...)
			needleAt = append(needleAt, offsets(buf[:filled], needle, base)...)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
				return 0, 0, false, readErr
			}
			break
		}
		keep := min(overlap, filled)
		copy(buf, buf[filled-keep:filled])
		base += int64(filled - keep)
		filled = keep
	}

	slices.Sort(anchorAt)
	slices.Sort(needleAt)
	anchorAt = slices.Compact(anchorAt)
	needleAt = slices.Compact(needleAt)
	for _, a := range anchorAt {
		for _, h := range needleAt {
			if d := a - h; d <= int64(window) && -d <= int64(window) {
				return len(anchorAt), len(needleAt), true, nil
			}
		}
	}
	return len(anchorAt), len(needleAt), false, nil
}

// reapSwitchProblem judges what a scan of the Claude Code binary found about
// the background-shell pressure reap, and names the problem, or returns "" when
// the coupling ape depends on is intact.
//
// Three different failures, because the remedies differ: a renamed variable
// means ape exports a string nobody reads (find the new name); a vanished
// handler means the reap itself is gone (retire ape's default and this gate
// rather than leave them passing); both present but far apart means the
// variable no longer guards the handler (re-read the gate before trusting it).
func reapSwitchProblem(anchors, needles int, together bool) string {
	switch {
	case needles == 0:
		return "the variable is gone: Claude Code no longer reads it, so ape cannot turn the reap off"
	case anchors == 0:
		return "the memory-pressure handler is gone: the reap looks retired, and ape's spawn default with it"
	case !together:
		return "the variable and the handler are no longer near each other: it may not be the switch any more"
	default:
		return ""
	}
}
