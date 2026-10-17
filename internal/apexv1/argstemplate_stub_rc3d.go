package apexv1

// rc3-d stub of rc3-f's argstemplate.go (D77): the integrator deletes this
// file when rc3-f's ValidateArgsTemplate lands. Same signature; the
// minimal rule: every {placeholder} is one of the four names.

import (
	"fmt"
	"regexp"
	"slices"
)

var stubPlaceholderRe = regexp.MustCompile(`\{[a-z]+\}`)

// ValidateArgsTemplate refuses an args: template with an unknown
// placeholder.
func ValidateArgsTemplate(tmpl string) error {
	for _, ph := range stubPlaceholderRe.FindAllString(tmpl, -1) {
		if !slices.Contains([]string{"{id}", "{task}", "{assets}", "{change}"}, ph) {
			return fmt.Errorf("unknown placeholder %s (the placeholders are {id}, {task}, {assets}, {change})", ph)
		}
	}
	return nil
}
