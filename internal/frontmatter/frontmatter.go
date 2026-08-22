// Package frontmatter splits a Markdown document into its leading YAML
// frontmatter block and the body below it.
//
// Every APEX record — ADR, pattern, feature, capability, story, deferred
// item — is a Markdown file whose first block is `---`-delimited YAML.
// Four PLAN-25 packages need to read that block and only that block, so
// the parse lives here once: registry verification (does the record parse
// at all, what id does it claim), story projection (read at most 8 KB and
// never open a body), story verification, and the deferred store.
//
// Tolerances are deliberate and narrow: a UTF-8 BOM, CRLF line endings,
// and a closing delimiter at EOF without a trailing newline. Everything
// else is a parse failure, reported rather than guessed at.
package frontmatter

import (
	"bytes"
	"errors"
)

// ErrNoFrontmatter reports a document with no `---`-delimited leading
// block: either it does not start with a delimiter, or the block is never
// closed.
var ErrNoFrontmatter = errors.New("no YAML frontmatter block")

var (
	bom       = []byte{0xEF, 0xBB, 0xBF}
	delimiter = []byte("---")
)

// Split returns the frontmatter YAML (without its delimiters) and the
// body that follows. Both are subslices of data — no copying — so a
// caller that read only the first 8 KB of a file gets a frontmatter slice
// bounded by what it read.
//
// A truncated read whose block is never closed returns ErrNoFrontmatter,
// which is what stops an 8 KB-capped scan from silently treating half a
// document as its metadata.
func Split(data []byte) (fm, body []byte, err error) {
	rest := bytes.TrimPrefix(data, bom)

	first, ok := consumeDelimiterLine(rest)
	if !ok {
		return nil, nil, ErrNoFrontmatter
	}

	// Scan line by line for the closing delimiter. A line-oriented scan
	// (rather than searching for "\n---\n") is what makes CRLF and a
	// final line without a newline behave the same as the common case.
	offset := 0
	for offset < len(first) {
		lineEnd := bytes.IndexByte(first[offset:], '\n')
		var line []byte
		var next int
		if lineEnd < 0 {
			line = first[offset:]
			next = len(first)
		} else {
			line = first[offset : offset+lineEnd]
			next = offset + lineEnd + 1
		}
		if isDelimiter(line) {
			return first[:offset], first[next:], nil
		}
		offset = next
	}
	return nil, nil, ErrNoFrontmatter
}

// consumeDelimiterLine strips a leading `---` line and returns the rest.
func consumeDelimiterLine(data []byte) (rest []byte, ok bool) {
	lineEnd := bytes.IndexByte(data, '\n')
	if lineEnd < 0 {
		// A document that is nothing but "---" has no closing delimiter.
		return nil, false
	}
	if !isDelimiter(data[:lineEnd]) {
		return nil, false
	}
	return data[lineEnd+1:], true
}

// isDelimiter reports whether a line is exactly `---`, ignoring a
// trailing CR and trailing spaces.
func isDelimiter(line []byte) bool {
	return bytes.Equal(bytes.TrimRight(line, " \t\r"), delimiter)
}

// Has reports whether data opens with a frontmatter block, without
// caring what is in it.
func Has(data []byte) bool {
	_, _, err := Split(data)
	return err == nil
}
