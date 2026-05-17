package web

import (
	"net/http"
)

// MountAssets registers GET /assets/... on mux to serve the embedded
// assets/ subtree. Used by the broker as an extra handler. PLAN-5 / C8.
func MountAssets(mux *http.ServeMux) error {
	sub, err := AssetsFS()
	if err != nil {
		return err
	}
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))
	return nil
}
