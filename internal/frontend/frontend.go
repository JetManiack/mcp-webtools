// Package frontend embeds and serves the built web UI.
package frontend

import (
	"embed"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
)

//go:generate npm --prefix ../../web install
//go:generate npm --prefix ../../web run build

//go:embed static/*
var assets embed.FS

// FS returns the frontend's static assets. When devel is true, it reads live
// from disk (internal/frontend/static) instead of the embedded snapshot, so
// JS changes show up without rebuilding the Go binary.
func FS(devel bool) (http.FileSystem, error) {
	if devel {
		return http.Dir("internal/frontend/static"), nil
	}
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	return http.FS(sub), nil
}

// Handler returns an http.Handler that serves the embedded UI.
// Unknown paths (React client-side routes) fall back to index.html so
// BrowserRouter navigation works when the user refreshes or bookmarks a
// sub-path like /tool-calls or /actors.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fsPath := strings.TrimPrefix(r.URL.Path, "/")
		if fsPath == "" {
			fsPath = "."
		}
		f, openErr := sub.Open(fsPath)
		if openErr == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// Unknown path — let React router handle it.
		r2 := r.Clone(r.Context())
		r2.URL = &url.URL{Path: "/"}
		fileServer.ServeHTTP(w, r2)
	}), nil
}
