package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactionAndContextAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "info", "service", "test")
	ctx := WithAttrs(context.Background(), slog.String("request_id", "abc123"))
	log.InfoContext(ctx, "hello", "email", Email("Someone@Example.com"), "url", URL("https://shop.example.com/p/1?token=secret#x"))

	out := buf.String()
	for _, leaked := range []string{"Someone@Example.com", "someone@example.com", "secret"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("log leaked %q: %s", leaked, out)
		}
	}
	for _, want := range []string{`"request_id":"abc123"`, `"service":"test"`, `"email":"email:`, `"url":"https://shop.example.com/p/1"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	// The same address (any case) hashes identically, so events still correlate.
	if Email("a@b.com").LogValue().String() != Email("A@B.COM").LogValue().String() {
		t.Fatal("email hash is case-sensitive")
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "warn")
	log.Info("dropped")
	log.Warn("kept")
	if strings.Contains(buf.String(), "dropped") || !strings.Contains(buf.String(), "kept") {
		t.Fatalf("level filtering: %s", buf.String())
	}
}
