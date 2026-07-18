// Package metrics holds in-process counters surfaced by /api/status.
package metrics

import (
	"sync/atomic"
	"time"
)

type Metrics struct {
	StartedAt time.Time

	BlocksProcessed    atomic.Int64
	SkippedHeights     atomic.Int64
	LastBlockHeight    atomic.Int64
	LastBlockTsNs      atomic.Int64
	PendingSettlements atomic.Int64 // gauge: unresolved receipt map size

	SettlementsTotal  atomic.Int64
	SettlementsFailed atomic.Int64
	LegsTotal         atomic.Int64
	ParseErrors       atomic.Int64
	FollowerErrors    atomic.Int64

	EnrichedLegs    atomic.Int64
	NoReferenceLegs atomic.Int64
	EnricherErrors  atomic.Int64
	VenueFetches    atomic.Int64
}

func New() *Metrics { return &Metrics{StartedAt: time.Now().UTC()} }
