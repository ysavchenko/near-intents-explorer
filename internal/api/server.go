// Package api serves the JSON API and the embedded SPA behind basic auth.
package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"intents-explorer/internal/assets"
	"intents-explorer/internal/config"
	"intents-explorer/internal/intents"
	"intents-explorer/internal/metrics"
	"intents-explorer/internal/neardata"
)

type Server struct {
	Pool     *pgxpool.Pool
	Client   *neardata.Client
	Registry *assets.Registry
	Solvers  *intents.SolverSet
	Metrics  *metrics.Metrics
	Cfg      *config.Config
	Log      *slog.Logger
	UI       http.Handler // embedded SPA (nil = API only)

	headMu      sync.Mutex
	headHeight  int64
	headTsNs    int64
	headFetched time.Time
}

func (s *Server) Router() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))

	// Unauthenticated liveness probe for Render health checks.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	r.Group(func(r chi.Router) {
		r.Use(s.basicAuth)
		r.Route("/api", func(r chi.Router) {
			r.Get("/status", s.handleStatus)
			r.Get("/summary", s.handleSummary)
			r.Get("/pairs", s.handlePairs)
			r.Get("/solvers", s.handleSolvers)
			r.Get("/solvers/{id}", s.handleSolverDetail)
			r.Get("/legs", s.handleLegs)
			r.Get("/daily", s.handleDaily)
			r.Get("/routes/same-asset", s.handleSameAsset)
			r.Get("/routes/multihop", s.handleMultihop)
		})
		if s.UI != nil {
			r.Handle("/*", s.UI)
		}
	})
	return r
}

// basicAuth guards everything. An empty BASIC_AUTH_PASS disables auth (dev).
func (s *Server) basicAuth(next http.Handler) http.Handler {
	wantUser := sha256.Sum256([]byte(s.Cfg.BasicAuthUser))
	wantPass := sha256.Sum256([]byte(s.Cfg.BasicAuthPass))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.BasicAuthPass == "" {
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		gotUser := sha256.Sum256([]byte(user))
		gotPass := sha256.Sum256([]byte(pass))
		if !ok ||
			subtle.ConstantTimeCompare(gotUser[:], wantUser[:]) != 1 ||
			subtle.ConstantTimeCompare(gotPass[:], wantPass[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="intents-explorer"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// chainHead returns the chain head height/ts, cached for 10s so /api/status
// polling cannot eat into the neardata rate budget.
func (s *Server) chainHead(r *http.Request) (int64, int64) {
	s.headMu.Lock()
	defer s.headMu.Unlock()
	if time.Since(s.headFetched) < 10*time.Second {
		return s.headHeight, s.headTsNs
	}
	head, err := s.Client.Head(r.Context())
	if err != nil {
		s.Log.Warn("chain head fetch failed", "err", err)
		return s.headHeight, s.headTsNs // stale is better than nothing
	}
	s.headHeight, s.headTsNs = head.Height(), head.TimestampNs()
	s.headFetched = time.Now()
	return s.headHeight, s.headTsNs
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json failed", "err", err)
	}
}

func httpErr(w http.ResponseWriter, status int, err error) {
	http.Error(w, err.Error(), status)
}
