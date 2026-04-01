// Package web provides an embedded web UI for msgvault.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

// Handler returns an http.Handler serving the embedded web UI.
// Expected to be mounted at /web via chi.Mount("/web", web.Handler()).
// chi strips the /web prefix before calling the handler, so paths arrive as
// "/" for the index page and "/style.css", "/app.js" for assets.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("web: embedded static files: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(sub))

	// Pre-read index.html for direct serving (avoids FileServer redirects).
	indexHTML, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("web: read index.html: " + err.Error())
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve index.html at root.
		if r.URL.Path == "/" || r.URL.Path == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(indexHTML)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
