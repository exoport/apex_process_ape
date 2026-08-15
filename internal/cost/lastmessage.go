package cost

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

// finalMessageLine is the minimal shape LastAssistantText needs from a
// transcript row. Deliberately separate from AssistantLine: the cost
// scanner runs over every line of multi-megabyte transcripts and has no
// reason to pay for decoding content blocks, and this reader has no
// reason to pay for usage blocks.
//
// Content is a raw slice so a block whose `text` is absent (tool_use,
// thinking) costs nothing to skip.
type finalMessageLine struct {
	Type        string `json:"type"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// LastAssistantText returns the text of the last assistant message in a
// session transcript — the run's closing message, which is where a skill
// puts its terminal return contract.
//
// Deliberately reads the transcript rather than the `last_assistant_message`
// field Claude Code puts on the Stop hook payload. The transcript is a
// file ape must keep parsing correctly for cost telemetry regardless, so
// a check built on it survives the harness renaming or dropping a hook
// field. That independence is the whole point of this check.
//
// Skipped rows: non-assistant, isMeta, and isSidechain. Sidechain lines
// are sub-agent turns — a sub-agent's closing message is not the run's.
// Only text blocks contribute; a final turn that is pure tool_use has no
// text and is passed over in favour of the last one that does.
//
// ok is false when the file cannot be read or holds no assistant text.
// Malformed lines are skipped, never fatal — same tolerance the cost
// scanner applies, and for the same reason: one bad line must not
// invalidate the whole read.
func LastAssistantText(path string) (text string, ok bool) {
	if path == "" {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ml finalMessageLine
		if err := json.Unmarshal(sc.Bytes(), &ml); err != nil {
			continue
		}
		if ml.Type != AssistantRowType || ml.IsMeta || ml.IsSidechain {
			continue
		}
		var b strings.Builder
		for _, blk := range ml.Message.Content {
			if blk.Type != "text" || blk.Text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(blk.Text)
		}
		// Keep the last turn that actually said something.
		if b.Len() > 0 {
			text = b.String()
		}
	}
	if err := sc.Err(); err != nil {
		// A truncated tail still leaves everything read so far usable.
		return text, text != ""
	}
	return text, text != ""
}
