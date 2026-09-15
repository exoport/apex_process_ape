package story

import (
	"os"
	"regexp"
	"strings"

	"github.com/exoport/apex_process_ape/internal/mdscan"
)

// bodyStatusRe matches a brownfield story's own status line.
//
// Anchored at the start of a line and tolerant of bold, because a lifted
// document may carry `Status: in-progress`, `**Status:** in-progress`,
// `**Status**: in-progress` or `Status: **in-progress**`. Read from PROSE,
// so a status inside a fenced example is not mistaken for the story's own.
//
// Each spelling is asserted in the test. An earlier copy of this pattern
// allowed the closing `**` after the colon but no space after it, so
// `**Status:** in-progress` — the form its own comment named — read as no
// status at all.
var bodyStatusRe = regexp.MustCompile(`(?mi)^[ \t]*\*{0,2}status\*{0,2}[ \t]*:[ \t]*\*{0,2}[ \t]*([A-Za-z][A-Za-z0-9 _-]*?)\*{0,2}[ \t]*$`)

// BodyStatus reads the first `Status:` line from a story's prose — the
// text with its fenced blocks already stripped — lower-cased, or "" when
// there is none.
//
// This is a brownfield story's only statement of its own status: it has no
// frontmatter. It lives in `internal/story` rather than `internal/sprint`
// so that every reader of a story file reads the one line the same way; two
// copies would disagree about whether the same file has a status.
func BodyStatus(prose string) string {
	m := bodyStatusRe.FindStringSubmatch(prose)
	if m == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(m[1]))
}

// ReadBodyStatus reads a whole file and returns BodyStatus of its prose.
func ReadBodyStatus(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return BodyStatus(mdscan.StripFences(string(raw))), nil
}
