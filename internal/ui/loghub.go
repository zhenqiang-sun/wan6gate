package ui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogHub is a small in-memory pub/sub for log lines, plus a backlog ring buffer.
type LogHub struct {
	mu       sync.Mutex
	subs     map[chan string]struct{}
	backlog  []string
	capacity int
}

func NewLogHub(capacity int) *LogHub {
	if capacity <= 0 {
		capacity = 200
	}
	return &LogHub{
		subs:     make(map[chan string]struct{}),
		backlog:  make([]string, 0, capacity),
		capacity: capacity,
	}
}

func (h *LogHub) Publish(line string) {
	h.mu.Lock()
	h.backlog = append(h.backlog, line)
	if len(h.backlog) > h.capacity {
		h.backlog = h.backlog[len(h.backlog)-h.capacity:]
	}
	subs := make([]chan string, 0, len(h.subs))
	for c := range h.subs {
		subs = append(subs, c)
	}
	h.mu.Unlock()
	for _, c := range subs {
		select {
		case c <- line:
		default:
			// slow subscriber, drop
		}
	}
}

func (h *LogHub) Subscribe() chan string {
	ch := make(chan string, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *LogHub) Unsubscribe(ch chan string) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
	close(ch)
}

func (h *LogHub) Backlog(n int) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n <= 0 || n > len(h.backlog) {
		n = len(h.backlog)
	}
	out := make([]string, n)
	copy(out, h.backlog[len(h.backlog)-n:])
	return out
}

// LogHubHandler is a slog.Handler that fans every record into the LogHub
// (for SSE) and a downstream handler (for file/stderr).
type LogHubHandler struct {
	hub  *LogHub
	next slog.Handler
}

func NewLogHubHandler(hub *LogHub, next slog.Handler) *LogHubHandler {
	return &LogHubHandler{hub: hub, next: next}
}

func (h *LogHubHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *LogHubHandler) Handle(ctx context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Time.Format("15:04:05"))
	sb.WriteByte(' ')
	sb.WriteString(r.Level.String())
	sb.WriteByte(' ')
	sb.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		sb.WriteByte(' ')
		sb.WriteString(a.Key)
		sb.WriteByte('=')
		sb.WriteString(fmt.Sprint(a.Value.Any()))
		return true
	})
	h.hub.Publish(sb.String())
	return h.next.Handle(ctx, r)
}

func (h *LogHubHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &LogHubHandler{hub: h.hub, next: h.next.WithAttrs(as)}
}

func (h *LogHubHandler) WithGroup(name string) slog.Handler {
	return &LogHubHandler{hub: h.hub, next: h.next.WithGroup(name)}
}

// MakeLogger returns a slog.Logger that writes to writers (stderr/file) and the LogHub.
func MakeLogger(hub *LogHub, writers ...io.Writer) *slog.Logger {
	w := io.MultiWriter(writers...)
	base := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{Key: "ts", Value: slog.StringValue(a.Value.Time().Format(time.RFC3339))}
			}
			return a
		},
	})
	return slog.New(NewLogHubHandler(hub, base))
}
