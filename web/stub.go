//go:build !ui

package web

import "net/http"

// Handler without the `ui` build tag serves a hint instead of the SPA.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!doctype html><title>intents-explorer</title>
<body style="font-family:system-ui;padding:3rem"><h1>intents-explorer</h1>
<p>UI not embedded in this build. Build with <code>make build</code>
(runs <code>npm run build</code> + <code>go build -tags ui</code>).
The JSON API is live under <a href="/api/status">/api/status</a>.</p>`))
	})
}
