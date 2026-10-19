package enricher

import (
	"testing"
	"time"
)

func TestNextChunk(t *testing.T) {
	since := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	chunk := 6 * time.Hour

	// A full chunk above the floor is not the last one.
	lo, last := nextChunk(since.Add(10*time.Hour), since, chunk)
	if last || !lo.Equal(since.Add(4*time.Hour)) {
		t.Errorf("full chunk: lo=%v last=%v", lo, last)
	}
	// A partial chunk clamps to the floor and is the last one.
	lo, last = nextChunk(lo, since, chunk)
	if !last || !lo.Equal(since) {
		t.Errorf("partial chunk: lo=%v last=%v", lo, last)
	}
	// Landing exactly on the floor is also the last one.
	lo, last = nextChunk(since.Add(chunk), since, chunk)
	if !last || !lo.Equal(since) {
		t.Errorf("exact chunk: lo=%v last=%v", lo, last)
	}
	// A cursor already at or below the floor yields an empty final chunk.
	lo, last = nextChunk(since, since, chunk)
	if !last || !lo.Equal(since) {
		t.Errorf("at floor: lo=%v last=%v", lo, last)
	}
}
