// Package web provides an embedded web UI for msgvault.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/*
var staticFiles embed.FS

// Handler returns an http.Handler serving the embedded web UI.
// Files are served under the /web/ prefix. Requests to /web or /web/ serve index.html.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("web: embedded static files: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(sub))

	// Pre-read index.html for direct serving.
	indexHTML, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("web: read index.html: " + err.Error())
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/web")
		if path == "" || path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(indexHTML)
			return
		}
		r.URL.Path = path
		fileServer.ServeHTTP(w, r)
	})
}
