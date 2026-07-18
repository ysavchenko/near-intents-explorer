// intents-explorer: continuous NEAR Intents swap-stats platform.
// One process runs three loops — block follower, price enricher, HTTP API+UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"intents-explorer/internal/api"
	"intents-explorer/internal/assets"
	"intents-explorer/internal/config"
	"intents-explorer/internal/db"
	"intents-explorer/internal/enricher"
	"intents-explorer/internal/follower"
	"intents-explorer/internal/intents"
	"intents-explorer/internal/metrics"
	"intents-explorer/internal/neardata"
	"intents-explorer/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)
	if err := run(log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	log.Info("database ready")

	// Asset registry: 1Click token list, with the DB snapshot as fallback so a
	// tokens-API outage never blocks indexing.
	registry := assets.NewRegistry()
	if err := registry.Fetch(ctx, cfg.TokensURL); err != nil {
		log.Warn("token list fetch failed; falling back to DB snapshot", "err", err)
		if n, dbErr := follower.LoadAssetsFromDB(ctx, pool, registry); dbErr != nil || n == 0 {
			return fmt.Errorf("no asset registry available (fetch: %v, db: %v, rows: %d)", err, dbErr, 0)
		}
	} else if err := follower.SyncAssets(ctx, pool, registry); err != nil {
		log.Warn("asset db sync failed", "err", err)
	}
	log.Info("asset registry loaded", "assets", registry.Len())

	solvers := intents.NewSolverSet()
	if err := follower.LoadSolverState(ctx, pool, solvers); err != nil {
		return fmt.Errorf("load solver state: %w", err)
	}

	m := metrics.New()
	client := neardata.New(cfg.NeardataURL, cfg.NeardataAPIKey)

	// Periodic token-list refresh keeps decimals/labels current for new assets.
	go func() {
		ticker := time.NewTicker(cfg.TokensRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := registry.Fetch(ctx, cfg.TokensURL); err != nil {
					log.Warn("token list refresh failed", "err", err)
					continue
				}
				if err := follower.SyncAssets(ctx, pool, registry); err != nil {
					log.Warn("asset db sync failed", "err", err)
				}
			}
		}
	}()

	f := &follower.Follower{
		Pool: pool, Client: client, Registry: registry, Solvers: solvers,
		Metrics: m, Overlap: int64(cfg.StartOverlapBlocks), Log: log.With("loop", "follower"),
	}
	go func() {
		if err := f.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("follower stopped", "err", err)
			stop() // a dead follower means a dead service; let Render restart us
		}
	}()

	e := &enricher.Enricher{
		Pool: pool, Registry: registry, Metrics: m,
		Venues: cfg.Venues, Every: cfg.EnrichEvery, Log: log.With("loop", "enricher"),
	}
	go func() {
		if err := e.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("enricher stopped", "err", err)
			stop()
		}
	}()

	srv := &api.Server{
		Pool: pool, Client: client, Registry: registry, Solvers: solvers,
		Metrics: m, Cfg: cfg, Log: log.With("loop", "api"), UI: web.Handler(),
	}
	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
	}()
	log.Info("http listening", "port", cfg.Port, "auth", cfg.BasicAuthPass != "")
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
