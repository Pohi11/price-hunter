package products

import (
	"testing"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
)

func series(n int, at func(i int) int64) []domain.PricePoint {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	pts := make([]domain.PricePoint, n)
	for i := range pts {
		pts[i] = domain.PricePoint{ObservedAt: t0.Add(time.Duration(i) * time.Hour), Price: domain.Money{Minor: at(i), Currency: "USD"}}
	}
	return pts
}

func TestDownsampleKeepsExtremesAndOrder(t *testing.T) {
	pts := series(1000, func(i int) int64 {
		switch i {
		case 137:
			return 100 // deepest dip
		case 640:
			return 99999 // highest spike
		}
		return 5000 + int64(i%7)
	})
	out := Downsample(pts, 100)
	if len(out) > 100 {
		t.Fatalf("len = %d, want <= 100", len(out))
	}
	var sawMin, sawMax bool
	for i, p := range out {
		if i > 0 && p.ObservedAt.Before(out[i-1].ObservedAt) {
			t.Fatal("output not in time order")
		}
		sawMin = sawMin || p.Price.Minor == 100
		sawMax = sawMax || p.Price.Minor == 99999
	}
	if !sawMin || !sawMax {
		t.Fatalf("extremes lost: min=%v max=%v", sawMin, sawMax)
	}
}

func TestDownsampleSmallInputUnchanged(t *testing.T) {
	pts := series(10, func(i int) int64 { return int64(i) })
	if out := Downsample(pts, 100); len(out) != 10 {
		t.Fatalf("len = %d", len(out))
	}
}

func TestSummaryIgnoresSuspectPoints(t *testing.T) {
	pts := series(3, func(i int) int64 { return []int64{5000, 50, 4800}[i] })
	pts[1].Suspect = true
	var h History
	h.summarize(pts)
	if h.Min.Minor != 4800 || h.Max.Minor != 5000 || h.Latest.Price.Minor != 4800 || h.Count != 3 {
		t.Fatalf("summary: min=%v max=%v latest=%v", h.Min, h.Max, h.Latest)
	}
}

func TestParseTarget(t *testing.T) {
	if m, fe := parseTarget("100.00", ""); fe != nil || m != (domain.Money{Minor: 10000, Currency: "USD"}) {
		t.Fatalf("default currency: %v %v", m, fe)
	}
	for _, bad := range []struct{ amount, cur, code string }{
		{"", "USD", "REQUIRED"}, {"abc", "USD", "INVALID"}, {"0", "USD", "OUT_OF_RANGE"},
		{"10000001", "USD", "OUT_OF_RANGE"}, {"5", "XYZ", "UNSUPPORTED_CURRENCY"},
	} {
		if _, fe := parseTarget(bad.amount, bad.cur); fe == nil || fe.Code != bad.code {
			t.Errorf("parseTarget(%q,%q) = %v, want %s", bad.amount, bad.cur, fe, bad.code)
		}
	}
}
