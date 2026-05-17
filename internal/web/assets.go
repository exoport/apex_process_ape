// Package web hosts the HTMX-driven browser UI. Templates and assets
// are embedded at build time via //go:embed — no toolchain on either
// the contributor or the end-user side. PLAN-5 / C8.
package web

import (
	"embed"
	"html/template"
	"io/fs"
)

//go:embed assets templates
var rootFS embed.FS

// AssetsFS returns the embedded assets/ subtree, suitable for serving
// via http.FileServer.
func AssetsFS() (fs.FS, error) {
	return fs.Sub(rootFS, "assets")
}

// MustTemplates parses page.tmpl + fragments.tmpl. Panics on parse
// error — both files are embedded at compile time so a parse failure
// is a build bug, not a runtime concern.
func MustTemplates() *template.Template {
	t, err := template.New("ape").ParseFS(rootFS, "templates/*.tmpl")
	if err != nil {
		panic(err)
	}
	return t
}
