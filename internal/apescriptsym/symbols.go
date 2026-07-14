// Package apescriptsym holds the yaegi symbol table for the public
// apescript package. The bulk of this package is generated — regenerate it
// with `make apescript-symbols` (yaegi extract) whenever the apescript
// surface changes, and commit the result. Kept in a dedicated package so the
// generated wrapper (which imports apescript) does not pollute internal/apecmd.
package apescriptsym

import "reflect"

// Symbols is the interp.Exports map the `ape script` interpreter loads via
// i.Use(apescriptsym.Symbols), exposing the apescript library to scripts.
var Symbols = map[string]map[string]reflect.Value{}

//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract github.com/exoport/apex_process_ape/apescript
