package web

import (
	"net/http"
)

// MountAssets registers GET /assets/... on mux to serve the embedded
// assets/ subtree. PLAN-5 / C8.
//
// Cache-Control: no-store on every response so browsers always
// re-fetch when the ape binary's embedded assets change. Without
// this, the OS-level Chrome / Firefox disk cache cheerfully keeps
// the prior run's styles.css / page.tmpl / app.js, even across
// hard-refresh in some cases. Embed-time assets cycle on every
// rebuild; conditional GET / ETag doesn't add real value here.
func MountAssets(mux *http.ServeMux) error {
	sub, err := AssetsFS()
	if err != nil {
		return err
	}
	fs := http.FileServer(http.FS(sub))
	mux.Handle("/assets/", http.StripPrefix("/assets/", noCacheWrap(fs)))
	return nil
}

func noCacheWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}
