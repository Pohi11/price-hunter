package notify

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Pohi11/price-hunter/internal/telemetry"
)

// LogSender "sends" email by logging it and, if Dir is set, writing the
// HTML to Dir so it can be opened in a browser. Used locally.
type LogSender struct {
	Log *slog.Logger
	Dir string
}

func (s LogSender) Send(ctx context.Context, e Email) (string, error) {
	id := fmt.Sprintf("log-%d", time.Now().UnixNano())
	attrs := []any{"to", telemetry.Email(e.To), "subject", e.Subject, "message_id", id}
	if s.Dir != "" {
		if err := os.MkdirAll(s.Dir, 0o750); err != nil {
			return "", err
		}
		path := filepath.Join(s.Dir, id+".html")
		if err := os.WriteFile(path, []byte(e.HTML), 0o600); err != nil {
			return "", err
		}
		attrs = append(attrs, "file", path)
	}
	s.Log.InfoContext(ctx, "email (log mode)", attrs...)
	return id, nil
}
