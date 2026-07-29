// Package config reads runtime configuration from the environment.
//
// Secrets (DATABASE_URL, BASIC_AUTH_PASS) come from env vars only — never from
// command-line flags, so they cannot leak into process listings or logs.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL string

	// neardata.xyz block server (FASTNEAR). Optional API key appended as ?apiKey=.
	NeardataURL    string
	NeardataAPIKey string

	// NEAR JSON-RPC endpoint for contract view calls (solver balances).
	NearRPCURL string

	// HTTP server.
	Port          int
	BasicAuthUser string
	BasicAuthPass string // empty disables auth (local dev)

	// Follower.
	StartOverlapBlocks int // resume this many blocks before the stored cursor

	// Enricher.
	Venues        []string // subset of {"hl", "binance"}; first is primary
	EnrichEvery   time.Duration
	TokensURL     string
	TokensRefresh time.Duration
}

func FromEnv() (*Config, error) {
	c := &Config{
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		NeardataURL:        getenv("NEARDATA_URL", "https://mainnet.neardata.xyz"),
		NeardataAPIKey:     os.Getenv("NEARDATA_API_KEY"),
		NearRPCURL:         getenv("NEAR_RPC_URL", "https://free.rpc.fastnear.com"),
		Port:               getenvInt("PORT", 8080),
		BasicAuthUser:      getenv("BASIC_AUTH_USER", "admin"),
		BasicAuthPass:      os.Getenv("BASIC_AUTH_PASS"),
		StartOverlapBlocks: getenvInt("START_OVERLAP_BLOCKS", 30),
		EnrichEvery:        getenvDuration("ENRICH_EVERY", 30*time.Second),
		TokensURL:          getenv("TOKENS_URL", "https://1click.chaindefuser.com/v0/tokens"),
		TokensRefresh:      getenvDuration("TOKENS_REFRESH", time.Hour),
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	for _, v := range strings.Split(getenv("PRICE_VENUES", "hl"), ",") {
		v = strings.TrimSpace(strings.ToLower(v))
		if v == "" {
			continue
		}
		if v != "hl" && v != "binance" {
			return nil, fmt.Errorf("PRICE_VENUES: unknown venue %q (want hl, binance)", v)
		}
		c.Venues = append(c.Venues, v)
	}
	if len(c.Venues) == 0 {
		c.Venues = []string{"hl"}
	}
	return c, nil
}

func (c *Config) PrimaryVenue() string { return c.Venues[0] }

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvDuration(key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
