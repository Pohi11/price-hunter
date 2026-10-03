//go:build integration

package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
	"github.com/Pohi11/price-hunter/internal/store"
	"github.com/Pohi11/price-hunter/internal/testutil"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *store.Store {
	db, table := testutil.NewTable(t)
	return store.New(db, table)
}

func product(userID, id, hash string) domain.Product {
	next := t0.Add(-time.Minute)
	return domain.Product{
		ID: id, UserID: userID, Name: "Air Fryer", URL: "https://shop.example.com/p/" + id,
		CanonicalURL: "https://shop.example.com/p/" + id, URLHash: hash, Retailer: "generic", Host: "shop.example.com",
		Target: domain.Money{Minor: 10000, Currency: "USD"}, Frequency: domain.Frequency(6 * time.Hour),
		Status: domain.StatusActive, AlertState: domain.AlertArmed, Availability: domain.AvailabilityUnknown,
		NextCheckAt: &next, Version: 1, CreatedAt: t0, UpdatedAt: t0,
	}
}

func queued(p domain.Product, checkID string) domain.Check {
	return domain.Check{ID: checkID, ProductID: p.ID, UserID: p.UserID, Status: domain.CheckQueued, Trigger: domain.TriggerScheduled, QueuedAt: t0}
}

func TestCreateGetListDelete(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	p := product("u1", "P1", "h1")
	if err := s.CreateProduct(ctx, p, queued(p, "C1"), 10); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProduct(ctx, "u1", "P1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != p.Name || got.Target != p.Target || got.Frequency != p.Frequency || !got.NextCheckAt.Equal(*p.NextCheckAt) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, p)
	}
	if _, err := s.GetProduct(ctx, "other-user", "P1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("other user can read product: %v", err)
	}
	chk, err := s.GetCheck(ctx, "P1", "C1")
	if err != nil || chk.Status != domain.CheckQueued {
		t.Fatalf("first check: %+v %v", chk, err)
	}
	prof, _ := s.GetProfile(ctx, "u1")
	if prof.ProductCount != 1 {
		t.Fatalf("product_count = %d", prof.ProductCount)
	}

	deleted, err := s.DeleteProduct(ctx, "u1", "P1")
	if err != nil || deleted.ID != "P1" {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetProduct(ctx, "u1", "P1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("product still exists")
	}
	prof, _ = s.GetProfile(ctx, "u1")
	if prof.ProductCount != 0 {
		t.Fatalf("product_count after delete = %d", prof.ProductCount)
	}
	// The URL guard was removed, so the same URL can be added again.
	p2 := product("u1", "P2", "h1")
	if err := s.CreateProduct(ctx, p2, queued(p2, "C2"), 10); err != nil {
		t.Fatalf("re-add after delete: %v", err)
	}
	if _, err := s.DeleteProduct(ctx, "u1", "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestCreateDuplicateAndQuota(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p := product("u1", "P1", "same-hash")
	if err := s.CreateProduct(ctx, p, queued(p, "C1"), 2); err != nil {
		t.Fatal(err)
	}
	dup := product("u1", "P2", "same-hash")
	err := s.CreateProduct(ctx, dup, queued(dup, "C2"), 2)
	var de *store.DuplicateError
	if !errors.As(err, &de) || de.ExistingProductID != "P1" || !errors.Is(err, domain.ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	// Another user may track the same URL.
	other := product("u2", "P3", "same-hash")
	if err := s.CreateProduct(ctx, other, queued(other, "C3"), 2); err != nil {
		t.Fatalf("other user same URL: %v", err)
	}
	p4 := product("u1", "P4", "h4")
	if err := s.CreateProduct(ctx, p4, queued(p4, "C4"), 2); err != nil {
		t.Fatal(err)
	}
	p5 := product("u1", "P5", "h5")
	if err := s.CreateProduct(ctx, p5, queued(p5, "C5"), 2); !errors.Is(err, domain.ErrQuotaExceeded) {
		t.Fatalf("quota: %v", err)
	}
	// Failed transactions must not leave partial writes behind.
	if _, err := s.GetProduct(ctx, "u1", "P5"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("partial write after quota failure")
	}
}

func TestListProductsPagination(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("P%d", i)
		p := product("u1", id, "h"+id)
		if err := s.CreateProduct(ctx, p, queued(p, "C"+id), 50); err != nil {
			t.Fatal(err)
		}
	}
	var all []domain.Product
	cursor := ""
	for pages := 0; ; pages++ {
		page, next, err := s.ListProducts(ctx, "u1", 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page...)
		if next == "" {
			break
		}
		cursor = next
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(all) != 5 || all[0].ID != "P0" || all[4].ID != "P4" {
		t.Fatalf("got %d products", len(all))
	}
	if _, _, err := s.ListProducts(ctx, "u1", 2, "garbage!!"); !errors.Is(err, store.ErrInvalidCursor) {
		t.Fatalf("bad cursor: %v", err)
	}
}

func TestUpdateProductOptimisticLocking(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p := product("u1", "P1", "h1")
	_ = s.CreateProduct(ctx, p, queued(p, "C1"), 10)

	name := "Renamed"
	paused := domain.StatusPaused
	got, err := s.UpdateProduct(ctx, "u1", "P1", 1, store.ProductChanges{
		Name: &name, Status: &paused, Reschedule: true, NextCheckAt: nil, ResultingStatus: paused,
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name || got.Version != 2 || got.Status != paused || got.NextCheckAt != nil {
		t.Fatalf("update result %+v", got)
	}
	if _, err := s.UpdateProduct(ctx, "u1", "P1", 1, store.ProductChanges{Name: &name}, t0); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.UpdateProduct(ctx, "u1", "nope", 1, store.ProductChanges{Name: &name}, t0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing product: %v", err)
	}
	// Paused products are absent from the due index.
	for shard := 0; shard < store.DueShards; shard++ {
		due, _ := s.QueryDue(ctx, shard, t0.Add(time.Hour), 10)
		if len(due) != 0 {
			t.Fatalf("paused product is due: %+v", due)
		}
	}
}

func TestDueQueryAndLease(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("P%d", i)
		p := product("u1", id, "h"+id)
		next := t0.Add(time.Duration(i-4) * time.Minute) // P0..P3 due, P4..P7 in the future
		p.NextCheckAt = &next
		_ = s.CreateProduct(ctx, p, queued(p, "C"+id), 50)
	}
	var due []store.DueItem
	for shard := 0; shard < store.DueShards; shard++ {
		items, err := s.QueryDue(ctx, shard, t0.Add(-time.Second), 100)
		if err != nil {
			t.Fatal(err)
		}
		due = append(due, items...)
	}
	if len(due) != 4 {
		t.Fatalf("due = %d, want 4", len(due))
	}
	won, err := s.LeaseDue(ctx, due[0], t0.Add(15*time.Minute))
	if err != nil || !won {
		t.Fatalf("first lease: %v %v", won, err)
	}
	// A second scheduler holding the same stale read loses.
	won, err = s.LeaseDue(ctx, due[0], t0.Add(15*time.Minute))
	if err != nil || won {
		t.Fatalf("second lease should lose: %v %v", won, err)
	}
	p, _ := s.GetProduct(ctx, due[0].UserID, due[0].ProductID)
	if !p.NextCheckAt.Equal(t0.Add(15 * time.Minute)) {
		t.Fatalf("lease not recorded: %v", p.NextCheckAt)
	}
}

func TestCheckLifecycleIsIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p := product("u1", "P1", "h1")
	_ = s.CreateProduct(ctx, p, queued(p, "C1"), 10)
	msg := domain.CheckMessage{UserID: "u1", ProductID: "P1", CheckID: "C1", Trigger: domain.TriggerScheduled, EnqueuedAt: t0}

	begun, err := s.BeginCheck(ctx, msg, t0, 10*time.Minute)
	if err != nil || !begun {
		t.Fatalf("begin: %v %v", begun, err)
	}
	// Duplicate delivery while running: refused.
	if begun, _ := s.BeginCheck(ctx, msg, t0.Add(time.Minute), 10*time.Minute); begun {
		t.Fatal("duplicate begin while running")
	}

	price := domain.Money{Minor: 9400, Currency: "USD"}
	finished := t0.Add(2 * time.Second)
	next := t0.Add(6 * time.Hour)
	tracked := p
	tracked.Current, tracked.Lowest = &price, &price
	tracked.AlertState = domain.AlertTriggered
	tracked.LastCheckedAt, tracked.LastSuccessAt, tracked.LastAlertAt = &finished, &finished, &finished
	tracked.NextCheckAt = &next
	commit := store.CheckCommit{
		Check: domain.Check{ID: "C1", ProductID: "P1", UserID: "u1", Status: domain.CheckSucceeded, Outcome: domain.OutcomeOK,
			FinishedAt: &finished, DurationMS: 2000, Price: &price, Strategy: "jsonld", Alerted: true},
		Product: &tracked, ExpectedVersion: 1,
		Point: &domain.PricePoint{ProductID: "P1", ObservedAt: finished, Price: price, Availability: domain.InStock, Strategy: "jsonld", CheckID: "C1"},
		Notification: &domain.Notification{ID: "N1", UserID: "u1", ProductID: "P1", ProductName: p.Name, ProductURL: p.URL,
			Price: price, Target: p.Target, Status: domain.NotificationPending, CreatedAt: finished},
	}
	if err := s.CommitCheck(ctx, commit); err != nil {
		t.Fatal(err)
	}
	// Replaying the same commit (a duplicate that slipped past BeginCheck) is rejected.
	if err := s.CommitCheck(ctx, commit); !errors.Is(err, store.ErrCheckNotRunning) {
		t.Fatalf("replayed commit: %v", err)
	}
	// After completion, a redelivered message cannot restart the check.
	if begun, _ := s.BeginCheck(ctx, msg, t0.Add(time.Hour), 10*time.Minute); begun {
		t.Fatal("completed check restarted")
	}

	got, _ := s.GetProduct(ctx, "u1", "P1")
	if got.Current.Minor != 9400 || got.AlertState != domain.AlertTriggered || !got.NextCheckAt.Equal(next) || got.Version != 1 {
		t.Fatalf("product after commit: %+v", got)
	}
	points, _, err := s.ListPrices(ctx, store.PriceQuery{ProductID: "P1", From: t0, To: t0.Add(time.Hour), Limit: 10})
	if err != nil || len(points) != 1 || points[0].Price != price {
		t.Fatalf("price history: %+v %v", points, err)
	}
	n, err := s.GetNotification(ctx, "u1", "N1")
	if err != nil || n.Status != domain.NotificationPending {
		t.Fatalf("outbox: %+v %v", n, err)
	}
	chk, _ := s.GetCheck(ctx, "P1", "C1")
	if chk.Status != domain.CheckSucceeded || !chk.Alerted || chk.Price.Minor != 9400 {
		t.Fatalf("check record: %+v", chk)
	}
}

func TestStaleRunningCheckCanBeRetaken(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	msg := domain.CheckMessage{UserID: "u1", ProductID: "P1", CheckID: "C9", Trigger: domain.TriggerScheduled, EnqueuedAt: t0}
	if begun, _ := s.BeginCheck(ctx, msg, t0, 10*time.Minute); !begun {
		t.Fatal("first begin")
	}
	// The first worker crashed. 11 minutes later a redelivery may take over.
	if begun, _ := s.BeginCheck(ctx, msg, t0.Add(11*time.Minute), 10*time.Minute); !begun {
		t.Fatal("stale check not retaken")
	}
}

func TestCommitDetectsConcurrentUserEdit(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p := product("u1", "P1", "h1")
	_ = s.CreateProduct(ctx, p, queued(p, "C1"), 10)
	msg := domain.CheckMessage{UserID: "u1", ProductID: "P1", CheckID: "C1", EnqueuedAt: t0}
	_, _ = s.BeginCheck(ctx, msg, t0, time.Minute)

	newTarget := domain.Money{Minor: 8000, Currency: "USD"}
	if _, err := s.UpdateProduct(ctx, "u1", "P1", 1, store.ProductChanges{Target: &newTarget}, t0); err != nil {
		t.Fatal(err)
	}
	err := s.CommitCheck(ctx, store.CheckCommit{
		Check:   domain.Check{ID: "C1", ProductID: "P1", UserID: "u1", Status: domain.CheckSucceeded, Outcome: domain.OutcomeOK},
		Product: &p, ExpectedVersion: 1,
	})
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
	// Nothing was written: the check is still RUNNING.
	if chk, _ := s.GetCheck(ctx, "P1", "C1"); chk.Status != domain.CheckRunning {
		t.Fatalf("check status %s", chk.Status)
	}
}

func TestManualCheckCooldown(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p := product("u1", "P1", "h1")
	_ = s.CreateProduct(ctx, p, queued(p, "C0"), 10)
	c := domain.Check{ID: "M1", ProductID: "P1", UserID: "u1", Status: domain.CheckQueued, Trigger: domain.TriggerManual, QueuedAt: t0}
	if err := s.RequestManualCheck(ctx, "u1", "P1", c, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	c2 := c
	c2.ID, c2.QueuedAt = "M2", t0.Add(time.Minute)
	var cd *domain.CooldownError
	if err := s.RequestManualCheck(ctx, "u1", "P1", c2, 5*time.Minute); !errors.As(err, &cd) || cd.RetryAfterSeconds != 240 {
		t.Fatalf("cooldown: %v", err)
	}
	c3 := c
	c3.ID, c3.QueuedAt = "M3", t0.Add(6*time.Minute)
	if err := s.RequestManualCheck(ctx, "u1", "P1", c3, 5*time.Minute); err != nil {
		t.Fatalf("after cooldown: %v", err)
	}
	if err := s.RequestManualCheck(ctx, "u1", "missing", c3, 5*time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing product: %v", err)
	}
}

func TestPriceHistoryRangeAndPurge(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	var pts []domain.PricePoint
	for h := 0; h < 60; h++ {
		pts = append(pts, domain.PricePoint{ProductID: "P1", ObservedAt: t0.Add(time.Duration(h) * time.Hour),
			Price: domain.Money{Minor: int64(10000 + h), Currency: "USD"}, Availability: domain.InStock})
	}
	if err := s.PutPricePoints(ctx, pts); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.ListPrices(ctx, store.PriceQuery{ProductID: "P1", From: t0.Add(10 * time.Hour), To: t0.Add(19 * time.Hour), Limit: 100})
	if err != nil || len(got) != 10 || got[0].Price.Minor != 10010 {
		t.Fatalf("range query: %d points, %v", len(got), err)
	}
	latest, _, _ := s.ListPrices(ctx, store.PriceQuery{ProductID: "P1", From: t0, To: t0.Add(100 * time.Hour), Limit: 1, NewestFirst: true})
	if len(latest) != 1 || latest[0].Price.Minor != 10059 {
		t.Fatalf("latest: %+v", latest)
	}
	n, err := s.PurgeProductData(ctx, "P1")
	if err != nil || n != 60 {
		t.Fatalf("purge: %d %v", n, err)
	}
	after, _, _ := s.ListPrices(ctx, store.PriceQuery{ProductID: "P1", From: t0, To: t0.Add(100 * time.Hour), Limit: 100})
	if len(after) != 0 {
		t.Fatalf("%d points survived purge", len(after))
	}
}

func TestNotificationClaim(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p := product("u1", "P1", "h1")
	_ = s.CreateProduct(ctx, p, queued(p, "C1"), 10)
	_, _ = s.BeginCheck(ctx, domain.CheckMessage{UserID: "u1", ProductID: "P1", CheckID: "C1"}, t0, time.Minute)
	n := domain.Notification{ID: "N1", UserID: "u1", ProductID: "P1", Price: domain.Money{Minor: 1, Currency: "USD"},
		Target: domain.Money{Minor: 2, Currency: "USD"}, Status: domain.NotificationPending, CreatedAt: t0}
	_ = s.CommitCheck(ctx, store.CheckCommit{Check: domain.Check{ID: "C1", ProductID: "P1", Status: domain.CheckSucceeded}, Notification: &n})

	if ok, _ := s.ClaimNotification(ctx, "u1", "N1", t0, 5*time.Minute); !ok {
		t.Fatal("first claim")
	}
	if ok, _ := s.ClaimNotification(ctx, "u1", "N1", t0.Add(time.Minute), 5*time.Minute); ok {
		t.Fatal("double claim")
	}
	if err := s.CompleteNotification(ctx, "u1", "N1", domain.NotificationSent, "ses-123", "", t0); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetNotification(ctx, "u1", "N1")
	if got.Status != domain.NotificationSent || got.ProviderID != "ses-123" || got.Attempts != 1 {
		t.Fatalf("notification: %+v", got)
	}
	if ok, _ := s.ClaimNotification(ctx, "u1", "N1", t0.Add(time.Hour), 5*time.Minute); ok {
		t.Fatal("sent notification reclaimed")
	}
}

func TestDomainCircuit(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		h, err := s.RecordDomainFailure(ctx, "shop.example.com", domain.OutcomeBlocked, t0)
		if err != nil || h.ConsecutiveFailures != i {
			t.Fatalf("failure %d: %+v %v", i, h, err)
		}
	}
	if ok, _ := s.OpenCircuit(ctx, "shop.example.com", t0.Add(15*time.Minute), t0); !ok {
		t.Fatal("open")
	}
	if ok, _ := s.OpenCircuit(ctx, "shop.example.com", t0.Add(30*time.Minute), t0.Add(time.Minute)); ok {
		t.Fatal("re-opened while open")
	}
	h, _ := s.GetDomainHealth(ctx, "shop.example.com")
	if !h.IsOpen(t0.Add(time.Minute)) || h.OpenCount != 1 || h.ConsecutiveFailures != 0 {
		t.Fatalf("health: %+v", h)
	}
	_ = s.RecordDomainSuccess(ctx, "shop.example.com", t0.Add(20*time.Minute))
	h, _ = s.GetDomainHealth(ctx, "shop.example.com")
	if h.IsOpen(t0.Add(20*time.Minute)) || h.OpenCount != 0 {
		t.Fatalf("after success: %+v", h)
	}
}
