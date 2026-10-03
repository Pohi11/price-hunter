package products

import (
	"context"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/store"
)

// HistoryQuery selects price history. With MaxPoints > 0 the whole range is
// loaded and downsampled for charting; otherwise results are paged raw.
type HistoryQuery struct {
	From, To  time.Time
	Limit     int
	Cursor    string
	MaxPoints int
}

// History is a product's price history plus summary statistics.
type History struct {
	Currency   domain.Currency
	Resolution string // "raw" or "downsampled"
	Points     []domain.PricePoint
	Min, Max   *domain.Money
	Latest     *domain.PricePoint
	Count      int
	NextCursor string
}

// maxHistoryRange and maxLoadedPoints bound the work a single request can cause.
const (
	maxHistoryRange = 2 * 366 * 24 * time.Hour
	maxLoadedPoints = 20_000
)

// Prices returns history for a product the user owns.
func (s *Service) Prices(ctx context.Context, userID, productID string, q HistoryQuery) (History, error) {
	p, err := s.store.GetProduct(ctx, userID, productID)
	if err != nil {
		return History{}, err
	}
	now := s.now()
	if q.To.IsZero() {
		q.To = now
	}
	if q.From.IsZero() {
		q.From = q.To.Add(-30 * 24 * time.Hour)
	}
	verr := &domain.ValidationError{}
	if !q.From.Before(q.To) {
		verr.Add("from", "INVALID_RANGE", "from must be before to")
	}
	if q.To.Sub(q.From) > maxHistoryRange {
		verr.Add("from", "RANGE_TOO_LARGE", "range must be at most 2 years")
	}
	if err := verr.OrNil(); err != nil {
		return History{}, err
	}

	h := History{Currency: p.Target.Currency, Resolution: "raw"}
	if q.MaxPoints <= 0 {
		limit := q.Limit
		if limit <= 0 || limit > 1000 {
			limit = 500
		}
		pts, next, err := s.store.ListPrices(ctx, store.PriceQuery{ProductID: productID, From: q.From, To: q.To, Limit: limit, Cursor: q.Cursor})
		if err != nil {
			return History{}, err
		}
		h.Points, h.NextCursor = pts, next
		h.summarize(pts)
		return h, nil
	}

	var all []domain.PricePoint
	cursor := ""
	for len(all) < maxLoadedPoints {
		pts, next, err := s.store.ListPrices(ctx, store.PriceQuery{ProductID: productID, From: q.From, To: q.To, Limit: 1000, Cursor: cursor})
		if err != nil {
			return History{}, err
		}
		all = append(all, pts...)
		if next == "" {
			break
		}
		cursor = next
	}
	h.summarize(all)
	if len(all) > q.MaxPoints {
		h.Points, h.Resolution = Downsample(all, q.MaxPoints), "downsampled"
	} else {
		h.Points = all
	}
	return h, nil
}

func (h *History) summarize(pts []domain.PricePoint) {
	h.Count = len(pts)
	for i := range pts {
		pt := pts[i]
		if pt.Suspect {
			continue // unconfirmed glitches don't count as the lowest price ever
		}
		if h.Min == nil || pt.Price.Minor < h.Min.Minor {
			m := pt.Price
			h.Min = &m
		}
		if h.Max == nil || pt.Price.Minor > h.Max.Minor {
			m := pt.Price
			h.Max = &m
		}
		if h.Latest == nil || pt.ObservedAt.After(h.Latest.ObservedAt) {
			h.Latest = &pt
		}
	}
}

// Downsample reduces points (sorted by time) to at most maxPoints by
// splitting the time span into maxPoints/2 buckets and keeping each bucket's
// minimum and maximum. Unlike averaging, this preserves the dips that
// matter for a price tracker.
func Downsample(points []domain.PricePoint, maxPoints int) []domain.PricePoint {
	if len(points) <= maxPoints || maxPoints < 2 {
		return points
	}
	buckets := maxPoints / 2
	start, end := points[0].ObservedAt, points[len(points)-1].ObservedAt
	span := end.Sub(start)
	if span <= 0 {
		return points[:maxPoints]
	}
	out := make([]domain.PricePoint, 0, maxPoints)
	idx := 0
	for b := 0; b < buckets && idx < len(points); b++ {
		bucketEnd := start.Add(time.Duration(float64(span) * float64(b+1) / float64(buckets)))
		lo, hi := -1, -1
		for ; idx < len(points) && (!points[idx].ObservedAt.After(bucketEnd) || b == buckets-1); idx++ {
			if lo == -1 || points[idx].Price.Minor < points[lo].Price.Minor {
				lo = idx
			}
			if hi == -1 || points[idx].Price.Minor > points[hi].Price.Minor {
				hi = idx
			}
		}
		switch {
		case lo == -1:
			continue
		case lo == hi:
			out = append(out, points[lo])
		case lo < hi:
			out = append(out, points[lo], points[hi])
		default:
			out = append(out, points[hi], points[lo])
		}
	}
	return out
}
