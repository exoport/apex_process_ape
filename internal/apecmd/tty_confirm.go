package apecmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ttyConfirm prints question to stderr and reads a single line from
// stdin. Returns true on "y" / "yes" (case-insensitive). Used by the
// PLAN-5 / C6 .gitignore prompt.
func ttyConfirm(question string) bool {
	fmt.Fprint(os.Stderr, question)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	return answer == "y" || answer == "yes"
}
