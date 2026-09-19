// Package frontend embeds and serves the built web UI.
package frontend

import (
	"embed"
	"io/fs"
	"net/http"
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
