package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Pohi11/price-hunter/internal/domain"
)

type fakeStore struct {
	mu      sync.Mutex
	n       domain.Notification
	profile domain.Profile
	errMsg  string
}

func (f *fakeStore) ClaimNotification(_ context.Context, _, _ string, _ time.Time, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n.Status != domain.NotificationPending {
		return false, nil
	}
	f.n.Status = domain.NotificationSending
	f.n.Attempts++
	return true, nil
}

func (f *fakeStore) GetNotification(context.Context, string, string) (domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n, nil
}

func (f *fakeStore) CompleteNotification(_ context.Context, _, _ string, s domain.NotificationStatus, pid, msg string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n.Status, f.n.ProviderID, f.errMsg = s, pid, msg
	return nil
}

func (f *fakeStore) ReleaseNotification(_ context.Context, _, _, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n.Status, f.errMsg = domain.NotificationPending, msg
	return nil
}

func (f *fakeStore) GetProfile(context.Context, string) (domain.Profile, error) {
	return f.profile, nil
}

type fakeSender struct {
	sent []Email
	err  error
}

func (s *fakeSender) Send(_ context.Context, e Email) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.sent = append(s.sent, e)
	return "msg-1", nil
}

func setup(enabled bool, email string) (*fakeStore, *fakeSender, *Dispatcher) {
	fs := &fakeStore{
		n: domain.Notification{ID: "N1", UserID: "u1", ProductID: "P1", ProductName: "Air Fryer", ProductURL: "https://shop.example.com/p/1",
			Price: domain.Money{Minor: 9400, Currency: "USD"}, Target: domain.Money{Minor: 10000, Currency: "USD"}, Status: domain.NotificationPending},
		profile: domain.Profile{UserID: "u1", Email: email, NotificationsEnabled: enabled},
	}
	s := &fakeSender{}
	return fs, s, NewDispatcher(fs, s, nil, "https://app.example.com/", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestDispatchSendsOnce(t *testing.T) {
	fs, s, d := setup(true, "me@example.com")
	for i := 0; i < 3; i++ { // e.g. redelivered stream records
		if err := d.Dispatch(context.Background(), "u1", "N1"); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.sent) != 1 || fs.n.Status != domain.NotificationSent || fs.n.ProviderID != "msg-1" {
		t.Fatalf("sent=%d status=%s", len(s.sent), fs.n.Status)
	}
	e := s.sent[0]
	if e.To != "me@example.com" || e.Subject != "Price alert: Air Fryer is now $94.00" {
		t.Fatalf("email = %+v", e)
	}
	for _, want := range []string{"$100.00", "$6.00 under target", "https://shop.example.com/p/1", "https://app.example.com/products/P1", "/settings"} {
		if !strings.Contains(e.Text, want) || !strings.Contains(e.HTML, want) {
			t.Errorf("email missing %q", want)
		}
	}
}

func TestDispatchTransientFailureReleasesForRetry(t *testing.T) {
	fs, s, d := setup(true, "me@example.com")
	s.err = errors.New("ses: throttled")
	if err := d.Dispatch(context.Background(), "u1", "N1"); err == nil {
		t.Fatal("transient failure must return an error so the caller retries")
	}
	if fs.n.Status != domain.NotificationPending {
		t.Fatalf("status = %s, want PENDING", fs.n.Status)
	}
	s.err = nil
	if err := d.Dispatch(context.Background(), "u1", "N1"); err != nil || fs.n.Status != domain.NotificationSent || fs.n.Attempts != 2 {
		t.Fatalf("retry: %v %s attempts=%d", err, fs.n.Status, fs.n.Attempts)
	}
}

func TestDispatchPermanentFailure(t *testing.T) {
	fs, s, d := setup(true, "bounce@example.com")
	s.err = &PermanentError{Err: errors.New("address on suppression list")}
	if err := d.Dispatch(context.Background(), "u1", "N1"); err != nil {
		t.Fatalf("permanent failure should not be retried: %v", err)
	}
	if fs.n.Status != domain.NotificationFailed {
		t.Fatalf("status = %s", fs.n.Status)
	}
}

func TestDispatchSkipsWhenDisabledOrNoEmail(t *testing.T) {
	for _, tc := range []struct {
		enabled bool
		email   string
	}{{false, "me@example.com"}, {true, ""}} {
		fs, s, d := setup(tc.enabled, tc.email)
		if err := d.Dispatch(context.Background(), "u1", "N1"); err != nil {
			t.Fatal(err)
		}
		if len(s.sent) != 0 || fs.n.Status != domain.NotificationSkipped {
			t.Fatalf("%+v: sent=%d status=%s", tc, len(s.sent), fs.n.Status)
		}
	}
}

func TestRenderEscapesHTML(t *testing.T) {
	n := domain.Notification{ProductID: "P1", ProductName: `<script>alert(1)</script>`, ProductURL: "https://shop.example.com/p",
		Price: domain.Money{Minor: 129999, Currency: "USD"}, Target: domain.Money{Minor: 129999, Currency: "USD"}}
	e, err := Render(n, "me@example.com", "https://app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.HTML, "<script>") {
		t.Fatal("product name not escaped in HTML email")
	}
	if !strings.Contains(e.Subject, "$1,299.99") || strings.Contains(e.Text, "under target") {
		t.Fatalf("formatting: %q", e.Subject)
	}
}
