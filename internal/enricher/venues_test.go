package enricher

import "testing"

// Regression: the HL payload carries both "t" (open) and "T" (close). The
// decoder must keep the open time — an earlier version let "T" clobber "t"
// via case-insensitive matching, so no minute lookup ever hit.
func TestHLParseCandlesKeepsOpenTime(t *testing.T) {
	body := []byte(`[{"t":1784379240000,"T":1784379299999,"s":"SOL","i":"1m",` +
		`"o":"74.979","c":"74.981","h":"74.991","l":"74.979","v":"7.01","n":9}]`)
	data, err := hlParseCandles(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 {
		t.Fatalf("want 1 candle, got %d", len(data))
	}
	if data[0].T != 1784379240000 {
		t.Errorf("open time: got %d want 1784379240000 (close clobbered open?)", data[0].T)
	}
	mid := (float64(data[0].H) + float64(data[0].L)) / 2.0
	if mid != (74.991+74.979)/2.0 {
		t.Errorf("mid: got %v", mid)
	}
}
