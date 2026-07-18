//go:build ui

// Package web embeds the built SPA (web/dist) into the binary.
// Build with `-tags ui` after `npm run build`; without the tag a stub page is
// served so `go test ./...` never needs node.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if f, err := sub.Open(path); err == nil {
				f.Close()
				// Vite emits content-hashed filenames under assets/.
				if strings.HasPrefix(path, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback: every other route renders index.html.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
