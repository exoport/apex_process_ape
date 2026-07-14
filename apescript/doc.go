// Package apescript is the public, versioned library scripts run by
// `ape script` import to orchestrate ape's primitives.
//
// A script is a plain Go file that defines
//
//	func Main(ctx context.Context) error
//
// The `ape script <file.go>` command evaluates the file inside an in-process
// [github.com/traefik/yaegi] interpreter, then calls Main. Everything a script
// needs to drive ape — running a pipeline/task/prompt, reading a manifest,
// scanning a transcript, logging, reading its args, publishing an event, or
// uploading a blob — is a function in this package.
//
// # Dual nature
//
// This package is importable two ways, and the same import line serves both:
//
//   - At authoring time you `go get` the ape module and import this package so
//     your editor type-checks the script and offers autocomplete.
//   - At run time the `ape script` command resolves the import to the
//     in-process implementation via yaegi's symbol table, so RunPipeline &
//     friends drive the exact same code paths the `ape` CLI uses.
//
// # Runtime binding
//
// The orchestration functions only work while a script runs under
// `ape script`: the command installs a per-invocation environment (project
// root, args, event publisher, blob store, and the PTY-backed runner hooks)
// via [Activate] before evaluating the file. Called outside that window they
// return [ErrNoRuntime]. Scripts never call [Activate] themselves — it is the
// host command's wiring seam.
//
// # Compatibility
//
// The surface is a semver-honest public API: additive until v1.0. It mirrors
// the shapes of ape's internal packages (cost, pipeline, blobstore) through
// type aliases so a script sees the real, documented field sets without
// importing internal packages.
package apescript
