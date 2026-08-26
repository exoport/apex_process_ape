package framework

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// The command surface an installed framework requires of the binary.
//
// From framework v0.11.0 the skills shell out to ape subcommands and
// PLAN-57's DD3 deleted every fallback branch, so a binary missing one
// makes a skill fail deep inside a multi-hour stage rather than degrade.
// Nothing else notices: `framework.metadata` compares versions of the
// framework, not of ape, and an eval capture came within a hand-check of
// measuring 8 hours of broken runs against an ape that predated the
// commands its skills called.
//
// The manifest is the framework's declaration of that surface, shipped to
// `_apex/ape-commands.yaml`. It answers ONE of the two questions the file
// carries, and this reader deliberately reads only that one:
//
//	required_commands  what the BINARY must provide  → ape doctor
//	sanctioned         what a SKILL STEP may execute → the framework's own linter
//
// `sanctioned` is strictly smaller — it exists to lint skills, so it omits
// the operator-facing and dispatch commands that the binary nonetheless
// owes. Reading it here would under-declare the surface by exactly the
// commands an operator is told to type.
//
// Why names and not a version floor: a locally-built ape reports a Go
// pseudo-version (`0.0.53-0.20260823120345-def342771795`) that a floor
// check skips as unstamped, so it would pass on precisely the binary in
// question. A name diff tests the capability rather than the label, and
// holds for local builds, forks and pseudo-versions alike.

// ApeCommands is the parsed manifest.
type ApeCommands struct {
	// Required is the command surface the framework depends on, each entry
	// as written: a space-separated path, conventionally including the
	// leading `ape`.
	Required []string
}

// apeCommandsFile is the on-disk shape. Only required_commands is decoded;
// the file's other lists belong to the framework's own validators, and
// yaml.Unmarshal ignores what this struct does not name.
//
//nolint:tagliatelle // snake_case is the framework's on-disk contract, not ape's to rename
type apeCommandsFile struct {
	RequiredCommands []string `yaml:"required_commands"`
}

// LoadApeCommands reads `_apex/ape-commands.yaml` from a project.
//
// An absent file returns (nil, nil): a framework older than v0.11.0 ships
// no such manifest, and that is version skew rather than a fault. Only a
// present-but-unreadable or unparseable file returns an error.
func LoadApeCommands(projectRoot string) (*ApeCommands, error) {
	path := filepath.Join(projectRoot, ProjectApeCommands)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil //nolint:nilnil // absent manifest is a documented "nothing to check"
		}
		return nil, fmt.Errorf("read %s: %w", ProjectApeCommands, err)
	}
	var f apeCommandsFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ProjectApeCommands, err)
	}
	out := &ApeCommands{}
	for _, entry := range f.RequiredCommands {
		if e := strings.TrimSpace(entry); e != "" {
			out.Required = append(out.Required, e)
		}
	}
	return out, nil
}

// CommandPath splits a manifest entry into the argument path to resolve
// against a command tree, dropping the leading binary name.
//
// Entries are written the way an operator types them (`ape memory index`),
// so the `ape` is prose rather than part of the path. Tolerated either way
// in case a future manifest omits it.
func CommandPath(entry string) []string {
	fields := strings.Fields(entry)
	if len(fields) > 0 && fields[0] == "ape" {
		fields = fields[1:]
	}
	return fields
}
