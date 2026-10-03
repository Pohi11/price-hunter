// Package telemetry sets up structured logging and CloudWatch metrics.
package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
)

// NewLogger returns a JSON slog logger. In Lambda, stdout goes to CloudWatch
// Logs, where JSON fields are queryable with Logs Insights.
func NewLogger(w io.Writer, level string, attrs ...any) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		lv = slog.LevelInfo
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv})
	return slog.New(contextHandler{h}).With(attrs...)
}

type ctxKey struct{}

// WithAttrs returns ctx carrying log attributes (request_id, check_id, ...)
// that every log call made with that context will include.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	existing, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	merged := make([]slog.Attr, 0, len(existing)+len(attrs))
	merged = append(merged, existing...)
	merged = append(merged, attrs...)
	return context.WithValue(ctx, ctxKey{}, merged)
}

// contextHandler adds attributes stored by WithAttrs to each record.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs, ok := ctx.Value(ctxKey{}).([]slog.Attr); ok {
		r.AddAttrs(attrs...)
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}

// Email is a log-safe email: it renders as a short hash so logs can
// correlate events for one address without storing the address.
type Email string

func (e Email) LogValue() slog.Value {
	if e == "" {
		return slog.StringValue("")
	}
	sum := sha256.Sum256([]byte(strings.ToLower(string(e))))
	return slog.StringValue("email:" + hex.EncodeToString(sum[:])[:12])
}

// URL is a log-safe URL: the query string is dropped (it may carry tokens).
type URL string

func (u URL) LogValue() slog.Value {
	s := string(u)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	return slog.StringValue(s)
}
