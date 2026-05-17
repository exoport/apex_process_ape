package views

// Stage is the typed input to the `stage-card` template. The SSE
// renderer fills the StatusClass / Glyph fields from Stage.Status.
type Stage struct {
	Slug        string
	Name        string
	Status      string // running / done / failed / stopped / pending
	Duration    string
	Cost        string
	LastHook    string
	Hooks       []string // ring buffer of the last 20 hook lines (PLAN-5 / C8)
	StatusClass string
	Glyph       string
}

// HookBufferCap matches the spike behaviour: last 20 hook lines per
// stage so the per-stage details panel does not grow without bound.
// PLAN-5 / C8.
const HookBufferCap = 20

// AppendHook pushes one hook line into Stage.Hooks, dropping the
// oldest if the buffer is full. Also updates LastHook.
func (s *Stage) AppendHook(line string) {
	s.LastHook = line
	if len(s.Hooks) >= HookBufferCap {
		s.Hooks = s.Hooks[1:]
	}
	s.Hooks = append(s.Hooks, line)
}

// ApplyStatus computes StatusClass + Glyph from Status. Pure: no
// access to broker state, easy to test.
func (s *Stage) ApplyStatus() {
	switch s.Status {
	case "running":
		s.StatusClass = "running"
		s.Glyph = "⟳"
	case "done":
		s.StatusClass = "done"
		s.Glyph = "✓"
	case "failed":
		s.StatusClass = "failed"
		s.Glyph = "✗"
	case "stopped":
		s.StatusClass = "stopped"
		s.Glyph = "⏸"
	default:
		s.StatusClass = ""
		s.Glyph = "·"
	}
}

// TruncateMid keeps the first n/2 and last n/2 runes joined by `…`.
// PLAN-5 / C8 — hook envelopes can carry long `tool_input.command`
// values; truncate at render time so the activity feed stays narrow.
func TruncateMid(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	half := (n - 1) / 2
	return string(r[:half]) + "…" + string(r[len(r)-half:])
}
